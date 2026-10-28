package catalog

import (
	"time"

	"github.com/sweetrpg/entitlements-api/models"
)

// Resolve returns the effective value per key:
//
//	effective = max(default tier, tier grants, at_least grants) + sum(add grants)
//
// for a limit key; a flag key is on when its tier-derived value is on or any key-level grant is
// active. Tiers and at_least grants are alternatives (maximum); purchased add units stack. types says which keys are
// flags, any key not listed is treated as a limit. A key with no source is absent from the map
// (read as zero or false). Grants are re-checked for activity at now so the rule holds for any
// Store.
func Resolve(def *models.Tier, tiers map[string]models.Tier, grants []models.Grant, types map[string]models.KeyType, now time.Time) map[string]int64 {
	tierValue := map[string]int64{}
	raise := func(key string, v int64) {
		if cur, ok := tierValue[key]; !ok || v > cur {
			tierValue[key] = v
		}
	}
	if def != nil {
		for _, tv := range def.Values {
			raise(tv.Key, tv.Value)
		}
	}
	units := map[string]int64{}
	for _, g := range grants {
		if !g.ActiveAt(now) {
			continue
		}
		if g.TierID != "" {
			for _, tv := range tiers[g.TierID].Values {
				raise(tv.Key, tv.Value)
			}
		}
		if g.Key == "" {
			continue
		}
		switch {
		case types[g.Key] == models.TypeFlag:
			units[g.Key] = 1
		case g.Mode == models.ModeAtLeast:
			raise(g.Key, g.Value)
		default:
			units[g.Key] += g.Value
		}
	}

	out := tierValue
	for key, add := range units {
		if types[key] == models.TypeFlag {
			out[key] = max(out[key], add)
		} else {
			out[key] += add
		}
	}
	return out
}
