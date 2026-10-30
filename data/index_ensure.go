package data

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

// MongoDB command error codes for creating an index that clashes with an existing one: same
// options under another name (85), same keys with different options or name (86).
const (
	codeIndexOptionsConflict  = 85
	codeIndexKeySpecsConflict = 86
)

// indexer is one collection's index set, so index creation can be tested without a database.
type indexer interface {
	create(ctx context.Context, m mongo.IndexModel) error
	list(ctx context.Context) ([]bson.Raw, error)
}

type mongoIndexer struct{ view mongo.IndexView }

func (m mongoIndexer) create(ctx context.Context, model mongo.IndexModel) error {
	_, err := m.view.CreateOne(ctx, model)
	return err
}

func (m mongoIndexer) list(ctx context.Context) ([]bson.Raw, error) {
	cur, err := m.view.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []bson.Raw
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ensureIndex creates an index unless an equivalent one exists. Several replicas start together
// and run this at the same moment, so one can find the index already built by another, possibly
// under a different name; that is the goal reached, not a failure. A clash with an index that
// differs in keys, uniqueness, or filter is still an error.
func ensureIndex(ctx context.Context, ix indexer, m mongo.IndexModel) error {
	err := ix.create(ctx, m)
	if err == nil || !isIndexConflict(err) {
		return err
	}
	existing, lerr := ix.list(ctx)
	if lerr != nil {
		return fmt.Errorf("%w (and listing indexes to compare failed: %v)", err, lerr)
	}
	for _, e := range existing {
		if equivalentIndex(e, m) {
			return nil
		}
	}
	return err
}

func isIndexConflict(err error) bool {
	var ce mongo.CommandError
	return errors.As(err, &ce) && (ce.Code == codeIndexOptionsConflict || ce.Code == codeIndexKeySpecsConflict)
}

// equivalentIndex reports whether an existing index spec (as listIndexes returns it) has the same
// keys in the same order and directions, the same uniqueness, and the same partial filter as the
// model. The name is ignored.
func equivalentIndex(existing bson.Raw, want mongo.IndexModel) bool {
	wantKeys, err := bson.Marshal(want.Keys)
	if err != nil {
		return false
	}
	have, err := existing.LookupErr("key")
	if err != nil {
		return false
	}
	haveDoc, ok := have.DocumentOK()
	if !ok || !sameKeys(haveDoc, bson.Raw(wantKeys)) {
		return false
	}
	return uniqueOf(existing) == wantUnique(want) && sameFilter(existing, want)
}

// sameKeys compares key patterns by field name and direction, ignoring the number type the
// server stored the direction as.
func sameKeys(a, b bson.Raw) bool {
	ae, err1 := a.Elements()
	be, err2 := b.Elements()
	if err1 != nil || err2 != nil || len(ae) != len(be) {
		return false
	}
	for i := range ae {
		if ae[i].Key() != be[i].Key() {
			return false
		}
		av, aok := number(ae[i].Value())
		bv, bok := number(be[i].Value())
		if !aok || !bok || av != bv {
			// Not numeric (text, 2dsphere): compare as is.
			if aok || bok || ae[i].Value().String() != be[i].Value().String() {
				return false
			}
		}
	}
	return true
}

func number(v bson.RawValue) (float64, bool) {
	switch v.Type {
	case bson.TypeInt32:
		return float64(v.Int32()), true
	case bson.TypeInt64:
		return float64(v.Int64()), true
	case bson.TypeDouble:
		return v.Double(), true
	}
	return 0, false
}

func uniqueOf(existing bson.Raw) bool {
	v, err := existing.LookupErr("unique")
	return err == nil && v.Type == bson.TypeBoolean && v.Boolean()
}

func wantUnique(m mongo.IndexModel) bool {
	return m.Options != nil && m.Options.Unique != nil && *m.Options.Unique
}

func sameFilter(existing bson.Raw, want mongo.IndexModel) bool {
	have, haveErr := existing.LookupErr("partialFilterExpression")
	if want.Options == nil || want.Options.PartialFilterExpression == nil {
		return haveErr != nil
	}
	if haveErr != nil {
		return false
	}
	wantBytes, err := bson.Marshal(want.Options.PartialFilterExpression)
	if err != nil {
		return false
	}
	doc, ok := have.DocumentOK()
	return ok && doc.String() == bson.Raw(wantBytes).String()
}
