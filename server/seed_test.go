package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sweetrpg/entitlements-api/models"
	"github.com/sweetrpg/entitlements-api/seed"
)

func seedGrant(user, key string) models.Grant {
	return models.Grant{UserID: user, Key: key, Value: 1, Status: models.GrantActive}
}

// loadSeed runs the real loader against the real admin routes.
func loadSeed(t *testing.T, r http.Handler) *seed.Report {
	t.Helper()
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	doc, err := seed.Parse(seed.Initiative)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := (&seed.Client{BaseURL: srv.URL, Token: adminTok}).Load(context.Background(), doc, false)
	if err != nil {
		t.Fatalf("load: %v (report %+v)", err, rep)
	}
	return rep
}

func TestSeedLoadsThroughTheAdminAPI(t *testing.T) {
	r, _ := newRouter(t)
	rep := loadSeed(t, r)
	if len(rep.Created) != 10+1+1+9 || len(rep.Existing) != 0 {
		t.Fatalf("report = %+v", rep)
	}

	var def struct{ ID string }
	_ = json.Unmarshal(do(r, "GET", "/admin/default-tier", adminTok, "").Body.Bytes(), &def)
	if def.ID != "free" {
		t.Errorf("default tier = %q", def.ID)
	}

	// A new user sees the free tier values, with the maxima in place.
	cases := map[string]any{
		"initiative.sessions_per_gm":         float64(1),
		"initiative.members_per_session":     float64(4),
		"initiative.live_subscriptions":      float64(3),
		"initiative.integration_credentials": float64(3),
		"initiative.theme_slots":             float64(0),
		"initiative.packaged_themes":         false,
	}
	for key, want := range cases {
		rec := do(r, "GET", "/check?key="+key, userTok, "")
		mustCode(t, rec, http.StatusOK)
		var vo CheckVO
		_ = json.Unmarshal(rec.Body.Bytes(), &vo)
		if vo.Value != want {
			t.Errorf("%s = %v, want %v", key, vo.Value, want)
		}
	}

	var products []ProductVO
	_ = json.Unmarshal(do(r, "GET", "/admin/products", adminTok, "").Body.Bytes(), &products)
	if len(products) != 9 {
		t.Errorf("products = %d", len(products))
	}
}

func TestSeedReloadChangesNothing(t *testing.T) {
	r, _ := newRouter(t)
	loadSeed(t, r)
	mustCode(t, do(r, "PUT", "/admin/products/themes", adminTok,
		`{"name":"Themes","billing":"one_off","stripe_price_id":"price_live","entitlements":{"initiative.customization":true}}`), http.StatusOK)

	rep := loadSeed(t, r)
	if len(rep.Created) != 0 || len(rep.Existing) != 10+1+1+9 {
		t.Fatalf("report = %+v", rep)
	}
	var products []ProductVO
	_ = json.Unmarshal(do(r, "GET", "/admin/products", adminTok, "").Body.Bytes(), &products)
	for _, p := range products {
		if p.ID == "themes" && p.StripePriceID != "price_live" {
			t.Errorf("reload overwrote the price ID: %q", p.StripePriceID)
		}
	}
}

func TestSeededSessionsAreCappedAtFive(t *testing.T) {
	r, st := newRouter(t)
	loadSeed(t, r)
	// Nine session units on top of the free 1 would be 10; the maximum is 5.
	for i := 0; i < 9; i++ {
		st.AddGrant(seedGrant("canonical-"+userTok, "initiative.sessions_per_gm"))
	}
	rec := do(r, "GET", "/check?key=initiative.sessions_per_gm", userTok, "")
	var vo CheckVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if vo.Value != float64(5) {
		t.Errorf("value = %v, want 5", vo.Value)
	}
}

func TestDevPricesApplyThroughTheAdminAPIAndMakeProductsPurchasable(t *testing.T) {
	r, _ := newRouter(t)
	rep := loadSeed(t, r)
	if len(rep.Created) == 0 {
		t.Fatal("seed not loaded")
	}
	// Before prices, a seeded product cannot be bought.
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{"product_id":"encounters"}`), http.StatusBadRequest)

	raw, err := os.ReadFile("../seed/prices.dev.json")
	if err != nil {
		t.Fatal(err)
	}
	prices, err := seed.ParsePrices(raw)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	client := &seed.Client{BaseURL: srv.URL, Token: adminTok}

	first := &seed.Report{}
	if err := client.ApplyPrices(context.Background(), prices, false, first); err != nil {
		t.Fatal(err)
	}
	if len(first.Created) != 8 {
		t.Fatalf("report = %+v", first)
	}
	again := &seed.Report{}
	if err := client.ApplyPrices(context.Background(), prices, false, again); err != nil || len(again.Created) != 0 || len(again.Existing) != 8 {
		t.Fatalf("second apply = %+v err = %v", again, err)
	}

	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{"product_id":"encounters"}`), http.StatusOK)
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{"product_id":"members-10"}`), http.StatusBadRequest) // price pending

	var products []ProductVO
	_ = json.Unmarshal(do(r, "GET", "/admin/products", adminTok, "").Body.Bytes(), &products)
	for _, p := range products {
		if p.ID != "members-20" {
			continue
		}
		got, _ := p.Entitlements["initiative.members_per_session"].(map[string]any)
		if p.StripePriceID != "price_0UViDdpRY4hLTjk70PBkZT8r" || p.PlanGroup != "members" || got["mode"] != "at_least" {
			t.Errorf("members-20 after pricing = %+v", p)
		}
	}
}
