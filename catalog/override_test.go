package catalog

import (
	"errors"
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

func setOverride(t *testing.T, s *Service, key string, v any, reason string) {
	t.Helper()
	if _, err := s.SetOverride(ctx, "admin1", "u1", key, OverrideInput{Value: v, Reason: reason}); err != nil {
		t.Fatal(err)
	}
}

func TestSetOverrideReplacesThePreviousOne(t *testing.T) {
	s, st := seedMembers(t)
	setOverride(t, s, "a.members", 12.0, "first")
	setOverride(t, s, "a.members", 7.0, "second")
	if v := value(t, s, "u1", "a.members"); v != 7 {
		t.Errorf("value = %d, want 7", v)
	}
	n := 0
	for _, g := range mustGrants(t, st) {
		if g.Mode == models.ModeOverride && g.Status == models.GrantActive {
			n++
		}
	}
	if n != 1 {
		t.Errorf("active overrides = %d, want 1", n)
	}
}

func mustGrants(t *testing.T, st *data.MemoryStore) []models.Grant {
	t.Helper()
	g, err := st.ActiveGrants(ctx, "u1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestRemoveOverrideRestoresComputedValueAndKeepsOtherGrants(t *testing.T) {
	s, st := seedMembers(t)
	grantMode(st, "u1", "a.members", 6, models.ModeAdd)
	setOverride(t, s, "a.members", 1.0, "abuse")
	if err := s.RemoveOverride(ctx, "admin1", "u1", "a.members", "resolved"); err != nil {
		t.Fatal(err)
	}
	if v := value(t, s, "u1", "a.members"); v != 10 {
		t.Errorf("value = %d, want 4 + 6 added", v)
	}
	setOverride(t, s, "a.members", 3.0, "again")
	if v := value(t, s, "u1", "a.members"); v != 3 {
		t.Errorf("re-set value = %d, want 3", v)
	}
}

func TestRemoveOverrideWithoutOneIsNotFound(t *testing.T) {
	s, _ := seedMembers(t)
	if err := s.RemoveOverride(ctx, "admin1", "u1", "a.members", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestOverrideWritesAreValidated(t *testing.T) {
	s, _ := seedMembers(t)
	past := time.Now().Add(-time.Minute)
	cases := []struct {
		name string
		key  string
		in   OverrideInput
		want error
	}{
		{"missing reason", "a.members", OverrideInput{Value: 3.0}, models.ErrInvalid},
		{"blank reason", "a.members", OverrideInput{Value: 3.0, Reason: "  "}, models.ErrInvalid},
		{"past end date", "a.members", OverrideInput{Value: 3.0, Reason: "x", ValidUntil: &past}, models.ErrInvalid},
		{"wrong value type", "a.members", OverrideInput{Value: true, Reason: "x"}, models.ErrInvalid},
		{"negative limit", "a.members", OverrideInput{Value: -1.0, Reason: "x"}, models.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := s.SetOverride(ctx, "admin1", "u1", c.key, c.in); !errors.Is(err, c.want) {
				t.Errorf("err = %v, want %v", err, c.want)
			}
		})
	}
	if _, err := s.SetOverride(ctx, "admin1", "u1", "no.such", OverrideInput{Value: 1.0, Reason: "x"}); !errors.As(err, new(UnknownKeysError)) {
		t.Errorf("unknown key err = %v", err)
	}
}

func TestOverrideAuditRecordsActorValuesAndReason(t *testing.T) {
	s, st := seedMembers(t)
	setOverride(t, s, "a.members", 12.0, "support ticket 42")
	entries, err := st.ListAudit(ctx, 1, 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit = %v, %v", entries, err)
	}
	e := entries[0]
	if e.Actor != "admin1" || e.Kind != "override" || e.Subject != "u1/a.members" || e.Reason != "support ticket 42" {
		t.Errorf("entry = %+v", e)
	}
	if e.Before != int64(4) || e.After != int64(12) {
		t.Errorf("before/after = %v/%v, want 4/12", e.Before, e.After)
	}
}
