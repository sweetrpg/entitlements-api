package catalog

import (
	"errors"
	"testing"

	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

// seedMembers registers a capped members limit (free 4, maximum 20) and a flag.
func seedMembers(t *testing.T) (*Service, *data.MemoryStore) {
	t.Helper()
	s, st := newSvc(t)
	if _, err := s.CreateKey(ctx, "admin", KeyInput{Key: "a.members", Type: models.TypeLimit, Max: i64(20)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateKey(ctx, "admin", KeyInput{Key: "a.flag", Type: models.TypeFlag}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTier(ctx, "admin", TierInput{Slug: "free", Name: "Free", Values: map[string]any{"a.members": 4.0, "a.flag": false}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultTier(ctx, "admin", "free"); err != nil {
		t.Fatal(err)
	}
	return s, st
}

func grantMode(st *data.MemoryStore, user, key string, v int64, mode string) {
	st.AddGrant(models.Grant{UserID: user, Key: key, Value: v, Mode: mode, Status: models.GrantActive})
}

func TestAtLeastGrantsAreAlternativesNotSums(t *testing.T) {
	s, st := seedMembers(t)
	grantMode(st, "u1", "a.members", 10, models.ModeAtLeast)
	grantMode(st, "u1", "a.members", 20, models.ModeAtLeast)
	if v := value(t, s, "u1", "a.members"); v != 20 {
		t.Errorf("value = %d, want 20, not 30", v)
	}
}

func TestAtLeastRaisesTheFloorAndAddStacksOnTop(t *testing.T) {
	s, st := seedMembers(t)
	grantMode(st, "u1", "a.members", 10, models.ModeAtLeast)
	if v := value(t, s, "u1", "a.members"); v != 10 {
		t.Fatalf("at_least over the free 4 = %d, want 10", v)
	}
	grantMode(st, "u1", "a.members", 3, models.ModeAdd)
	grantMode(st, "u1", "a.members", 2, "")
	if v := value(t, s, "u1", "a.members"); v != 15 {
		t.Errorf("with 5 added = %d, want 15", v)
	}
}

func TestAtLeastBelowTheTierValueChangesNothing(t *testing.T) {
	s, st := seedMembers(t)
	grantMode(st, "u1", "a.members", 2, models.ModeAtLeast)
	if v := value(t, s, "u1", "a.members"); v != 4 {
		t.Errorf("value = %d, want the free 4", v)
	}
}

func TestAtLeastPlusAddIsStillClamped(t *testing.T) {
	s, st := seedMembers(t)
	grantMode(st, "u1", "a.members", 20, models.ModeAtLeast)
	grantMode(st, "u1", "a.members", 9, models.ModeAdd)
	if v := value(t, s, "u1", "a.members"); v != 20 {
		t.Errorf("value = %d, want the maximum 20", v)
	}
}

func TestFlagGrantsStayAny(t *testing.T) {
	s, st := seedMembers(t)
	grantMode(st, "u1", "a.flag", 0, models.ModeAtLeast)
	if v := value(t, s, "u1", "a.flag"); v != 1 {
		t.Errorf("flag = %d, want on", v)
	}
}

func TestProductEntitlementModesAreParsed(t *testing.T) {
	s, _ := seedMembers(t)
	in := ProductInput{Slug: "m10", Name: "Members 10", Billing: models.BillingRecurring,
		Entitlements: map[string]any{"a.members": map[string]any{"value": 10.0, "mode": "at_least"}}}
	p, err := s.CreateProduct(ctx, "admin", in)
	if err != nil {
		t.Fatal(err)
	}
	if e := p.Entitlements[0]; !e.IsAtLeast() || e.Value != 10 {
		t.Errorf("entitlement = %+v", e)
	}
	in.Slug = "m4"
	in.Entitlements = map[string]any{"a.members": map[string]any{"value": 4.0}}
	if p, err = s.CreateProduct(ctx, "admin", in); err != nil || p.Entitlements[0].Mode != models.ModeAdd {
		t.Errorf("object without mode: %+v err = %v", p, err)
	}
}

func TestProductEntitlementModeValidation(t *testing.T) {
	s, _ := seedMembers(t)
	cases := map[string]map[string]any{
		"unknown mode":    {"a.members": map[string]any{"value": 5.0, "mode": "sometimes"}},
		"at_least a flag": {"a.flag": map[string]any{"value": true, "mode": "at_least"}},
		"object no value": {"a.members": map[string]any{"mode": "at_least"}},
	}
	for name, ents := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := s.CreateProduct(ctx, "admin", ProductInput{Slug: "x", Name: "X", Billing: models.BillingRecurring, Entitlements: ents})
			if !errors.Is(err, models.ErrInvalid) {
				t.Errorf("err = %v, want invalid", err)
			}
		})
	}
}

func ent(v int64, mode string) []models.ProductEntitlement {
	return []models.ProductEntitlement{{Key: "a.members", Type: models.TypeLimit, Value: v, Mode: mode}}
}

func TestCheckRaiseRejectsWhatChangesNothing(t *testing.T) {
	s, st := seedMembers(t)
	if err := s.CheckRaise(ctx, "u1", ent(10, models.ModeAtLeast)); err != nil {
		t.Errorf("first purchase refused: %v", err)
	}
	grantMode(st, "u1", "a.members", 10, models.ModeAtLeast)
	var no NoIncreaseError
	if err := s.CheckRaise(ctx, "u1", ent(10, models.ModeAtLeast)); !errors.As(err, &no) {
		t.Errorf("same floor: err = %v, want no increase", err)
	}
	if err := s.CheckRaise(ctx, "u1", ent(4, models.ModeAtLeast)); !errors.As(err, &no) {
		t.Errorf("lower floor: err = %v, want no increase", err)
	}
	if err := s.CheckRaise(ctx, "u1", ent(20, models.ModeAtLeast)); err != nil {
		t.Errorf("higher floor refused: %v", err)
	}
}

func TestCheckRaiseJudgesAgainstTheClampedValue(t *testing.T) {
	s, st := seedMembers(t)
	grantMode(st, "u1", "a.members", 20, models.ModeAtLeast)
	var no NoIncreaseError
	if err := s.CheckRaise(ctx, "u1", ent(30, models.ModeAtLeast)); !errors.As(err, &no) {
		t.Errorf("err = %v, want no increase past the maximum", err)
	}
}

func TestCheckRaiseBundlePassesWhenAnyEntitlementRaises(t *testing.T) {
	s, st := seedMembers(t)
	if _, err := s.CreateKey(ctx, "admin", KeyInput{Key: "a.seats", Type: models.TypeLimit}); err != nil {
		t.Fatal(err)
	}
	grantMode(st, "u1", "a.members", 20, models.ModeAtLeast)
	bundle := []models.ProductEntitlement{
		{Key: "a.members", Type: models.TypeLimit, Value: 10, Mode: models.ModeAtLeast},
		{Key: "a.seats", Type: models.TypeLimit, Value: 5, Mode: models.ModeAtLeast},
	}
	if err := s.CheckRaise(ctx, "u1", bundle); err != nil {
		t.Errorf("bundle with one raising entitlement refused: %v", err)
	}
}

func TestCheckRaiseIgnoresAddOnlyAndFlagProducts(t *testing.T) {
	s, _ := seedMembers(t)
	for _, ents := range [][]models.ProductEntitlement{
		ent(5, models.ModeAdd),
		{{Key: "a.flag", Type: models.TypeFlag, Value: 1}},
	} {
		if err := s.CheckRaise(ctx, "u1", ents); err != nil {
			t.Errorf("err = %v", err)
		}
	}
}
