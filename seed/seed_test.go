package seed

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestEmbeddedSeedIsConsistent(t *testing.T) {
	d, err := Parse(Initiative)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]Key{}
	for _, k := range d.Keys {
		if keys[k.Key].Key != "" {
			t.Errorf("duplicate key %s", k.Key)
		}
		keys[k.Key] = k
	}
	if len(keys) != 10 {
		t.Errorf("keys = %d, want 10", len(keys))
	}
	if len(d.Tiers) != 1 || d.Tiers[0].ID != "free" || d.DefaultTier != "free" {
		t.Fatalf("tiers = %+v default = %q", d.Tiers, d.DefaultTier)
	}
	for k := range keys {
		if _, ok := d.Tiers[0].Values[k]; !ok {
			t.Errorf("free tier has no value for %s", k)
		}
	}
	for k := range d.Tiers[0].Values {
		if _, ok := keys[k]; !ok {
			t.Errorf("free tier names undeclared key %s", k)
		}
	}
	if len(d.Products) != 9 {
		t.Errorf("products = %d, want 9", len(d.Products))
	}
	for _, p := range d.Products {
		if p.StripePriceID != "" {
			t.Errorf("%s has a Stripe price ID in the seed", p.ID)
		}
		for k := range p.Entitlements {
			if _, ok := keys[k]; !ok {
				t.Errorf("product %s names undeclared key %s", p.ID, k)
			}
		}
	}
}

func TestEmbeddedSeedMaximumsAndFreeValues(t *testing.T) {
	d, _ := Parse(Initiative)
	max := map[string]int64{}
	for _, k := range d.Keys {
		if k.Max != nil {
			max[k.Key] = *k.Max
		}
	}
	if len(max) != 2 || max["initiative.sessions_per_gm"] != 5 || max["initiative.members_per_session"] != 20 {
		t.Errorf("maximums = %v", max)
	}
	free := d.Tiers[0].Values
	if free["initiative.sessions_per_gm"] != float64(1) || free["initiative.members_per_session"] != float64(4) || free["initiative.packaged_themes"] != false {
		t.Errorf("free = %v", free)
	}
}

func TestParseRejectsUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(`{"keys":[{"key":"a.b","type":"flag","typo":1}]}`)); err == nil {
		t.Error("unknown field accepted")
	}
	if _, err := Parse([]byte(`not json`)); err == nil {
		t.Error("garbage accepted")
	}
}

type recorder struct {
	mu       sync.Mutex
	requests []string
	auth     string
}

func stub(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		rec.requests = append(rec.requests, r.Method+" "+r.URL.Path)
		rec.auth = r.Header.Get("Authorization")
		rec.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

var tiny = &Doc{
	Keys:        []Key{{Key: "a.b", Type: "flag"}},
	DefaultTier: "free",
	Tiers:       []Tier{{ID: "free", Name: "Free", Values: map[string]any{"a.b": false}}},
	Products:    []Product{{ID: "p", Name: "P", Billing: "one_off", Entitlements: map[string]any{"a.b": true}}},
}

func TestLoadSendsTokenAndSetsDefaultTierOnlyWhenNoneIsSet(t *testing.T) {
	srv, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(`{"id":""}`))
		case http.MethodPut:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	})
	rep, err := (&Client{BaseURL: srv.URL + "/", Token: "tok123"}).Load(context.Background(), tiny, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Created) != 4 || len(rep.Existing) != 0 {
		t.Errorf("report = %+v", rep)
	}
	if rec.auth != "Bearer tok123" {
		t.Errorf("auth = %q", rec.auth)
	}
	want := []string{"POST /admin/keys", "POST /admin/tiers", "GET /admin/default-tier", "PUT /admin/default-tier", "POST /admin/products"}
	if strings.Join(rec.requests, ",") != strings.Join(want, ",") {
		t.Errorf("requests = %v, want %v", rec.requests, want)
	}
}

func TestLoadLeavesExistingRowsAndAnAlreadySetDefaultAlone(t *testing.T) {
	srv, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"id":"paid"}`))
			return
		}
		w.WriteHeader(http.StatusConflict)
	})
	rep, err := (&Client{BaseURL: srv.URL, Token: "t"}).Load(context.Background(), tiny, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Created) != 0 || len(rep.Existing) != 4 {
		t.Errorf("report = %+v", rep)
	}
	for _, r := range rec.requests {
		if strings.HasPrefix(r, "PUT") {
			t.Errorf("an existing row was overwritten: %s", r)
		}
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	srv, rec := stub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"id":""}`)) })
	rep, err := (&Client{BaseURL: srv.URL, Token: "t"}).Load(context.Background(), tiny, true)
	if err != nil || len(rep.Created) != 4 {
		t.Fatalf("report = %+v err = %v", rep, err)
	}
	for _, r := range rec.requests {
		if !strings.HasPrefix(r, "GET") {
			t.Errorf("dry run sent %s", r)
		}
	}
}

func TestLoadStopsOnAnErrorAndNeverEchoesTheToken(t *testing.T) {
	srv, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_request", "message": "bad key"})
	})
	_, err := (&Client{BaseURL: srv.URL, Token: "supersecret"}).Load(context.Background(), tiny, false)
	if err == nil || !strings.Contains(err.Error(), "status 400") || strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("err = %v", err)
	}
	if len(rec.requests) != 1 {
		t.Errorf("requests after the failure = %v", rec.requests)
	}
}

func TestLoadReportsUnreachableServer(t *testing.T) {
	srv, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {})
	url := srv.URL
	srv.Close()
	if _, err := (&Client{BaseURL: url, Token: "t"}).Load(context.Background(), tiny, false); err == nil {
		t.Error("expected an error")
	}
}

func TestMembersProductsShareAPlanGroup(t *testing.T) {
	d, _ := Parse(Initiative)
	groups := map[string]string{}
	for _, p := range d.Products {
		if p.PlanGroup != "" {
			groups[p.ID] = p.PlanGroup
			if p.Billing != "recurring" {
				t.Errorf("%s is in a plan group but not recurring", p.ID)
			}
		}
	}
	if len(groups) != 2 || groups["members-10"] != "members" || groups["members-20"] != "members" {
		t.Errorf("plan groups = %v", groups)
	}
}

func TestDevPriceOverlayOnlyNamesSeededProducts(t *testing.T) {
	raw, err := os.ReadFile("prices.dev.json")
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePrices(raw)
	if err != nil {
		t.Fatal(err)
	}
	d, _ := Parse(Initiative)
	seeded := map[string]bool{}
	for _, pr := range d.Products {
		seeded[pr.ID] = true
	}
	for id := range p.Products {
		if !seeded[id] {
			t.Errorf("overlay prices unknown product %s", id)
		}
	}
	if len(p.Products) != 9 {
		t.Errorf("prices = %d, want 9", len(p.Products))
	}
	if p.Products["members-10"] != "members_10" {
		t.Errorf("members-10 price = %q", p.Products["members-10"])
	}
}

func TestParsePricesRejectsBadInput(t *testing.T) {
	for name, in := range map[string]string{
		"empty":         `{"products":{"a":""}}`,
		"padded":        `{"products":{"a":" price_x"}}`,
		"unknown field": `{"products":{},"extra":1}`,
		"garbage":       `nope`,
	} {
		if _, err := ParsePrices([]byte(in)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func liveProducts() string {
	return `[{"id":"a","name":"A","billing":"recurring","entitlements":{"x.y":{"value":10,"mode":"at_least"}}},` +
		`{"id":"b","name":"B","billing":"one_off","stripe_price_id":"price_old","entitlements":{"x.y":1}},` +
		`{"id":"c","name":"C","billing":"one_off","stripe_price_id":"price_c","entitlements":{"x.y":1}}]`
}

func TestApplyPricesSetsOnlyMissingPricesAndKeepsTheRestOfTheProduct(t *testing.T) {
	var put []byte
	srv, rec := stub(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_, _ = w.Write([]byte(liveProducts()))
		case http.MethodPut:
			put, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
		}
	})
	prices := &Prices{Products: map[string]string{"a": "price_a", "b": "price_new", "c": "price_c"}}
	rep := &Report{}
	if err := (&Client{BaseURL: srv.URL, Token: "t"}).ApplyPrices(context.Background(), prices, false, rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Created) != 1 || rep.Created[0] != "price a" || len(rep.Existing) != 1 || len(rep.Skipped) != 1 {
		t.Errorf("report = %+v", rep)
	}
	if got := strings.Join(rec.requests, ","); got != "GET /admin/products,PUT /admin/products/a" {
		t.Errorf("requests = %s", got)
	}
	var sent map[string]any
	_ = json.Unmarshal(put, &sent)
	ents, _ := sent["entitlements"].(map[string]any)
	if sent["stripe_price_id"] != "price_a" || sent["name"] != "A" || ents["x.y"] == nil {
		t.Errorf("PUT body = %s", put)
	}
}

func TestApplyPricesDryRunAndMissingProducts(t *testing.T) {
	srv, rec := stub(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(liveProducts())) })
	c := &Client{BaseURL: srv.URL, Token: "t"}
	rep := &Report{}
	if err := c.ApplyPrices(context.Background(), &Prices{Products: map[string]string{"a": "price_a", "z": "price_z"}}, true, rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Created) != 2 {
		t.Errorf("report = %+v", rep)
	}
	for _, r := range rec.requests {
		if strings.HasPrefix(r, "PUT") {
			t.Errorf("dry run wrote: %s", r)
		}
	}
	if err := c.ApplyPrices(context.Background(), &Prices{Products: map[string]string{"z": "price_z"}}, false, &Report{}); err == nil {
		t.Error("a missing product was not an error")
	}
}

func TestPriceIDsAreOpaque(t *testing.T) {
	p, err := ParsePrices([]byte(`{"products":{"a":"members_10","b":"anything-goes.123"}}`))
	if err != nil || p.Products["a"] != "members_10" {
		t.Fatalf("prices = %+v err = %v", p, err)
	}

	var put []byte
	srv, _ := stub(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(liveProducts()))
			return
		}
		put, _ = io.ReadAll(r.Body)
	})
	rep := &Report{}
	if err := (&Client{BaseURL: srv.URL, Token: "t"}).ApplyPrices(context.Background(), &Prices{Products: map[string]string{"a": "members_10"}}, false, rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(put), `"stripe_price_id":"members_10"`) || len(rep.Created) != 1 {
		t.Errorf("PUT body = %s report = %+v", put, rep)
	}
}
