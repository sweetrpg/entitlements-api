package catalog

import (
	"errors"
	"testing"

	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

// paidUser has 10 members paid for (two purchases of 5) on top of the free 4.
func paidUser(t *testing.T, enabled bool) (*Service, *data.MemoryStore) {
	t.Helper()
	s, st := seedMembers(t)
	s.EnablePaidReductions(enabled)
	st.AddGrant(paidGrant("u1", "a.members", 5, models.ModeAdd, "pi_1"))
	st.AddGrant(paidGrant("u1", "a.members", 5, models.ModeAdd, "pi_2"))
	return s, st
}

func TestReductionBelowPaidValueNeedsAcknowledgmentAndReference(t *testing.T) {
	cases := []struct {
		name string
		in   OverrideInput
	}{
		{"no acknowledgment", OverrideInput{Value: 3.0, Reason: "r"}},
		{"acknowledged without a reference", OverrideInput{Value: 3.0, Reason: "r", AcknowledgePaidReduction: true}},
		{"reference without acknowledgment", OverrideInput{Value: 3.0, Reason: "r", Reference: "case 9"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _ := paidUser(t, true)
			_, err := s.SetOverride(ctx, "admin1", "u1", "a.members", c.in)
			var pe *PaidReductionError
			if !errors.As(err, &pe) {
				t.Fatalf("err = %v, want PaidReductionError", err)
			}
			if pe.Paid != 10 || pe.Proposed != 3 || len(pe.Payments) != 2 {
				t.Errorf("error = paid %d proposed %d payments %d, want 10 / 3 / 2", pe.Paid, pe.Proposed, len(pe.Payments))
			}
			if v := value(t, s, "u1", "a.members"); v != 14 {
				t.Errorf("value = %d, a rejected reduction must change nothing", v)
			}
		})
	}
}

func TestAcknowledgedReductionIsAppliedAndAuditedWhenEnabled(t *testing.T) {
	s, st := paidUser(t, true)
	in := OverrideInput{Value: 3.0, Reason: "terms violation", AcknowledgePaidReduction: true, Reference: "case 9"}
	if _, err := s.SetOverride(ctx, "admin1", "u1", "a.members", in); err != nil {
		t.Fatal(err)
	}
	if v := value(t, s, "u1", "a.members"); v != 3 {
		t.Errorf("value = %d, want 3", v)
	}
	entries, _ := st.ListPaidReductions(ctx, 10, 0)
	if len(entries) != 1 {
		t.Fatalf("paid reductions = %d, want 1", len(entries))
	}
	e := entries[0]
	if !e.PaidReduction || e.Reference != "case 9" || e.Actor != "admin1" || e.Subject != "u1/a.members" || e.Paid != int64(10) || e.After != int64(3) {
		t.Errorf("entry = %+v", e)
	}
	// the paid grants are untouched
	if n := len(mustGrants(t, st)); n != 3 {
		t.Errorf("active grants = %d, want the 2 payments and the override", n)
	}
}

func TestAcknowledgedReductionIsRejectedWhenTheSwitchIsOff(t *testing.T) {
	s, st := paidUser(t, false)
	in := OverrideInput{Value: 3.0, Reason: "r", AcknowledgePaidReduction: true, Reference: "case 9"}
	if _, err := s.SetOverride(ctx, "admin1", "u1", "a.members", in); !errors.Is(err, ErrPaidReductionsDisabled) {
		t.Errorf("err = %v, want ErrPaidReductionsDisabled", err)
	}
	if v := value(t, s, "u1", "a.members"); v != 14 {
		t.Errorf("value = %d, want unchanged 14", v)
	}
	if list, _ := st.ListPaidReductions(ctx, 10, 0); len(list) != 0 {
		t.Errorf("paid reductions = %d, want 0", len(list))
	}
}

func TestOverridesThatKeepThePaidValueNeedNoAcknowledgment(t *testing.T) {
	for _, v := range []float64{10, 11, 20} {
		s, st := paidUser(t, false)
		if _, err := s.SetOverride(ctx, "admin1", "u1", "a.members", OverrideInput{Value: v, Reason: "r"}); err != nil {
			t.Errorf("override %v: %v", v, err)
		}
		if list, _ := st.ListPaidReductions(ctx, 10, 0); len(list) != 0 {
			t.Errorf("override %v flagged as a paid reduction", v)
		}
	}
}

func TestKeyWithNoPaymentNeedsNoAcknowledgmentEvenWhenLowered(t *testing.T) {
	s, _ := seedMembers(t)
	if _, err := s.SetOverride(ctx, "admin1", "u1", "a.members", OverrideInput{Value: 1.0, Reason: "r"}); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestPaidFlagTurnedOffIsAReduction(t *testing.T) {
	s, st := seedMembers(t)
	st.AddGrant(models.Grant{UserID: "u1", Key: "a.flag", Value: 1, Source: models.SourceStripePurchase, ExternalID: "pi_f", Status: models.GrantActive})
	_, err := s.SetOverride(ctx, "admin1", "u1", "a.flag", OverrideInput{Value: false, Reason: "r"})
	var pe *PaidReductionError
	if !errors.As(err, &pe) || pe.Paid != 1 || pe.Proposed != 0 {
		t.Errorf("err = %v, want paid reduction 1 -> 0", err)
	}
}

func TestPaidValueAboveTheKeyMaximumIsNotReducedByTheMaximum(t *testing.T) {
	s, st := seedMembers(t)
	st.AddGrant(paidGrant("u1", "a.members", 30, models.ModeAdd, "pi_big"))
	if _, err := s.SetOverride(ctx, "admin1", "u1", "a.members", OverrideInput{Value: 20.0, Reason: "r"}); err != nil {
		t.Errorf("setting the maximum (20) when 30 was paid: %v", err)
	}
}

func TestListPaidReductionsOnlyReturnsFlaggedEntries(t *testing.T) {
	s, st := paidUser(t, true)
	if _, err := s.SetOverride(ctx, "admin1", "u1", "a.members", OverrideInput{Value: 15.0, Reason: "gift"}); err != nil {
		t.Fatal(err)
	}
	in := OverrideInput{Value: 2.0, Reason: "r", AcknowledgePaidReduction: true, Reference: "ref"}
	if _, err := s.SetOverride(ctx, "admin1", "u1", "a.members", in); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.ListPaidReductions(ctx, 10, 0); len(list) != 1 || list[0].After != int64(2) {
		t.Errorf("list = %+v", list)
	}
}
