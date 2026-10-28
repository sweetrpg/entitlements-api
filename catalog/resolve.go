package catalog

import (
	"time"

	"github.com/sweetrpg/entitlements-api/models"
)

// Resolve returns the maximum value per key across the default tier, the tiers of active tier
// grants, and active key-level grants. A key with no source is absent from the map (read as
// zero or false). Grants are re-checked for activity at now so the rule holds for any Store.
func Resolve(def *models.Tier, tiers map[string]models.Tier, grants []models.Grant, now time.Time) map[string]int64 {
	out := map[string]int64{}
	raise := func(key string, v int64) {
		if cur, ok := out[key]; !ok || v > cur {
			out[key] = v
		}
	}
	if def != nil {
		for _, tv := range def.Values {
			raise(tv.Key, tv.Value)
		}
	}
	for _, g := range grants {
		if !g.ActiveAt(now) {
			continue
		}
		if g.TierID != "" {
			for _, tv := range tiers[g.TierID].Values {
				raise(tv.Key, tv.Value)
			}
		}
		if g.Key != "" {
			raise(g.Key, g.Value)
		}
	}
	return out
}
