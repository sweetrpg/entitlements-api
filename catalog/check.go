package catalog

import (
	"context"
	"fmt"
	"slices"

	"github.com/sweetrpg/entitlements-api/constants"
	"github.com/sweetrpg/entitlements-api/models"
)

// Result is one key's effective value for a user. Usage is never included; counting against a
// limit is the consumer's job.
type Result struct {
	Key        string
	Type       models.KeyType
	Value      int64
	TTLSeconds int
}

// UnknownKeysError lists requested keys that are not in the catalog.
type UnknownKeysError struct{ Keys []string }

func (e UnknownKeysError) Error() string { return fmt.Sprintf("unknown keys: %v", e.Keys) }

// Check returns the effective value of each named key for userID, in the order given. Any key
// missing from the catalog fails the whole call with UnknownKeysError rather than defaulting.
func (s *Service) Check(ctx context.Context, userID string, names []string) ([]Result, error) {
	names = dedupe(names)
	keys, err := s.store.GetKeys(ctx, names)
	if err != nil {
		return nil, err
	}
	return s.resolveKeys(ctx, userID, names, keys)
}

// CheckPrefix returns every live key under prefix (up to MaxBatchKeys) with its effective value.
func (s *Service) CheckPrefix(ctx context.Context, userID, prefix string) ([]Result, error) {
	keys, err := s.store.ListKeys(ctx, prefix, constants.MaxBatchKeys, 0)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(keys))
	for i, k := range keys {
		names[i] = k.Key
	}
	return s.resolveKeys(ctx, userID, names, keys)
}

// resolveKeys returns clamped effective values: a limit key with a maximum never reports more.
func (s *Service) resolveKeys(ctx context.Context, userID string, names []string, keys []models.Key) ([]Result, error) {
	byName, values, err := s.rawValues(ctx, userID, names, keys)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, len(names))
	for _, n := range names {
		k := byName[n]
		out = append(out, Result{Key: n, Type: k.Type, Value: clamp(k, values[n]), TTLSeconds: k.TTLSeconds})
	}
	return out, nil
}

func clamp(k models.Key, v int64) int64 {
	if k.Max != nil && v > *k.Max {
		return *k.Max
	}
	return v
}

// rawValues resolves the unclamped effective value of each key. Any name missing from keys fails
// the call with UnknownKeysError.
func (s *Service) rawValues(ctx context.Context, userID string, names []string, keys []models.Key) (map[string]models.Key, map[string]int64, error) {
	return s.rawValuesWith(ctx, userID, names, keys, nil)
}

// rawValuesWith is rawValues with extra hypothetical grants counted, to project a purchase.
func (s *Service) rawValuesWith(ctx context.Context, userID string, names []string, keys []models.Key, extra []models.Grant) (map[string]models.Key, map[string]int64, error) {
	byName := make(map[string]models.Key, len(keys))
	for _, k := range keys {
		byName[k.Key] = k
	}
	var unknown []string
	for _, n := range names {
		if _, ok := byName[n]; !ok {
			unknown = append(unknown, n)
		}
	}
	if len(unknown) > 0 {
		return nil, nil, UnknownKeysError{Keys: unknown}
	}

	now := s.now().UTC()
	grants, err := s.store.ActiveGrants(ctx, userID, now)
	if err != nil {
		return nil, nil, err
	}
	grants = append(grants, extra...)
	tiers, err := s.loadTiers(ctx, grants)
	if err != nil {
		return nil, nil, err
	}

	types := make(map[string]models.KeyType, len(byName))
	for n, k := range byName {
		types[n] = k.Type
	}
	return byName, Resolve(tiers.def, tiers.granted, grants, types, now), nil
}

type loadedTiers struct {
	def     *models.Tier
	granted map[string]models.Tier
}

// loadTiers fetches the default tier and every tier the grants reference in one query.
func (s *Service) loadTiers(ctx context.Context, grants []models.Grant) (loadedTiers, error) {
	defID, err := s.store.DefaultTierID(ctx)
	if err != nil {
		return loadedTiers{}, err
	}
	ids := []string{}
	if defID != "" {
		ids = append(ids, defID)
	}
	for _, g := range grants {
		if g.TierID != "" && !slices.Contains(ids, g.TierID) {
			ids = append(ids, g.TierID)
		}
	}
	found, err := s.store.GetTiers(ctx, ids)
	if err != nil {
		return loadedTiers{}, err
	}
	lt := loadedTiers{granted: map[string]models.Tier{}}
	for _, t := range found {
		lt.granted[t.Slug] = t
		if t.Slug == defID {
			c := t
			lt.def = &c
		}
	}
	return lt, nil
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
