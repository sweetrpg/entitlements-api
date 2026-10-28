package catalog

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

var ctx = context.Background()

func newSvc(t *testing.T) (*Service, *data.MemoryStore) {
	t.Helper()
	st := data.NewMemoryStore()
	return New(st), st
}

// seed registers a limit and a flag key, a free default tier, and a plus tier.
func seed(t *testing.T, s *Service) {
	t.Helper()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.CreateKey(ctx, "admin", KeyInput{Key: "initiative.theme_slots", Type: models.TypeLimit, TTLSeconds: 60})
	must(err)
	_, err = s.CreateKey(ctx, "admin", KeyInput{Key: "initiative.deluxe", Type: models.TypeFlag})
	must(err)
	_, err = s.CreateTier(ctx, "admin", TierInput{Slug: "free", Name: "Free", Values: map[string]any{"initiative.theme_slots": 5.0, "initiative.deluxe": false}})
	must(err)
	_, err = s.CreateTier(ctx, "admin", TierInput{Slug: "plus", Name: "Plus", Values: map[string]any{"initiative.theme_slots": 20.0, "initiative.deluxe": true}})
	must(err)
	must(s.SetDefaultTier(ctx, "admin", "free"))
}

func value(t *testing.T, s *Service, user, key string) int64 {
	t.Helper()
	r, err := s.Check(ctx, user, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	return r[0].Value
}

func TestCreateKeyRejectsDuplicate(t *testing.T) {
	s, _ := newSvc(t)
	in := KeyInput{Key: "a.b", Type: models.TypeFlag}
	if _, err := s.CreateKey(ctx, "admin", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateKey(ctx, "admin", in); !errors.Is(err, ErrDuplicate) {
		t.Errorf("err = %v, want duplicate", err)
	}
}

func TestCreateKeyValidation(t *testing.T) {
	s, _ := newSvc(t)
	bad := []KeyInput{
		{Key: "nodots", Type: models.TypeFlag},
		{Key: "a.b", Type: "weird"},
		{Key: "a.b", Type: models.TypeFlag, TTLSeconds: -1},
		{Key: "a.b", Type: models.TypeFlag, TTLSeconds: 999999},
	}
	for _, in := range bad {
		if _, err := s.CreateKey(ctx, "admin", in); !errors.Is(err, models.ErrInvalid) {
			t.Errorf("%+v err = %v", in, err)
		}
	}
}

func TestDeletedKeyCanBeRecreated(t *testing.T) {
	s, _ := newSvc(t)
	in := KeyInput{Key: "a.b", Type: models.TypeFlag}
	_, _ = s.CreateKey(ctx, "admin", in)
	if err := s.DeleteKey(ctx, "admin", "a.b"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateKey(ctx, "admin", in); err != nil {
		t.Errorf("recreate: %v", err)
	}
	if err := s.DeleteKey(ctx, "admin", "missing.key"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete missing = %v", err)
	}
}

func TestUpdateKey(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	k, err := s.UpdateKey(ctx, "admin2", "initiative.deluxe", "new desc", 30)
	if err != nil || k.Description != "new desc" || k.TTLSeconds != 30 || k.UpdatedBy != "admin2" || k.CreatedBy != "admin" {
		t.Errorf("k = %+v err = %v", k, err)
	}
	if _, err := s.UpdateKey(ctx, "admin", "nope.nope", "", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if _, err := s.UpdateKey(ctx, "admin", "initiative.deluxe", "", -5); !errors.Is(err, models.ErrInvalid) {
		t.Errorf("bad ttl = %v", err)
	}
}

func TestTierValidation(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	bad := []TierInput{
		{Slug: "Bad Slug", Name: "x"},
		{Slug: "x", Name: " "},
		{Slug: "x", Name: "x", Values: map[string]any{"unknown.key": 1.0}},
		{Slug: "x", Name: "x", Values: map[string]any{"initiative.theme_slots": true}},
		{Slug: "x", Name: "x", Values: map[string]any{"initiative.deluxe": 3.0}},
	}
	for _, in := range bad {
		if _, err := s.CreateTier(ctx, "admin", in); !errors.Is(err, models.ErrInvalid) {
			t.Errorf("%+v err = %v", in, err)
		}
	}
	if _, err := s.CreateTier(ctx, "admin", TierInput{Slug: "free", Name: "Dup"}); !errors.Is(err, ErrDuplicate) {
		t.Errorf("dup tier = %v", err)
	}
}

func TestUpdateAndDeleteTier(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	got, err := s.UpdateTier(ctx, "admin", TierInput{Slug: "plus", Name: "Plus+", Values: map[string]any{"initiative.theme_slots": 50.0}})
	if err != nil || got.Name != "Plus+" || len(got.Values) != 1 {
		t.Errorf("got %+v err %v", got, err)
	}
	if _, err := s.UpdateTier(ctx, "admin", TierInput{Slug: "ghost", Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update missing = %v", err)
	}
	if err := s.DeleteTier(ctx, "admin", "free"); !errors.Is(err, ErrInUse) {
		t.Errorf("delete default = %v", err)
	}
	if err := s.DeleteTier(ctx, "admin", "plus"); err != nil {
		t.Errorf("delete plus = %v", err)
	}
	if err := s.DeleteTier(ctx, "admin", "plus"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice = %v", err)
	}
	tiers, _ := s.ListTiers(ctx, 10, 0)
	if len(tiers) != 1 || tiers[0].Slug != "free" {
		t.Errorf("tiers = %+v", tiers)
	}
}

func TestSetDefaultTierRequiresExistingTier(t *testing.T) {
	s, _ := newSvc(t)
	if err := s.SetDefaultTier(ctx, "admin", "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	seed(t, s)
	_ = s.SetDefaultTier(ctx, "admin", "plus")
	if id, _ := s.DefaultTier(ctx); id != "plus" {
		t.Errorf("default = %q", id)
	}
}

func TestEveryWriteIsAudited(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	_, _ = s.UpdateKey(ctx, "admin", "initiative.deluxe", "d", 0)
	_ = s.DeleteKey(ctx, "admin", "initiative.deluxe")
	entries, _ := s.ListAudit(ctx, 100, 0)
	// 2 key creates + 2 tier creates + set_default + update + delete
	if len(entries) != 7 {
		t.Fatalf("entries = %d", len(entries))
	}
	if entries[0].Action != "delete" || entries[0].Actor != "admin" || entries[0].Subject != "initiative.deluxe" {
		t.Errorf("newest = %+v", entries[0])
	}
}

func TestNewUserGetsDefaultTier(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	if v := value(t, s, "u1", "initiative.theme_slots"); v != 5 {
		t.Errorf("value = %d, want 5", v)
	}
}

func TestRaisingDefaultLimitNeedsNoDeploy(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	_, _ = s.UpdateTier(ctx, "admin", TierInput{Slug: "free", Name: "Free", Values: map[string]any{"initiative.theme_slots": 9.0}})
	if v := value(t, s, "u1", "initiative.theme_slots"); v != 9 {
		t.Errorf("value = %d, want 9", v)
	}
}

func TestStackingTakesMaximum(t *testing.T) {
	s, st := newSvc(t)
	seed(t, s)
	st.AddGrant(models.Grant{UserID: "u1", TierID: "plus", Status: models.GrantActive})
	st.AddGrant(models.Grant{UserID: "u1", Key: "initiative.theme_slots", Value: 12, Status: models.GrantActive})
	if v := value(t, s, "u1", "initiative.theme_slots"); v != 20 {
		t.Errorf("tier grant should win: %d", v)
	}
	st.AddGrant(models.Grant{UserID: "u1", Key: "initiative.theme_slots", Value: 40, Status: models.GrantActive})
	if v := value(t, s, "u1", "initiative.theme_slots"); v != 40 {
		t.Errorf("key grant should win: %d", v)
	}
	if v := value(t, s, "u1", "initiative.deluxe"); v != 1 {
		t.Errorf("flag from plus = %d", v)
	}
	if v := value(t, s, "other", "initiative.deluxe"); v != 0 {
		t.Errorf("other user flag = %d", v)
	}
}

func TestExpiredAndEndedGrantsDoNotCount(t *testing.T) {
	s, st := newSvc(t)
	seed(t, s)
	past := time.Now().Add(-time.Hour)
	st.AddGrant(models.Grant{UserID: "u1", TierID: "plus", Status: models.GrantActive, ValidUntil: &past})
	st.AddGrant(models.Grant{UserID: "u1", TierID: "plus", Status: models.GrantEnded})
	if v := value(t, s, "u1", "initiative.theme_slots"); v != 5 {
		t.Errorf("value = %d, want default 5", v)
	}
}

func TestGrantOfDeletedTierContributesNothing(t *testing.T) {
	s, st := newSvc(t)
	seed(t, s)
	st.AddGrant(models.Grant{UserID: "u1", TierID: "plus", Status: models.GrantActive})
	_ = s.DeleteTier(ctx, "admin", "plus")
	if v := value(t, s, "u1", "initiative.theme_slots"); v != 5 {
		t.Errorf("value = %d", v)
	}
}

func TestNoDefaultTierMeansZero(t *testing.T) {
	s, _ := newSvc(t)
	_, _ = s.CreateKey(ctx, "admin", KeyInput{Key: "a.b", Type: models.TypeLimit})
	if v := value(t, s, "u1", "a.b"); v != 0 {
		t.Errorf("value = %d", v)
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	_, err := s.Check(ctx, "u1", []string{"initiative.theme_slots", "initiative.theme_slotz"})
	var unknown UnknownKeysError
	if !errors.As(err, &unknown) || len(unknown.Keys) != 1 || unknown.Keys[0] != "initiative.theme_slotz" {
		t.Errorf("err = %v", err)
	}
}

func TestCheckReturnsTTLTypeAndDedupes(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	r, err := s.Check(ctx, "u1", []string{"initiative.theme_slots", "initiative.theme_slots"})
	if err != nil || len(r) != 1 || r[0].TTLSeconds != 60 || r[0].Type != models.TypeLimit {
		t.Errorf("r = %+v err = %v", r, err)
	}
}

func TestCheckPrefix(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	_, _ = s.CreateKey(ctx, "admin", KeyInput{Key: "other.thing", Type: models.TypeFlag})
	r, err := s.CheckPrefix(ctx, "u1", "initiative.")
	if err != nil || len(r) != 2 || r[0].Key != "initiative.deluxe" || r[1].Key != "initiative.theme_slots" {
		t.Errorf("r = %+v err = %v", r, err)
	}
}
