package catalog

import (
	"context"
	"fmt"

	"github.com/sweetrpg/entitlements-api/models"
)

// PurchaseOption adjusts how a purchase is checked.
type PurchaseOption func(*purchaseOptions)

type purchaseOptions struct{ replacing string }

// Replacing judges the purchase as if the grants of one Stripe object (a subscription being
// switched to another plan) were gone.
func Replacing(externalID string) PurchaseOption {
	return func(o *purchaseOptions) { o.replacing = externalID }
}

func applyOptions(opts []PurchaseOption) purchaseOptions {
	var o purchaseOptions
	for _, f := range opts {
		f(&o)
	}
	return o
}

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
func (s *Service) CheckPurchase(ctx context.Context, userID string, adds []Units, opts ...PurchaseOption) error {
	names := make([]string, len(adds))
	for i, a := range adds {
		names[i] = a.Key
	}
	names = dedupe(names)
	keys, err := s.store.GetKeys(ctx, names)
	if err != nil {
		return err
	}
	byName, values, err := s.rawValuesWith(ctx, userID, names, keys, nil, applyOptions(opts).replacing)
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

// NoIncreaseError means a product's at_least entitlements would not raise the user's value for
// any of their keys, so buying it would change nothing.
type NoIncreaseError struct{ Keys []string }

func (e NoIncreaseError) Error() string {
	return fmt.Sprintf("purchase would not raise %v: already at or above it", e.Keys)
}

// CheckRaise refuses a purchase whose at_least entitlements would not raise the user's current
// (clamped) value. A bundle passes when any of its at_least entitlements raises one; a product
// with no at_least entitlement always passes.
func (s *Service) CheckRaise(ctx context.Context, userID string, ents []models.ProductEntitlement, opts ...PurchaseOption) error {
	replacing := applyOptions(opts).replacing
	var atLeast []models.ProductEntitlement
	var names []string
	for _, e := range ents {
		if e.IsAtLeast() && e.Type == models.TypeLimit {
			atLeast = append(atLeast, e)
			names = append(names, e.Key)
		}
	}
	if len(atLeast) == 0 {
		return nil
	}
	names = dedupe(names)
	keys, err := s.store.GetKeys(ctx, names)
	if err != nil {
		return err
	}
	byName, now, err := s.rawValuesWith(ctx, userID, names, keys, nil, replacing)
	if err != nil {
		return err
	}
	hypothetical := make([]models.Grant, len(atLeast))
	for i, e := range atLeast {
		hypothetical[i] = models.Grant{UserID: userID, Key: e.Key, Value: e.Value, Mode: models.ModeAtLeast, Status: models.GrantActive}
	}
	_, projected, err := s.rawValuesWith(ctx, userID, names, keys, hypothetical, replacing)
	if err != nil {
		return err
	}
	for _, n := range names {
		if clamp(byName[n], projected[n]) > clamp(byName[n], now[n]) {
			return nil
		}
	}
	return NoIncreaseError{Keys: names}
}
