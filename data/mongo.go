package data

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/sweetrpg/entitlements-api/constants"
	"github.com/sweetrpg/entitlements-api/models"
	mcore "github.com/sweetrpg/model-core.go/models"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoStore implements Store on MongoDB. It is a thin translation layer: the rules live in the
// catalog package and are tested against MemoryStore; this file is exercised against a live
// database only.
type MongoStore struct {
	db *mongo.Database
}

// NewMongoStore wraps db.
func NewMongoStore(db *mongo.Database) *MongoStore { return &MongoStore{db: db} }

// notDeleted is merged into every catalog read so a new read path inherits the tombstone filter.
var notDeleted = bson.E{Key: "deleted_at", Value: nil}

// indexSpecs is every index the service relies on, by collection. It is a pure function so a test
// can check each definition against what MongoDB accepts without a database.
func indexSpecs() map[string][]mongo.IndexModel {
	// MongoDB partial indexes cannot express "deleted_at is absent" ($exists: false is rejected),
	// so live documents carry live: true, set on insert and removed on soft delete.
	liveOnly := options.Index().SetUnique(true).
		SetPartialFilterExpression(bson.D{{Key: "live", Value: true}})
	return map[string][]mongo.IndexModel{
		constants.KeysCollection:  {{Keys: bson.D{{Key: "key", Value: 1}}, Options: liveOnly}},
		constants.TiersCollection: {{Keys: bson.D{{Key: "slug", Value: 1}}, Options: liveOnly}},
		constants.ProductsCollection: {
			{Keys: bson.D{{Key: "slug", Value: 1}}, Options: liveOnly},
			{Keys: bson.D{{Key: "stripe_price_id", Value: 1}}},
		},
		constants.GrantsCollection: {
			{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "status", Value: 1}}},
			{Keys: bson.D{{Key: "source", Value: 1}, {Key: "external_id", Value: 1}, {Key: "key", Value: 1}}, Options: options.Index().SetUnique(true).
				SetPartialFilterExpression(bson.D{{Key: "external_id", Value: bson.D{{Key: "$exists", Value: true}}}})},
			{Keys: bson.D{{Key: "refs", Value: 1}}},
		},
		constants.StripeCustomersCollection: {{Keys: bson.D{{Key: "user_id", Value: 1}}, Options: options.Index().SetUnique(true)}},
		constants.AuditCollection:           {{Keys: bson.D{{Key: "at", Value: -1}}}},
	}
}

// EnsureIndexes creates the unique-among-live indexes and the lookup indexes. Safe to call on
// every startup.
func (s *MongoStore) EnsureIndexes(ctx context.Context) error {
	// The first grants index was unique on (source, external_id) alone, which would block a
	// bundle's several grants sharing one external ID. Drop it; a missing index is not an error.
	_, _ = s.db.Collection(constants.GrantsCollection).Indexes().DropOne(ctx, "source_1_external_id_1")
	if err := s.backfillLive(ctx); err != nil {
		return err
	}
	for coll, ms := range indexSpecs() {
		if _, err := s.db.Collection(coll).Indexes().CreateMany(ctx, ms); err != nil {
			return fmt.Errorf("indexes on %s: %w", coll, err)
		}
	}
	return nil
}

// liveCollections are the soft-deleted catalog collections whose uniqueness is among live rows.
var liveCollections = []string{constants.KeysCollection, constants.TiersCollection, constants.ProductsCollection}

// backfillLive marks documents written before the live flag existed. It is idempotent and only
// touches live documents that lack the flag, so it is a no-op after the first startup.
func (s *MongoStore) backfillLive(ctx context.Context) error {
	for _, coll := range liveCollections {
		f := bson.D{{Key: "live", Value: bson.D{{Key: "$exists", Value: false}}}, notDeleted}
		if _, err := s.db.Collection(coll).UpdateMany(ctx, f, bson.D{{Key: "$set", Value: bson.D{{Key: "live", Value: true}}}}); err != nil {
			return fmt.Errorf("backfill live on %s: %w", coll, err)
		}
	}
	return nil
}

func (s *MongoStore) insert(ctx context.Context, coll string, doc any) error {
	_, err := s.db.Collection(coll).InsertOne(ctx, doc)
	if mongo.IsDuplicateKeyError(err) {
		return ErrDuplicate
	}
	return err
}

func find[T any](ctx context.Context, c *mongo.Collection, filter bson.D, opts *options.FindOptions) ([]T, error) {
	cur, err := c.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	var out []T
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func page(sortKey string, dir int, limit, skip int64) *options.FindOptions {
	o := options.Find().SetSort(bson.D{{Key: sortKey, Value: dir}}).SetSkip(skip)
	if limit > 0 {
		o.SetLimit(limit)
	}
	return o
}

func (s *MongoStore) InsertKey(ctx context.Context, k *models.Key, actor string) error {
	mcore.StampCreate(&k.Auditable, actor, time.Now().UTC())
	k.Live = true
	return s.insert(ctx, constants.KeysCollection, k)
}

func (s *MongoStore) GetKeys(ctx context.Context, names []string) ([]models.Key, error) {
	f := bson.D{notDeleted, {Key: "key", Value: bson.D{{Key: "$in", Value: names}}}}
	return find[models.Key](ctx, s.db.Collection(constants.KeysCollection), f, nil)
}

func (s *MongoStore) ListKeys(ctx context.Context, prefix string, limit, skip int64) ([]models.Key, error) {
	f := bson.D{notDeleted, {Key: "key", Value: bson.D{{Key: "$regex", Value: "^" + regexp.QuoteMeta(prefix)}}}}
	return find[models.Key](ctx, s.db.Collection(constants.KeysCollection), f, page("key", 1, limit, skip))
}

func (s *MongoStore) UpdateKey(ctx context.Context, name, description string, ttl int, max *int64, actor string) (*models.Key, error) {
	set := bson.D{
		{Key: "description", Value: description}, {Key: "ttl_seconds", Value: ttl},
		{Key: "updated_at", Value: time.Now().UTC()}, {Key: "updated_by", Value: actor},
	}
	update := bson.D{{Key: "$set", Value: set}}
	if max == nil {
		update = append(update, bson.E{Key: "$unset", Value: bson.D{{Key: "max", Value: ""}}})
	} else {
		set = append(set, bson.E{Key: "max", Value: *max})
		update = bson.D{{Key: "$set", Value: set}}
	}
	var out models.Key
	err := s.db.Collection(constants.KeysCollection).
		FindOneAndUpdate(ctx, bson.D{notDeleted, {Key: "key", Value: name}}, update,
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	return &out, mapNoDoc(err)
}

func (s *MongoStore) DeleteKey(ctx context.Context, name, actor string) error {
	return s.softDelete(ctx, constants.KeysCollection, bson.D{notDeleted, {Key: "key", Value: name}}, actor)
}

func (s *MongoStore) softDelete(ctx context.Context, coll string, filter bson.D, actor string) error {
	set := bson.D{{Key: "deleted_at", Value: time.Now().UTC()}, {Key: "deleted_by", Value: actor}}
	update := bson.D{{Key: "$set", Value: set}, {Key: "$unset", Value: bson.D{{Key: "live", Value: ""}}}}
	res, err := s.db.Collection(coll).UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *MongoStore) InsertTier(ctx context.Context, t *models.Tier, actor string) error {
	mcore.StampCreate(&t.Auditable, actor, time.Now().UTC())
	t.Live = true
	return s.insert(ctx, constants.TiersCollection, t)
}

func (s *MongoStore) GetTiers(ctx context.Context, slugs []string) ([]models.Tier, error) {
	f := bson.D{notDeleted, {Key: "slug", Value: bson.D{{Key: "$in", Value: slugs}}}}
	return find[models.Tier](ctx, s.db.Collection(constants.TiersCollection), f, nil)
}

func (s *MongoStore) ListTiers(ctx context.Context, limit, skip int64) ([]models.Tier, error) {
	return find[models.Tier](ctx, s.db.Collection(constants.TiersCollection), bson.D{notDeleted}, page("slug", 1, limit, skip))
}

func (s *MongoStore) UpdateTier(ctx context.Context, t *models.Tier, actor string) (*models.Tier, error) {
	set := bson.D{
		{Key: "name", Value: t.Name}, {Key: "description", Value: t.Description},
		{Key: "values", Value: t.Values}, {Key: "stripe_price_id", Value: t.StripePriceID},
		{Key: "updated_at", Value: time.Now().UTC()}, {Key: "updated_by", Value: actor},
	}
	var out models.Tier
	err := s.db.Collection(constants.TiersCollection).
		FindOneAndUpdate(ctx, bson.D{notDeleted, {Key: "slug", Value: t.Slug}}, bson.D{{Key: "$set", Value: set}},
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	return &out, mapNoDoc(err)
}

func (s *MongoStore) DeleteTier(ctx context.Context, slug, actor string) error {
	return s.softDelete(ctx, constants.TiersCollection, bson.D{notDeleted, {Key: "slug", Value: slug}}, actor)
}

func (s *MongoStore) InsertProduct(ctx context.Context, p *models.Product, actor string) error {
	mcore.StampCreate(&p.Auditable, actor, time.Now().UTC())
	p.Live = true
	return s.insert(ctx, constants.ProductsCollection, p)
}

func (s *MongoStore) GetProducts(ctx context.Context, slugs []string) ([]models.Product, error) {
	f := bson.D{notDeleted, {Key: "slug", Value: bson.D{{Key: "$in", Value: slugs}}}}
	return find[models.Product](ctx, s.db.Collection(constants.ProductsCollection), f, nil)
}

func (s *MongoStore) ListProducts(ctx context.Context, limit, skip int64) ([]models.Product, error) {
	return find[models.Product](ctx, s.db.Collection(constants.ProductsCollection), bson.D{notDeleted}, page("slug", 1, limit, skip))
}

func (s *MongoStore) UpdateProduct(ctx context.Context, p *models.Product, actor string) (*models.Product, error) {
	set := bson.D{
		{Key: "name", Value: p.Name}, {Key: "description", Value: p.Description},
		{Key: "billing", Value: p.Billing}, {Key: "repeatable", Value: p.Repeatable}, {Key: "plan_group", Value: p.PlanGroup},
		{Key: "stripe_price_id", Value: p.StripePriceID}, {Key: "entitlements", Value: p.Entitlements},
		{Key: "updated_at", Value: time.Now().UTC()}, {Key: "updated_by", Value: actor},
	}
	var out models.Product
	err := s.db.Collection(constants.ProductsCollection).
		FindOneAndUpdate(ctx, bson.D{notDeleted, {Key: "slug", Value: p.Slug}}, bson.D{{Key: "$set", Value: set}},
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	return &out, mapNoDoc(err)
}

func (s *MongoStore) ProductByPriceID(ctx context.Context, priceID string) (*models.Product, error) {
	var p models.Product
	err := s.db.Collection(constants.ProductsCollection).
		FindOne(ctx, bson.D{notDeleted, {Key: "stripe_price_id", Value: priceID}}).Decode(&p)
	return &p, mapNoDoc(err)
}

func (s *MongoStore) DeleteProduct(ctx context.Context, slug, actor string) error {
	return s.softDelete(ctx, constants.ProductsCollection, bson.D{notDeleted, {Key: "slug", Value: slug}}, actor)
}

func (s *MongoStore) DefaultTierID(ctx context.Context) (string, error) {
	var cfg models.CatalogConfig
	err := s.db.Collection(constants.CatalogConfigCollection).
		FindOne(ctx, bson.D{{Key: "_id", Value: constants.CatalogConfigID}}).Decode(&cfg)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	return cfg.DefaultTierID, err
}

func (s *MongoStore) SetDefaultTierID(ctx context.Context, slug, actor string) error {
	now := time.Now().UTC()
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "default_tier_id", Value: slug},
			{Key: "updated_at", Value: now}, {Key: "updated_by", Value: actor},
		}},
		{Key: "$setOnInsert", Value: bson.D{{Key: "created_at", Value: now}, {Key: "created_by", Value: actor}}},
	}
	_, err := s.db.Collection(constants.CatalogConfigCollection).
		UpdateOne(ctx, bson.D{{Key: "_id", Value: constants.CatalogConfigID}}, update, options.Update().SetUpsert(true))
	return err
}

func (s *MongoStore) ActiveGrants(ctx context.Context, userID string, now time.Time) ([]models.Grant, error) {
	f := bson.D{
		{Key: "user_id", Value: userID},
		{Key: "status", Value: models.GrantActive},
		notDeleted,
		{Key: "$or", Value: bson.A{
			bson.D{{Key: "valid_until", Value: bson.D{{Key: "$exists", Value: false}}}},
			bson.D{{Key: "valid_until", Value: nil}},
			bson.D{{Key: "valid_until", Value: bson.D{{Key: "$gt", Value: now}}}},
		}},
	}
	return find[models.Grant](ctx, s.db.Collection(constants.GrantsCollection), f, nil)
}

func (s *MongoStore) GrantsByExternal(ctx context.Context, source, externalID string) ([]models.Grant, error) {
	f := bson.D{{Key: "source", Value: source}, {Key: "external_id", Value: externalID}}
	return find[models.Grant](ctx, s.db.Collection(constants.GrantsCollection), f, nil)
}

func (s *MongoStore) GrantsByRef(ctx context.Context, ref string) ([]models.Grant, error) {
	f := bson.D{{Key: "$or", Value: bson.A{
		bson.D{{Key: "external_id", Value: ref}},
		bson.D{{Key: "refs", Value: ref}},
	}}}
	return find[models.Grant](ctx, s.db.Collection(constants.GrantsCollection), f, nil)
}

func (s *MongoStore) InsertGrant(ctx context.Context, g *models.Grant, actor string) error {
	mcore.StampCreate(&g.Auditable, actor, time.Now().UTC())
	return s.insert(ctx, constants.GrantsCollection, g)
}

func (s *MongoStore) UpdateGrant(ctx context.Context, g *models.Grant, actor string) error {
	mcore.StampUpdate(&g.Auditable, actor, time.Now().UTC())
	res, err := s.db.Collection(constants.GrantsCollection).ReplaceOne(ctx, bson.D{{Key: "_id", Value: g.ID}}, g)
	if err == nil && res.MatchedCount == 0 {
		return ErrNotFound
	}
	return err
}

func (s *MongoStore) StripeCustomer(ctx context.Context, userID string) (string, error) {
	var c models.StripeCustomer
	err := s.db.Collection(constants.StripeCustomersCollection).FindOne(ctx, bson.D{{Key: "user_id", Value: userID}}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", nil
	}
	return c.CustomerID, err
}

func (s *MongoStore) SaveStripeCustomer(ctx context.Context, userID, customerID, actor string) error {
	now := time.Now().UTC()
	update := bson.D{
		{Key: "$set", Value: bson.D{{Key: "customer_id", Value: customerID}, {Key: "updated_at", Value: now}, {Key: "updated_by", Value: actor}}},
		{Key: "$setOnInsert", Value: bson.D{{Key: "created_at", Value: now}, {Key: "created_by", Value: actor}}},
	}
	_, err := s.db.Collection(constants.StripeCustomersCollection).
		UpdateOne(ctx, bson.D{{Key: "user_id", Value: userID}}, update, options.Update().SetUpsert(true))
	return err
}

func (s *MongoStore) EventProcessed(ctx context.Context, eventID string) (bool, error) {
	n, err := s.db.Collection(constants.StripeEventsCollection).CountDocuments(ctx, bson.D{{Key: "_id", Value: eventID}}, options.Count().SetLimit(1))
	return n > 0, err
}

func (s *MongoStore) RecordEvent(ctx context.Context, eventID, eventType string, at time.Time) error {
	err := s.insert(ctx, constants.StripeEventsCollection, bson.D{{Key: "_id", Value: eventID}, {Key: "type", Value: eventType}, {Key: "at", Value: at}})
	if errors.Is(err, ErrDuplicate) {
		return nil
	}
	return err
}

func (s *MongoStore) AppendAudit(ctx context.Context, e models.AuditEntry) error {
	_, err := s.db.Collection(constants.AuditCollection).InsertOne(ctx, e)
	return err
}

func (s *MongoStore) ListAudit(ctx context.Context, limit, skip int64) ([]models.AuditEntry, error) {
	return find[models.AuditEntry](ctx, s.db.Collection(constants.AuditCollection), bson.D{}, page("at", -1, limit, skip))
}

func (s *MongoStore) Ping(ctx context.Context) error { return s.db.Client().Ping(ctx, nil) }

func mapNoDoc(err error) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ErrNotFound
	}
	return err
}
