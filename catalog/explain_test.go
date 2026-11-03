package catalog

import (
	"testing"
	"time"

	"github.com/sweetrpg/entitlements-api/models"
)

func paidGrant(user, key string, v int64, mode, ext string) models.Grant {
	return models.Grant{
		UserID: user, Key: key, Value: v, Mode: mode, Source: models.SourceStripePurchase,
		ExternalID: ext, ProductID: "seats", Status: models.GrantActive,
	}
}

func explain(t *testing.T, s *Service, user, key string) Explanation {
	t.Helper()
	list, err := s.ExplainUser(ctx, user, key)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("explanations = %d, want 1", len(list))
	}
	return list[0]
}

func TestExplainShowsEverySourceAndThePaidValue(t *testing.T) {
	s, st := seedMembers(t)
	st.AddGrant(paidGrant("u1", "a.members", 5, models.ModeAdd, "pi_1"))
	st.AddGrant(paidGrant("u1", "a.members", 5, models.ModeAdd, "pi_2"))
	st.AddGrant(models.Grant{UserID: "u1", Key: "a.members", Value: 2, Mode: models.ModeAdd, Source: models.SourceAdmin, ExternalID: "gift", Status: models.GrantActive})
	e := explain(t, s, "u1", "a.members")

	if e.Default == nil || *e.Default != 4 {
		t.Errorf("default = %v, want 4", e.Default)
	}
	if e.Paid != 10 || len(e.Payments) != 2 {
		t.Errorf("paid = %d from %d payments, want 10 from 2", e.Paid, len(e.Payments))
	}
	if e.Effective != 16 {
		t.Errorf("effective = %d, want 4 + 10 + 2", e.Effective)
	}
	if len(e.KeyGrants) != 3 {
		t.Errorf("key grants = %d, want 3", len(e.KeyGrants))
	}
}

func TestExplainIncludesTierGrantsAndPaidTierValue(t *testing.T) {
	s, st := seedMembers(t)
	if _, err := s.CreateTier(ctx, "admin", TierInput{Slug: "pro", Name: "Pro", Values: map[string]any{"a.members": 12.0}}); err != nil {
		t.Fatal(err)
	}
	st.AddGrant(models.Grant{UserID: "u1", TierID: "pro", Source: models.SourceStripeSubscription, ExternalID: "sub_1", Status: models.GrantActive})
	e := explain(t, s, "u1", "a.members")
	if len(e.TierGrants) != 1 || e.TierGrants[0].TierID != "pro" || e.TierGrants[0].Value != 12 {
		t.Errorf("tier grants = %+v", e.TierGrants)
	}
	if e.Paid != 12 || len(e.Payments) != 1 || e.Effective != 12 {
		t.Errorf("paid = %d (%d payments), effective = %d, want 12 / 1 / 12", e.Paid, len(e.Payments), e.Effective)
	}
}

func TestExplainShowsTheOverrideAndLeavesPaidAlone(t *testing.T) {
	s, st := seedMembers(t)
	st.AddGrant(paidGrant("u1", "a.members", 5, models.ModeAdd, "pi_1"))
	until := time.Now().Add(24 * time.Hour)
	if _, err := s.SetOverride(ctx, "admin1", "u1", "a.members", OverrideInput{Value: 20.0, ValidUntil: &until, Reason: "goodwill"}); err != nil {
		t.Fatal(err)
	}
	e := explain(t, s, "u1", "a.members")
	if e.Override == nil || e.Override.Reason != "goodwill" || e.Override.Value != 20 || e.Override.ValidUntil == nil {
		t.Errorf("override = %+v", e.Override)
	}
	if e.Paid != 5 || e.Effective != 20 {
		t.Errorf("paid = %d, effective = %d, want 5 / 20", e.Paid, e.Effective)
	}
}

func TestExplainWithNoPaymentsHasZeroPaid(t *testing.T) {
	s, _ := seedMembers(t)
	e := explain(t, s, "u1", "a.members")
	if e.Paid != 0 || len(e.Payments) != 0 || e.Effective != 4 || e.Override != nil {
		t.Errorf("explanation = %+v", e)
	}
}
