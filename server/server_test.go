package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/sweetrpg/authz-client.go/authz"
	"github.com/sweetrpg/common.go/logging"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/config"
	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

const (
	svcToken = "initiative-service-token-0123456789abcdef"
	adminTok = "admin-user-token"
	userTok  = "plain-user-token"
)

// identityStub stands in for auth-api's /authz/check and users-api's /profile.
func identityStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch r.URL.Path {
		case "/authz/check":
			switch token {
			case adminTok:
				_ = json.NewEncoder(w).Encode(authz.CheckResponse{Allowed: true, Roles: []string{authz.RoleAdmin}, Sub: "auth0|admin"})
			case userTok:
				_ = json.NewEncoder(w).Encode(authz.CheckResponse{Allowed: true, Roles: []string{authz.RoleUser}, Sub: "auth0|user"})
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		case "/profile":
			_ = json.NewEncoder(w).Encode(map[string]string{"user_id": "canonical-" + token})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newRouter(t *testing.T) (*gin.Engine, *data.MemoryStore) {
	t.Helper()
	logging.Init()
	gin.SetMode(gin.TestMode)
	st := data.NewMemoryStore()
	id := identityStub(t)
	cfg := &config.Config{ServiceTokens: map[string]string{svcToken: "initiative-api"}}
	r := gin.New()
	SetupHandlers(r, catalog.New(st), authz.NewClient(id.URL, id.URL), cfg)
	return r, st
}

func do(r http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func mustCode(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, want, rec.Body)
	}
}

func TestAdminWritesRequireAdminRole(t *testing.T) {
	r, _ := newRouter(t)
	body := `{"key":"a.b","type":"flag"}`
	mustCode(t, do(r, "POST", "/admin/keys", "", body), http.StatusUnauthorized)
	mustCode(t, do(r, "POST", "/admin/keys", "bogus", body), http.StatusUnauthorized)
	mustCode(t, do(r, "POST", "/admin/keys", userTok, body), http.StatusForbidden)
	mustCode(t, do(r, "GET", "/admin/keys", userTok, ""), http.StatusForbidden)
	rec := do(r, "GET", "/admin/keys", adminTok, "")
	mustCode(t, rec, http.StatusOK)
}

func TestNonAdminWriteMakesNoChange(t *testing.T) {
	r, _ := newRouter(t)
	do(r, "POST", "/admin/keys", userTok, `{"key":"a.b","type":"flag"}`)
	var keys []models.Key
	rec := do(r, "GET", "/admin/keys", adminTok, "")
	_ = json.Unmarshal(rec.Body.Bytes(), &keys)
	if len(keys) != 0 {
		t.Errorf("keys = %v", keys)
	}
}

func TestAdminCatalogFlow(t *testing.T) {
	r, _ := newRouter(t)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"initiative.theme_slots","type":"limit","ttl_seconds":30}`), http.StatusCreated)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"initiative.theme_slots","type":"limit"}`), http.StatusConflict)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"bad","type":"limit"}`), http.StatusBadRequest)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{not json`), http.StatusBadRequest)
	mustCode(t, do(r, "PUT", "/admin/keys/initiative.theme_slots", adminTok, `{"description":"d","ttl_seconds":10}`), http.StatusOK)
	mustCode(t, do(r, "PUT", "/admin/keys/nope.nope", adminTok, `{}`), http.StatusNotFound)

	rec := do(r, "POST", "/admin/tiers", adminTok, `{"id":"free","name":"Free","values":{"initiative.theme_slots":5}}`)
	mustCode(t, rec, http.StatusCreated)
	var tier TierVO
	_ = json.Unmarshal(rec.Body.Bytes(), &tier)
	if tier.ID != "free" || tier.Values["initiative.theme_slots"] != float64(5) || tier.CreatedBy != "canonical-"+adminTok {
		t.Errorf("tier = %+v", tier)
	}
	mustCode(t, do(r, "POST", "/admin/tiers", adminTok, `{"id":"x","name":"X","values":{"unknown.key":1}}`), http.StatusBadRequest)
	mustCode(t, do(r, "PUT", "/admin/tiers/free", adminTok, `{"name":"Free 2","values":{}}`), http.StatusOK)
	mustCode(t, do(r, "PUT", "/admin/tiers/ghost", adminTok, `{"name":"G"}`), http.StatusNotFound)
	mustCode(t, do(r, "GET", "/admin/tiers", adminTok, ""), http.StatusOK)

	mustCode(t, do(r, "PUT", "/admin/default-tier", adminTok, `{}`), http.StatusBadRequest)
	mustCode(t, do(r, "PUT", "/admin/default-tier", adminTok, `{"id":"ghost"}`), http.StatusNotFound)
	mustCode(t, do(r, "PUT", "/admin/default-tier", adminTok, `{"id":"free"}`), http.StatusNoContent)
	rec = do(r, "GET", "/admin/default-tier", adminTok, "")
	mustCode(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), `"free"`) {
		t.Errorf("default = %s", rec.Body)
	}
	mustCode(t, do(r, "DELETE", "/admin/tiers/free", adminTok, ""), http.StatusConflict)
	mustCode(t, do(r, "DELETE", "/admin/keys/initiative.theme_slots", adminTok, ""), http.StatusNoContent)
	mustCode(t, do(r, "DELETE", "/admin/keys/initiative.theme_slots", adminTok, ""), http.StatusNotFound)

	rec = do(r, "GET", "/admin/audit?limit=3", adminTok, "")
	mustCode(t, rec, http.StatusOK)
	var audit []models.AuditEntry
	_ = json.Unmarshal(rec.Body.Bytes(), &audit)
	if len(audit) != 3 || audit[0].Actor != "canonical-"+adminTok {
		t.Errorf("audit = %+v", audit)
	}
}

func seedChecks(t *testing.T) (*gin.Engine, *data.MemoryStore) {
	r, st := newRouter(t)
	for _, b := range []string{
		`{"key":"initiative.theme_slots","type":"limit","ttl_seconds":60}`,
		`{"key":"initiative.deluxe","type":"flag"}`,
	} {
		mustCode(t, do(r, "POST", "/admin/keys", adminTok, b), http.StatusCreated)
	}
	mustCode(t, do(r, "POST", "/admin/tiers", adminTok, `{"id":"free","name":"Free","values":{"initiative.theme_slots":5,"initiative.deluxe":false}}`), http.StatusCreated)
	mustCode(t, do(r, "PUT", "/admin/default-tier", adminTok, `{"id":"free"}`), http.StatusNoContent)
	return r, st
}

func TestChecksRejectUnregisteredCallers(t *testing.T) {
	r, _ := seedChecks(t)
	path := "/check?user_id=u1&key=initiative.theme_slots"
	mustCode(t, do(r, "GET", path, "", ""), http.StatusUnauthorized)
	mustCode(t, do(r, "GET", path, "wrong-token", ""), http.StatusUnauthorized)
	mustCode(t, do(r, "GET", path, adminTok, ""), http.StatusUnauthorized)
	mustCode(t, do(r, "POST", "/check/batch", "", `{"user_id":"u1","keys":["a.b"]}`), http.StatusUnauthorized)
	mustCode(t, do(r, "GET", path, svcToken, ""), http.StatusOK)
}

func TestSingleCheck(t *testing.T) {
	r, _ := seedChecks(t)
	rec := do(r, "GET", "/check?user_id=u1&key=initiative.theme_slots", svcToken, "")
	mustCode(t, rec, http.StatusOK)
	var vo CheckVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if vo.Value != float64(5) || vo.TTLSeconds != 60 || vo.Type != models.TypeLimit {
		t.Errorf("vo = %+v", vo)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, max-age=60" {
		t.Errorf("Cache-Control = %q", cc)
	}
	rec = do(r, "GET", "/check?user_id=u1&key=initiative.deluxe", svcToken, "")
	if !strings.Contains(rec.Body.String(), `"value":false`) || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("flag response = %s / %q", rec.Body, rec.Header().Get("Cache-Control"))
	}
}

func TestSingleCheckErrors(t *testing.T) {
	r, _ := seedChecks(t)
	mustCode(t, do(r, "GET", "/check?user_id=u1&key=initiative.theme_slotz", svcToken, ""), http.StatusNotFound)
	mustCode(t, do(r, "GET", "/check?key=initiative.deluxe", svcToken, ""), http.StatusBadRequest)
	mustCode(t, do(r, "GET", "/check?user_id=u1", svcToken, ""), http.StatusBadRequest)
}

func TestBatchCheck(t *testing.T) {
	r, st := seedChecks(t)
	st.AddGrant(models.Grant{UserID: "u1", Key: "initiative.theme_slots", Value: 12, Status: models.GrantActive})

	rec := do(r, "POST", "/check/batch", svcToken, `{"user_id":"u1","prefix":"initiative."}`)
	mustCode(t, rec, http.StatusOK)
	var out BatchCheckVO
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Results) != 2 || out.Results[1].Key != "initiative.theme_slots" || out.Results[1].Value != float64(12) {
		t.Errorf("out = %+v", out)
	}

	rec = do(r, "POST", "/check/batch", svcToken, `{"user_id":"u1","keys":["initiative.deluxe"]}`)
	mustCode(t, rec, http.StatusOK)
}

func TestBatchCheckErrors(t *testing.T) {
	r, _ := seedChecks(t)
	mustCode(t, do(r, "POST", "/check/batch", svcToken, `{"keys":["a.b"]}`), http.StatusBadRequest)
	mustCode(t, do(r, "POST", "/check/batch", svcToken, `{"user_id":"u1"}`), http.StatusBadRequest)
	mustCode(t, do(r, "POST", "/check/batch", svcToken, `{"user_id":"u1","keys":["a.b"],"prefix":"a."}`), http.StatusBadRequest)
	mustCode(t, do(r, "POST", "/check/batch", svcToken, `{"user_id":"u1","keys":["initiative.nope"]}`), http.StatusNotFound)
	keys := strings.Repeat(`"a.b",`, 201)
	mustCode(t, do(r, "POST", "/check/batch", svcToken, `{"user_id":"u1","keys":[`+keys[:len(keys)-1]+`]}`), http.StatusBadRequest)
}

func TestPing(t *testing.T) {
	r, _ := newRouter(t)
	mustCode(t, do(r, "GET", "/status/ping", "", ""), http.StatusOK)
}
