package catalog

import (
	"errors"
	"testing"

	"github.com/sweetrpg/entitlements-api/models"
)

func i64(v int64) *int64 { return &v }

// seedCapped registers a limit key with maximum 5 and a free tier granting 1.
func seedCapped(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.CreateKey(ctx, "admin", KeyInput{Key: "initiative.sessions_per_gm", Type: models.TypeLimit, Max: i64(5)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTier(ctx, "admin", TierInput{Slug: "free", Name: "Free", Values: map[string]any{"initiative.sessions_per_gm": 1.0}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultTier(ctx, "admin", "free"); err != nil {
		t.Fatal(err)
	}
}

func units(st interface{ AddGrant(models.Grant) }, user string, n int, each int64) {
	for i := 0; i < n; i++ {
		st.AddGrant(models.Grant{UserID: user, Key: "initiative.sessions_per_gm", Value: each, Status: models.GrantActive})
	}
}

func TestEffectiveValueIsClampedToTheMaximum(t *testing.T) {
	s, st := newSvc(t)
	seedCapped(t, s)
	units(st, "u1", 1, 7) // 1 + 7 = 8, maximum 5
	if v := value(t, s, "u1", "initiative.sessions_per_gm"); v != 5 {
		t.Errorf("value = %d, want 5", v)
	}
}

func TestValueBelowTheMaximumIsUntouched(t *testing.T) {
	s, st := newSvc(t)
	seedCapped(t, s)
	units(st, "u1", 2, 1)
	if v := value(t, s, "u1", "initiative.sessions_per_gm"); v != 3 {
		t.Errorf("value = %d, want 3", v)
	}
}

func TestRaisingTheMaximumTakesEffectWithoutDeploy(t *testing.T) {
	s, st := newSvc(t)
	seedCapped(t, s)
	units(st, "u1", 1, 7)
	if _, err := s.UpdateKey(ctx, "admin", "initiative.sessions_per_gm", "", 0, i64(10)); err != nil {
		t.Fatal(err)
	}
	if v := value(t, s, "u1", "initiative.sessions_per_gm"); v != 8 {
		t.Errorf("value = %d, want 8", v)
	}
	if _, err := s.UpdateKey(ctx, "admin", "initiative.sessions_per_gm", "", 0, nil); err != nil {
		t.Fatal(err)
	}
	if v := value(t, s, "u1", "initiative.sessions_per_gm"); v != 8 {
		t.Errorf("value without maximum = %d, want 8", v)
	}
}

func TestMaximumValidation(t *testing.T) {
	s, _ := newSvc(t)
	cases := map[string]KeyInput{
		"on a flag":  {Key: "a.flag", Type: models.TypeFlag, Max: i64(1)},
		"zero":       {Key: "a.zero", Type: models.TypeLimit, Max: i64(0)},
		"negative":   {Key: "a.neg", Type: models.TypeLimit, Max: i64(-3)},
		"over int32": {Key: "a.big", Type: models.TypeLimit, Max: i64(1 << 40)},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.CreateKey(ctx, "admin", in); !errors.Is(err, models.ErrInvalid) {
				t.Errorf("err = %v, want invalid", err)
			}
		})
	}
	if _, err := s.CreateKey(ctx, "admin", KeyInput{Key: "a.flag2", Type: models.TypeFlag}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateKey(ctx, "admin", "a.flag2", "", 0, i64(2)); !errors.Is(err, models.ErrInvalid) {
		t.Errorf("update flag with maximum: err = %v, want invalid", err)
	}
}

func TestCheckPurchaseReportsUnitsRemaining(t *testing.T) {
	s, st := newSvc(t)
	seedCapped(t, s)
	units(st, "u1", 2, 1) // holds 1 + 2 = 3, two units left

	if err := s.CheckPurchase(ctx, "u1", []Units{{Key: "initiative.sessions_per_gm", Units: 2}}); err != nil {
		t.Errorf("exact fit refused: %v", err)
	}
	err := s.CheckPurchase(ctx, "u1", []Units{{Key: "initiative.sessions_per_gm", Units: 3}})
	var exceeded MaxExceededError
	if !errors.As(err, &exceeded) || exceeded.Remaining != 2 || exceeded.Key != "initiative.sessions_per_gm" {
		t.Errorf("err = %v, want exceeded with 2 remaining", err)
	}
}

func TestCheckPurchaseSumsRepeatedKeysAndHandlesAtMaximum(t *testing.T) {
	s, st := newSvc(t)
	seedCapped(t, s)
	two := []Units{{Key: "initiative.sessions_per_gm", Units: 2}, {Key: "initiative.sessions_per_gm", Units: 3}}
	var exceeded MaxExceededError
	if err := s.CheckPurchase(ctx, "u1", two); !errors.As(err, &exceeded) || exceeded.Remaining != 4 {
		t.Errorf("err = %v, want 4 remaining", err)
	}
	units(st, "u2", 1, 9) // already past the maximum
	err := s.CheckPurchase(ctx, "u2", []Units{{Key: "initiative.sessions_per_gm", Units: 1}})
	if !errors.As(err, &exceeded) || exceeded.Remaining != 0 {
		t.Errorf("err = %v, want 0 remaining", err)
	}
}

func TestCheckPurchaseIgnoresFlagsAndUncappedKeys(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	adds := []Units{{Key: "initiative.theme_slots", Units: 1000}, {Key: "initiative.deluxe", Units: 1}}
	if err := s.CheckPurchase(ctx, "u1", adds); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestCheckPurchaseUnknownKey(t *testing.T) {
	s, _ := newSvc(t)
	var unknown UnknownKeysError
	if err := s.CheckPurchase(ctx, "u1", []Units{{Key: "no.such", Units: 1}}); !errors.As(err, &unknown) {
		t.Errorf("err = %v, want UnknownKeysError", err)
	}
}
