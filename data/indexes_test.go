package data

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// MongoDB partial indexes accept only equality, $exists: true, $gt, $gte, $lt, $lte, $type, and
// $and. Anything else (notably $exists: false, $not, $ne, $or, $in) fails at index creation, and
// a failed index means uniqueness is silently not enforced.
var allowedPartialOperators = []string{"$exists", "$gt", "$gte", "$lt", "$lte", "$type", "$and", "$eq"}

func checkPartialFilter(v any) error {
	switch f := v.(type) {
	case bson.D:
		for _, e := range f {
			if len(e.Key) > 0 && e.Key[0] == '$' {
				if !slices.Contains(allowedPartialOperators, e.Key) {
					return fmt.Errorf("operator %s is not allowed in a partial filter", e.Key)
				}
				if e.Key == "$exists" && e.Value != true {
					return fmt.Errorf("$exists: %v is not allowed, only $exists: true", e.Value)
				}
			}
			if err := checkPartialFilter(e.Value); err != nil {
				return err
			}
		}
	case bson.A:
		for _, item := range f {
			if err := checkPartialFilter(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func TestEveryPartialFilterUsesOnlyOperatorsMongoAllows(t *testing.T) {
	checked := 0
	for coll, models := range indexSpecs() {
		for _, m := range models {
			if m.Options == nil || m.Options.PartialFilterExpression == nil {
				continue
			}
			checked++
			if err := checkPartialFilter(m.Options.PartialFilterExpression); err != nil {
				t.Errorf("%s %v: %v", coll, m.Keys, err)
			}
		}
	}
	if checked < 4 {
		t.Errorf("only %d partial filters found; the check is not reaching the index definitions", checked)
	}
}

func TestPartialFilterCheckerRejectsWhatMongoRejects(t *testing.T) {
	bad := []bson.D{
		{{Key: "deleted_at", Value: bson.D{{Key: "$exists", Value: false}}}},
		{{Key: "deleted_at", Value: bson.D{{Key: "$not", Value: bson.D{{Key: "$exists", Value: true}}}}}},
		{{Key: "live", Value: bson.D{{Key: "$ne", Value: false}}}},
		{{Key: "$or", Value: bson.A{bson.D{{Key: "a", Value: 1}}}}},
	}
	for _, f := range bad {
		if err := checkPartialFilter(f); err == nil {
			t.Errorf("accepted %v", f)
		}
	}
	good := []bson.D{
		{{Key: "live", Value: true}},
		{{Key: "external_id", Value: bson.D{{Key: "$exists", Value: true}}}},
		{{Key: "$and", Value: bson.A{bson.D{{Key: "n", Value: bson.D{{Key: "$gt", Value: 1}}}}}}},
	}
	for _, f := range good {
		if err := checkPartialFilter(f); err != nil {
			t.Errorf("rejected %v: %v", f, err)
		}
	}
}

func TestEveryUniqueIndexOnLiveDocumentsIsCoveredByAFilter(t *testing.T) {
	for _, coll := range []string{"entitlement_keys", "entitlement_tiers", "entitlement_products"} {
		found := false
		for _, m := range indexSpecs()[coll] {
			if m.Options != nil && m.Options.Unique != nil && *m.Options.Unique && m.Options.PartialFilterExpression != nil {
				found = true
			}
		}
		if !found {
			t.Errorf("%s has no unique live-only index", coll)
		}
	}
}

// fakeIndexer is a collection's index set. create fails with err when set, the way MongoDB does
// when another replica has already built an index with the same keys under a different name.
type fakeIndexer struct {
	existing []bson.D
	err      error
	created  int
	listErr  error
}

func (f *fakeIndexer) create(context.Context, mongo.IndexModel) error {
	if f.err != nil {
		return f.err
	}
	f.created++
	return nil
}

func (f *fakeIndexer) list(context.Context) ([]bson.Raw, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]bson.Raw, len(f.existing))
	for i, d := range f.existing {
		b, _ := bson.Marshal(d)
		out[i] = b
	}
	return out, nil
}

func conflict(code int32) error {
	return mongo.CommandError{Code: code, Name: "IndexOptionsConflict", Message: `Index already exists with a different name: key_1`}
}

var liveUnique = mongo.IndexModel{
	Keys:    bson.D{{Key: "slug", Value: 1}},
	Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "live", Value: true}}),
}

func existingLive(name string, keyValue any) bson.D {
	return bson.D{
		{Key: "v", Value: 2}, {Key: "name", Value: name},
		{Key: "key", Value: bson.D{{Key: "slug", Value: keyValue}}},
		{Key: "unique", Value: true},
		{Key: "partialFilterExpression", Value: bson.D{{Key: "live", Value: true}}},
	}
}

func TestEnsureIndexCreatesWhenMissing(t *testing.T) {
	f := &fakeIndexer{}
	if err := ensureIndex(context.Background(), f, liveUnique); err != nil || f.created != 1 {
		t.Errorf("err = %v created = %d", err, f.created)
	}
}

func TestEnsureIndexToleratesAnEquivalentIndexUnderAnotherName(t *testing.T) {
	// Another replica (or an earlier release) already built it, under a different name and with
	// the key direction stored as a different number type.
	for _, code := range []int32{85, 86} {
		for name, dir := range map[string]any{"int32": int32(1), "int64": int64(1), "double": float64(1)} {
			f := &fakeIndexer{err: conflict(code), existing: []bson.D{
				{{Key: "name", Value: "_id_"}, {Key: "key", Value: bson.D{{Key: "_id", Value: 1}}}},
				existingLive("key_1", dir),
			}}
			if err := ensureIndex(context.Background(), f, liveUnique); err != nil {
				t.Errorf("code %d, %s: err = %v, want nil for an equivalent index", code, name, err)
			}
		}
	}
}

func TestEnsureIndexStillFailsWhenTheExistingIndexDiffers(t *testing.T) {
	cases := map[string]bson.D{
		"not unique": {
			{Key: "name", Value: "slug_1"}, {Key: "key", Value: bson.D{{Key: "slug", Value: 1}}},
			{Key: "partialFilterExpression", Value: bson.D{{Key: "live", Value: true}}},
		},
		"no filter": {
			{Key: "name", Value: "slug_1"}, {Key: "key", Value: bson.D{{Key: "slug", Value: 1}}}, {Key: "unique", Value: true},
		},
		"other filter": {
			{Key: "name", Value: "slug_1"}, {Key: "key", Value: bson.D{{Key: "slug", Value: 1}}}, {Key: "unique", Value: true},
			{Key: "partialFilterExpression", Value: bson.D{{Key: "deleted_at", Value: bson.D{{Key: "$exists", Value: true}}}}},
		},
		"other direction": existingLive("slug_-1", -1),
		"other keys": {
			{Key: "name", Value: "other"}, {Key: "key", Value: bson.D{{Key: "key", Value: 1}}}, {Key: "unique", Value: true},
			{Key: "partialFilterExpression", Value: bson.D{{Key: "live", Value: true}}},
		},
	}
	for name, existing := range cases {
		t.Run(name, func(t *testing.T) {
			f := &fakeIndexer{err: conflict(85), existing: []bson.D{existing}}
			if err := ensureIndex(context.Background(), f, liveUnique); err == nil {
				t.Error("a conflicting, different index was accepted")
			}
		})
	}
}

func TestEnsureIndexReturnsOtherErrorsAndListFailures(t *testing.T) {
	boom := errors.New("connection reset")
	if err := ensureIndex(context.Background(), &fakeIndexer{err: boom}, liveUnique); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if err := ensureIndex(context.Background(), &fakeIndexer{err: conflict(85), listErr: boom}, liveUnique); err == nil {
		t.Error("a conflict that cannot be checked was accepted")
	}
	other := mongo.CommandError{Code: 13, Name: "Unauthorized", Message: "not authorized"}
	if err := ensureIndex(context.Background(), &fakeIndexer{err: other, existing: []bson.D{existingLive("key_1", 1)}}, liveUnique); err == nil {
		t.Error("an unrelated command error was treated as a conflict")
	}
}

func TestEnsureIndexMatchesPlainAndNonUniqueIndexes(t *testing.T) {
	plain := mongo.IndexModel{Keys: bson.D{{Key: "user_id", Value: 1}, {Key: "status", Value: 1}}}
	same := bson.D{{Key: "name", Value: "user_id_1_status_1"}, {Key: "key", Value: bson.D{{Key: "user_id", Value: 1}, {Key: "status", Value: 1}}}}
	if err := ensureIndex(context.Background(), &fakeIndexer{err: conflict(85), existing: []bson.D{same}}, plain); err != nil {
		t.Errorf("err = %v", err)
	}
	reordered := bson.D{{Key: "name", Value: "x"}, {Key: "key", Value: bson.D{{Key: "status", Value: 1}, {Key: "user_id", Value: 1}}}}
	if err := ensureIndex(context.Background(), &fakeIndexer{err: conflict(85), existing: []bson.D{reordered}}, plain); err == nil {
		t.Error("a compound index with the keys in another order is not equivalent")
	}
}

// The driver writes a generated index name into the *IndexOptions it is given. Models that share
// one options value therefore share the first model's name: the tiers and products slug indexes
// were created as "key_1", the name generated for the keys index, and replicas that processed the
// collections in another order disagreed about the names and conflicted.
func TestNoTwoIndexModelsShareAnOptionsValue(t *testing.T) {
	seen := map[*options.IndexOptions]string{}
	for coll, models := range indexSpecs() {
		for _, m := range models {
			if m.Options == nil {
				continue
			}
			where := fmt.Sprintf("%s %v", coll, m.Keys)
			if other, dup := seen[m.Options]; dup {
				t.Errorf("%s shares its options value with %s", where, other)
			}
			seen[m.Options] = where
		}
	}
}

func TestIndexSpecsAreBuiltFreshOnEveryCall(t *testing.T) {
	first, second := indexSpecs(), indexSpecs()
	for coll, ms := range first {
		for i, m := range ms {
			if m.Options != nil && m.Options == second[coll][i].Options {
				t.Errorf("%s %v: the options value is reused between calls, so a name set by the driver sticks", coll, m.Keys)
			}
		}
	}
}
