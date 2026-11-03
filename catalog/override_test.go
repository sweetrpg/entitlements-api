package catalog

import (
	"testing"
	"time"

	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

func override(st *data.MemoryStore, user, key string, v int64, until *time.Time, status string) {
	st.AddGrant(models.Grant{
		UserID: user, Key: key, Value: v, Mode: models.ModeOverride, Source: models.SourceAdmin,
		Status: status, ValidUntil: until, Reason: "support request",
	})
}

func TestOverrideReplacesComputedValue(t *testing.T) {
	cases := []struct {
		name  string
		setup func(st *data.MemoryStore)
		want  int64
	}{
		{"raises above tier", func(st *data.MemoryStore) { override(st, "u1", "a.members", 12, nil, models.GrantActive) }, 12},
		{"lowers below tier", func(st *data.MemoryStore) { override(st, "u1", "a.members", 1, nil, models.GrantActive) }, 1},
		{"lowers below purchased units", func(st *data.MemoryStore) {
			grantMode(st, "u1", "a.members", 6, models.ModeAdd)
			override(st, "u1", "a.members", 2, nil, models.GrantActive)
		}, 2},
		{"clamps to the key maximum", func(st *data.MemoryStore) { override(st, "u1", "a.members", 99, nil, models.GrantActive) }, 20},
		{"ended override restores the computed value", func(st *data.MemoryStore) { override(st, "u1", "a.members", 12, nil, models.GrantEnded) }, 4},
		{"expired override restores the computed value", func(st *data.MemoryStore) {
			past := time.Now().Add(-time.Hour)
			override(st, "u1", "a.members", 12, &past, models.GrantActive)
		}, 4},
		{"other user is unaffected", func(st *data.MemoryStore) { override(st, "u2", "a.members", 12, nil, models.GrantActive) }, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, st := seedMembers(t)
			c.setup(st)
			if v := value(t, s, "u1", "a.members"); v != c.want {
				t.Errorf("value = %d, want %d", v, c.want)
			}
		})
	}
}

func TestOverrideTurnsAFlagOffAndOn(t *testing.T) {
	s, st := seedMembers(t)
	override(st, "u1", "a.flag", 1, nil, models.GrantActive)
	if v := value(t, s, "u1", "a.flag"); v != 1 {
		t.Errorf("flag on = %d, want 1", v)
	}
	grantMode(st, "u2", "a.flag", 1, models.ModeAdd)
	override(st, "u2", "a.flag", 0, nil, models.GrantActive)
	if v := value(t, s, "u2", "a.flag"); v != 0 {
		t.Errorf("flag forced off = %d, want 0", v)
	}
}
