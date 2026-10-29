package server

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The seed files are data loaded in production by `sweetrpg entitlements seed` (sweetrpg-cli).
// These tests post the same files through the real admin routes, so a seed row the service would
// reject, or a value that no longer means what product intended, fails here.

type seedFile struct {
	Keys        []map[string]any `json:"keys"`
	DefaultTier string           `json:"default_tier"`
	Tiers       []map[string]any `json:"tiers"`
	Products    []map[string]any `json:"products"`
}

type pricesFile struct {
	Products map[string]string `json:"products"`
}

func readJSON(t *testing.T, path string, into any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func post(t *testing.T, r http.Handler, path string, body map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	mustCode(t, do(r, "POST", path, adminTok, string(b)), http.StatusCreated)
}

// loadSeed posts the seed file through the admin API and returns it.
func loadSeed(t *testing.T, r http.Handler) seedFile {
	t.Helper()
	var s seedFile
	readJSON(t, "../seed/initiative.json", &s)
	for _, k := range s.Keys {
		post(t, r, "/admin/keys", k)
	}
	for _, tier := range s.Tiers {
		post(t, r, "/admin/tiers", tier)
	}
	mustCode(t, do(r, "PUT", "/admin/default-tier", adminTok, `{"id":"`+s.DefaultTier+`"}`), http.StatusNoContent)
	for _, p := range s.Products {
		post(t, r, "/admin/products", p)
	}
	return s
}

func checkValue(t *testing.T, r http.Handler, key string) any {
	t.Helper()
	rec := do(r, "GET", "/check?key="+key, userTok, "")
	mustCode(t, rec, http.StatusOK)
	var vo CheckVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	return vo.Value
}

func TestSeedLoadsThroughTheAdminAPI(t *testing.T) {
	r, _ := newRouter(t)
	s := loadSeed(t, r)
	if len(s.Keys) != 10 || len(s.Tiers) != 1 || len(s.Products) != 9 {
		t.Fatalf("seed has %d keys, %d tiers, %d products", len(s.Keys), len(s.Tiers), len(s.Products))
	}

	var def struct{ ID string }
	_ = json.Unmarshal(do(r, "GET", "/admin/default-tier", adminTok, "").Body.Bytes(), &def)
	if def.ID != "free" {
		t.Errorf("default tier = %q", def.ID)
	}

	// A new user sees the free tier values.
	cases := map[string]any{
		"initiative.sessions_per_gm":         float64(1),
		"initiative.members_per_session":     float64(4),
		"initiative.saved_encounters":        float64(1),
		"initiative.saved_rosters":           float64(1),
		"initiative.live_subscriptions":      float64(3),
		"initiative.integration_credentials": float64(3),
		"initiative.theme_slots":             float64(0),
		"initiative.packaged_themes":         false,
		"initiative.customization":           false,
		"initiative.game_systems_extra":      false,
	}
	for key, want := range cases {
		if got := checkValue(t, r, key); got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}

func TestSeedMaximumsAreSessionsFiveAndMembersTwenty(t *testing.T) {
	r, _ := newRouter(t)
	loadSeed(t, r)
	var keys []struct {
		Key string
		Max *int64
	}
	_ = json.Unmarshal(do(r, "GET", "/admin/keys", adminTok, "").Body.Bytes(), &keys)
	max := map[string]int64{}
	for _, k := range keys {
		if k.Max != nil {
			max[k.Key] = *k.Max
		}
	}
	if len(max) != 2 || max["initiative.sessions_per_gm"] != 5 || max["initiative.members_per_session"] != 20 {
		t.Errorf("maximums = %v", max)
	}
}

func TestSeedMembersProductsShareAPlanGroup(t *testing.T) {
	r, _ := newRouter(t)
	loadSeed(t, r)
	var products []ProductVO
	_ = json.Unmarshal(do(r, "GET", "/admin/products", adminTok, "").Body.Bytes(), &products)
	groups := map[string]string{}
	for _, p := range products {
		if p.PlanGroup != "" {
			groups[p.ID] = p.PlanGroup
		}
		if p.StripePriceID != "" {
			t.Errorf("seed product %s carries a Stripe price ID; prices belong in the overlay", p.ID)
		}
	}
	if len(groups) != 2 || groups["members-10"] != "members" || groups["members-20"] != "members" {
		t.Errorf("plan groups = %v", groups)
	}
}

func TestDevPriceOverlayNamesOnlySeededProductsAndMakesThemPurchasable(t *testing.T) {
	r, _ := newRouter(t)
	s := loadSeed(t, r)
	var prices pricesFile
	readJSON(t, "../seed/prices.dev.json", &prices)

	seeded := map[string]bool{}
	for _, p := range s.Products {
		seeded[p["id"].(string)] = true
	}
	for id := range prices.Products {
		if !seeded[id] {
			t.Errorf("overlay prices unknown product %s", id)
		}
	}
	if len(prices.Products) != 9 || prices.Products["members-10"] != "members_10" {
		t.Errorf("prices = %v", prices.Products)
	}

	// Before prices, a seeded product cannot be bought.
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{"product_id":"encounters"}`), http.StatusBadRequest)
	applyPrices(t, r, prices)
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{"product_id":"encounters"}`), http.StatusOK)
	// members-10's price ID has no price_ prefix; it is opaque to the service.
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{"product_id":"members-10"}`), http.StatusOK)

	var products []ProductVO
	_ = json.Unmarshal(do(r, "GET", "/admin/products", adminTok, "").Body.Bytes(), &products)
	for _, p := range products {
		if p.ID != "members-20" {
			continue
		}
		got, _ := p.Entitlements["initiative.members_per_session"].(map[string]any)
		if p.StripePriceID == "" || p.PlanGroup != "members" || got["mode"] != "at_least" {
			t.Errorf("members-20 after pricing = %+v", p)
		}
	}
}

// applyPrices sets each overlay price the way the CLI does: read the product, put it back with
// the price.
func applyPrices(t *testing.T, r *gin.Engine, prices pricesFile) {
	t.Helper()
	var products []map[string]any
	_ = json.Unmarshal(do(r, "GET", "/admin/products", adminTok, "").Body.Bytes(), &products)
	for _, p := range products {
		id := p["id"].(string)
		price, ok := prices.Products[id]
		if !ok {
			continue
		}
		p["stripe_price_id"] = price
		b, _ := json.Marshal(p)
		mustCode(t, do(r, "PUT", "/admin/products/"+id, adminTok, string(b)), http.StatusOK)
	}
}

func TestSeedFilesHaveNoTrailingGarbage(t *testing.T) {
	for _, path := range []string{"../seed/initiative.json", "../seed/prices.dev.json"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil || !strings.HasSuffix(strings.TrimSpace(string(raw)), "}") {
			t.Errorf("%s is not a single JSON object: %v", path, err)
		}
	}
}
