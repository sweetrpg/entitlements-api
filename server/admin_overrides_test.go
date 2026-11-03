package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sweetrpg/entitlements-api/config"
	"github.com/sweetrpg/entitlements-api/models"
)

const ovPath = "/admin/users/target-user/overrides/initiative.theme_slots"

func TestOverrideRoutesRequireAdmin(t *testing.T) {
	r, _ := seedChecks(t)
	body := `{"value":9,"reason":"r"}`
	mustCode(t, do(r, "PUT", ovPath, "", body), http.StatusUnauthorized)
	mustCode(t, do(r, "PUT", ovPath, userTok, body), http.StatusForbidden)
	mustCode(t, do(r, "DELETE", ovPath, userTok, `{"reason":"r"}`), http.StatusForbidden)
	mustCode(t, do(r, "GET", "/admin/users/target-user/grants", userTok, ""), http.StatusForbidden)
}

func TestOverrideSetListRemove(t *testing.T) {
	r, st := seedChecks(t)
	rec := do(r, "PUT", ovPath, adminTok, `{"value":9,"reason":"support ticket 7"}`)
	mustCode(t, rec, http.StatusOK)
	var vo GrantVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if !vo.Override || vo.Value != float64(9) || vo.Reason != "support ticket 7" || vo.SetBy != "canonical-"+adminTok {
		t.Errorf("vo = %+v", vo)
	}

	rec = do(r, "GET", "/admin/users/target-user/grants", adminTok, "")
	mustCode(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"override":true`) {
		t.Errorf("list = %s", rec.Body)
	}

	mustCode(t, do(r, "DELETE", ovPath, adminTok, `{"reason":"done"}`), http.StatusNoContent)
	mustCode(t, do(r, "DELETE", ovPath, adminTok, `{"reason":"done"}`), http.StatusNotFound)

	entries, _ := st.ListAudit(ctx(), 10, 0)
	if len(entries) < 2 || entries[0].Action != "remove" || entries[0].Reason != "done" || entries[1].Action != "set" {
		t.Errorf("audit = %+v", entries)
	}
}

func TestOverrideWithoutReasonIsRejectedAndChangesNothing(t *testing.T) {
	r, _ := seedChecks(t)
	mustCode(t, do(r, "PUT", ovPath, adminTok, `{"value":9}`), http.StatusBadRequest)
	mustCode(t, do(r, "PUT", ovPath, adminTok, `{"value":9,"reason":""}`), http.StatusBadRequest)
	mustCode(t, do(r, "PUT", ovPath, adminTok, `{not json`), http.StatusBadRequest)
	mustCode(t, do(r, "PUT", "/admin/users/target-user/overrides/no.such", adminTok, `{"value":1,"reason":"r"}`), http.StatusNotFound)
	rec := do(r, "GET", "/admin/users/target-user/grants", adminTok, "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("grants = %s", rec.Body)
	}
}

func ctx() context.Context { return context.Background() }

func TestUserEntitlementsExplainEachKey(t *testing.T) {
	r, st := seedChecksWith(t, func(c *config.Config) { c.AdminPaidReductionsEnabled = true })
	st.AddGrant(models.Grant{UserID: "target-user", Key: "initiative.theme_slots", Value: 3, Mode: models.ModeAdd, Source: models.SourceStripePurchase, ExternalID: "pi_1", ProductID: "themes", Status: models.GrantActive})
	mustCode(t, do(r, "PUT", ovPath, adminTok, `{"value":2,"reason":"abuse","acknowledge_paid_reduction":true,"reference":"case 3"}`), http.StatusOK)

	mustCode(t, do(r, "GET", "/admin/users/target-user/entitlements", userTok, ""), http.StatusForbidden)
	rec := do(r, "GET", "/admin/users/target-user/entitlements?prefix=initiative.theme", adminTok, "")
	mustCode(t, rec, http.StatusOK)
	var got []EntitlementVO
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || len(got) != 1 {
		t.Fatalf("body = %s", rec.Body)
	}
	e := got[0]
	if e.Default != float64(5) || e.Paid != float64(3) || e.Effective != float64(2) {
		t.Errorf("default/paid/effective = %v/%v/%v, want 5/3/2", e.Default, e.Paid, e.Effective)
	}
	if e.Override == nil || e.Override.Reason != "abuse" || len(e.Payments) != 1 || len(e.Grants) != 1 {
		t.Errorf("entitlement = %+v", e)
	}
}

func paidUserRouter(t *testing.T, enabled bool) *gin.Engine {
	t.Helper()
	r, st := seedChecksWith(t, func(c *config.Config) { c.AdminPaidReductionsEnabled = enabled })
	st.AddGrant(models.Grant{UserID: "target-user", Key: "initiative.theme_slots", Value: 5, Mode: models.ModeAdd, Source: models.SourceStripePurchase, ExternalID: "pi_1", ProductID: "themes", Status: models.GrantActive})
	return r
}

func TestPaidReductionIsRejectedWithTheFactsUntilAcknowledged(t *testing.T) {
	r := paidUserRouter(t, true)
	rec := do(r, "PUT", ovPath, adminTok, `{"value":2,"reason":"abuse"}`)
	mustCode(t, rec, http.StatusConflict)
	var vo PaidReductionVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if vo.Error != "paid_reduction_not_acknowledged" || vo.Paid != float64(5) || vo.Proposed != float64(2) || len(vo.Payments) != 1 || vo.Key != "initiative.theme_slots" {
		t.Errorf("vo = %+v", vo)
	}
	mustCode(t, do(r, "PUT", ovPath, adminTok, `{"value":2,"reason":"abuse","acknowledge_paid_reduction":true}`), http.StatusConflict)
}

func TestAcknowledgedPaidReductionIsAppliedAndListed(t *testing.T) {
	r := paidUserRouter(t, true)
	mustCode(t, do(r, "PUT", ovPath, adminTok, `{"value":2,"reason":"abuse","acknowledge_paid_reduction":true,"reference":"case 12"}`), http.StatusOK)

	rec := do(r, "GET", "/admin/paid-reductions", adminTok, "")
	mustCode(t, rec, http.StatusOK)
	var got []PaidReductionEntryVO
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Fatalf("list = %s", rec.Body)
	}
	e := got[0]
	if e.UserID != "target-user" || e.Key != "initiative.theme_slots" || e.Paid != float64(5) || e.NewValue != float64(2) || e.Reference != "case 12" || e.Administrator != "canonical-"+adminTok {
		t.Errorf("entry = %+v", e)
	}
	mustCode(t, do(r, "GET", "/admin/paid-reductions", userTok, ""), http.StatusForbidden)
}

func TestPaidReductionIsRefusedWhenTheSwitchIsOffButOtherOverridesWork(t *testing.T) {
	r := paidUserRouter(t, false)
	rec := do(r, "PUT", ovPath, adminTok, `{"value":2,"reason":"abuse","acknowledge_paid_reduction":true,"reference":"case 12"}`)
	mustCode(t, rec, http.StatusForbidden)
	if !strings.Contains(rec.Body.String(), "paid_reductions_disabled") {
		t.Errorf("body = %s", rec.Body)
	}
	mustCode(t, do(r, "PUT", ovPath, adminTok, `{"value":8,"reason":"gift"}`), http.StatusOK)
	rec = do(r, "GET", "/admin/paid-reductions", adminTok, "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("list = %s", rec.Body)
	}
}

func TestUserAuditRoute(t *testing.T) {
	r, _ := seedChecks(t)
	mustCode(t, do(r, "GET", "/admin/users/target-user/audit", userTok, ""), http.StatusForbidden)
	rec := do(r, "GET", "/admin/users/target-user/audit", adminTok, "")
	mustCode(t, rec, http.StatusOK)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty = %s", rec.Body)
	}
	mustCode(t, do(r, "PUT", ovPath, adminTok, `{"value":9,"reason":"first"}`), http.StatusOK)
	mustCode(t, do(r, "PUT", "/admin/users/other-user/overrides/initiative.theme_slots", adminTok, `{"value":9,"reason":"x"}`), http.StatusOK)
	rec = do(r, "GET", "/admin/users/target-user/audit?limit=5", adminTok, "")
	var got []models.AuditEntry
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Subject != "target-user/initiative.theme_slots" || got[0].Reason != "first" || got[0].Actor != "canonical-"+adminTok {
		t.Errorf("entries = %+v", got)
	}
	mustCode(t, do(r, "GET", "/admin/users/bad%20id/audit", adminTok, ""), http.StatusBadRequest)
}

func TestPaymentGrantsCarryPurchaseDetailsInReadsAnd409(t *testing.T) {
	r, st := seedChecksWith(t, func(c *config.Config) { c.AdminPaidReductionsEnabled = true })
	bought := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	first := bought.AddDate(0, -2, 0)
	amount := int64(1500)
	g := models.Grant{UserID: "target-user", Key: "initiative.theme_slots", Value: 5, Mode: models.ModeAdd, Source: models.SourceStripePurchase, ExternalID: "pi_1", ProductID: "themes", Status: models.GrantActive,
		PurchasedAt: &bought, FirstPurchasedAt: &first, Amount: &amount, Currency: "usd"}
	g.CreatedAt = first
	st.AddGrant(g)
	// a grant from before the details were recorded stays readable, with nulls
	st.AddGrant(models.Grant{UserID: "target-user", Key: "initiative.theme_slots", Value: 1, Mode: models.ModeAdd, Source: models.SourceStripePurchase, ExternalID: "pi_old", Status: models.GrantActive})

	check := func(label string, payments []GrantVO) {
		t.Helper()
		var withDetails, without int
		for _, p := range payments {
			switch {
			case p.Amount != nil && *p.Amount == 1500 && p.Currency == "usd" && p.PurchasedAt.Equal(bought) && p.FirstPurchasedAt.Equal(first) && p.CreatedAt.Equal(first):
				withDetails++
			case p.Amount == nil && p.PurchasedAt == nil && p.FirstPurchasedAt == nil && p.Currency == "":
				without++
			}
		}
		if withDetails != 1 || without != 1 {
			t.Errorf("%s: payments = %+v", label, payments)
		}
	}

	rec := do(r, "GET", "/admin/users/target-user/entitlements?prefix=initiative.theme", adminTok, "")
	mustCode(t, rec, http.StatusOK)
	var got []EntitlementVO
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	check("entitlements", got[0].Payments)
	if !strings.Contains(rec.Body.String(), `"amount":null`) {
		t.Errorf("legacy grant should show amount null: %s", rec.Body)
	}

	rec = do(r, "PUT", ovPath, adminTok, `{"value":1,"reason":"r"}`)
	mustCode(t, rec, http.StatusConflict)
	var vo PaidReductionVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	check("409", vo.Payments)
}
