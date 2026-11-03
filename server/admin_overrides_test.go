package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
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
