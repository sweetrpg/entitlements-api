package catalog

import (
	"context"
	"time"

	"github.com/sweetrpg/entitlements-api/constants"
	"github.com/sweetrpg/entitlements-api/models"
)

// TierContribution is the value a tier grant gives for one key.
type TierContribution struct {
	TierID string
	Value  int64
	Source string
}

// Explanation shows why a user has the effective value for one key: every source that feeds
// it, the administrator override if any, and the paid value with the payments behind it.
type Explanation struct {
	Key models.Key
	// Default is the default tier's value for the key; nil when the tier does not set it.
	Default    *int64
	TierGrants []TierContribution
	// KeyGrants are the add and at_least grants for the key, from any source.
	KeyGrants []models.Grant
	// Override is the active administrator override, nil when there is none.
	Override *models.Grant
	// Paid is the value from payment-sourced grants alone; Payments are those grants.
	Paid      int64
	Payments  []models.Grant
	Effective int64
}

// isPayment reports whether a grant came from a Stripe payment.
func isPayment(g models.Grant) bool {
	return g.Source == models.SourceStripeSubscription || g.Source == models.SourceStripePurchase
}

// userState is everything resolution needs for one user at one instant.
type userState struct {
	grants []models.Grant
	tiers  loadedTiers
	now    time.Time
}

func (s *Service) loadUser(ctx context.Context, userID string) (userState, error) {
	now := s.now().UTC()
	grants, err := s.store.ActiveGrants(ctx, userID, now)
	if err != nil {
		return userState{}, err
	}
	tiers, err := s.loadTiers(ctx, grants)
	if err != nil {
		return userState{}, err
	}
	return userState{grants: grants, tiers: tiers, now: now}, nil
}

// paid returns the value from payment-sourced grants alone (and the tiers they confer, never the
// default tier or an override) for one key, with the grants that feed it.
func (u userState) paid(k models.Key) (int64, []models.Grant) {
	var grants []models.Grant
	for _, g := range u.grants {
		if !isPayment(g) || !g.ActiveAt(u.now) {
			continue
		}
		if g.Key == k.Key || (g.TierID != "" && tierValue(u.tiers.granted[g.TierID], k.Key) != nil) {
			grants = append(grants, g)
		}
	}
	types := map[string]models.KeyType{k.Key: k.Type}
	return Resolve(nil, u.tiers.granted, grants, types, u.now)[k.Key], grants
}

func tierValue(t models.Tier, key string) *int64 {
	for _, tv := range t.Values {
		if tv.Key == key {
			v := tv.Value
			return &v
		}
	}
	return nil
}

func (u userState) explain(k models.Key) Explanation {
	e := Explanation{Key: k}
	if u.tiers.def != nil {
		e.Default = tierValue(*u.tiers.def, k.Key)
	}
	for _, g := range u.grants {
		if !g.ActiveAt(u.now) {
			continue
		}
		switch {
		case g.TierID != "":
			if v := tierValue(u.tiers.granted[g.TierID], k.Key); v != nil {
				e.TierGrants = append(e.TierGrants, TierContribution{TierID: g.TierID, Value: *v, Source: g.Source})
			}
		case g.Key != k.Key:
		case g.Mode == models.ModeOverride:
			c := g
			e.Override = &c
		default:
			e.KeyGrants = append(e.KeyGrants, g)
		}
	}
	e.Paid, e.Payments = u.paid(k)
	types := map[string]models.KeyType{k.Key: k.Type}
	e.Effective = clamp(k, Resolve(u.tiers.def, u.tiers.granted, u.grants, types, u.now)[k.Key])
	return e
}

// ExplainUser explains the user's value for every live key starting with prefix (up to
// MaxBatchKeys), so an administrator can see the cause of any value.
func (s *Service) ExplainUser(ctx context.Context, userID, prefix string) ([]Explanation, error) {
	if err := validateUser(userID); err != nil {
		return nil, err
	}
	keys, err := s.store.ListKeys(ctx, prefix, constants.MaxBatchKeys, 0)
	if err != nil {
		return nil, err
	}
	u, err := s.loadUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]Explanation, 0, len(keys))
	for _, k := range keys {
		out = append(out, u.explain(k))
	}
	return out, nil
}
