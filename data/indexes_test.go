package data

import (
	"fmt"
	"slices"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
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
