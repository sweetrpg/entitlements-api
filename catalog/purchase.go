package catalog

import (
	"context"
	"fmt"

	"github.com/sweetrpg/entitlements-api/models"
)

// Units is a quantity of one limit key a purchase would add.
type Units struct {
	Key   string
	Units int64
}

// MaxExceededError means a purchase would take a key past its maximum. Remaining is how many
// units the user can still add.
type MaxExceededError struct {
	Key       string
	Remaining int64
}

func (e MaxExceededError) Error() string {
	return fmt.Sprintf("%s: purchase exceeds the maximum, %d units remaining", e.Key, e.Remaining)
}

// CheckPurchase refuses a purchase that would pass a limit key's maximum. The user's current
// holding is the unclamped effective value (default tier, tier grants, and purchased units), so a
// value already at or past the maximum leaves nothing to buy. Flags and keys without a maximum
// always pass.
func (s *Service) CheckPurchase(ctx context.Context, userID string, adds []Units) error {
	names := make([]string, len(adds))
	for i, a := range adds {
		names[i] = a.Key
	}
	names = dedupe(names)
	keys, err := s.store.GetKeys(ctx, names)
	if err != nil {
		return err
	}
	byName, values, err := s.rawValues(ctx, userID, names, keys)
	if err != nil {
		return err
	}
	total := map[string]int64{}
	for _, a := range adds {
		total[a.Key] += a.Units
	}
	for _, n := range names {
		k := byName[n]
		if k.Type != models.TypeLimit || k.Max == nil {
			continue
		}
		if values[n]+total[n] > *k.Max {
			return MaxExceededError{Key: n, Remaining: max(0, *k.Max-values[n])}
		}
	}
	return nil
}
