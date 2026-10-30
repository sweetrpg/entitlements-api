package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v87"
	"github.com/stripe/stripe-go/v87/webhook"
	apiv "github.com/sweetrpg/api-core.go/vo"
	"github.com/sweetrpg/entitlements-api/billing"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/models"
)

// fakeStripeErr, when set by a test, is returned by every fake provider call.
var fakeStripeErr error

type fakeStripe struct{}

func (fakeStripe) CreateCheckout(_ context.Context, p billing.CheckoutParams) (string, error) {
	if fakeStripeErr != nil {
		return "", fakeStripeErr
	}
	return "https://checkout.example/" + p.UserID + "/" + p.TierID + p.ProductID + "?email=" + p.Email, nil
}

// Price is 10.00 USD a month, or 20.00 for an ID ending in 20.
func (fakeStripe) Price(_ context.Context, id string) (billing.PriceInfo, error) {
	amount := int64(1000)
	if strings.HasSuffix(id, "20") {
		amount = 2000
	}
	return billing.PriceInfo{ID: id, UnitAmount: amount, Currency: "usd", Interval: "month", IntervalCount: 1}, fakeStripeErr
}

func (fakeStripe) Subscription(_ context.Context, id string) (billing.SubscriptionInfo, error) {
	return billing.SubscriptionInfo{ID: id, CustomerID: "cus_x", ItemID: "si_" + id, Quantity: 1,
		Price: billing.PriceInfo{ID: "price_old", UnitAmount: 1000, Currency: "usd", Interval: "month", IntervalCount: 1}}, fakeStripeErr
}

// fakeSwitchErr, when set by a test, is returned by SwitchPlan only.
var fakeSwitchErr error

func (fakeStripe) SwitchPlan(context.Context, billing.SwitchParams) error {
	if fakeSwitchErr != nil {
		return fakeSwitchErr
	}
	return fakeStripeErr
}

func (fakeStripe) CreatePortal(_ context.Context, customerID, _ string) (string, error) {
	if fakeStripeErr != nil {
		return "", fakeStripeErr
	}
	return "https://portal.example/" + customerID, nil
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "billing", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func postWebhook(r http.Handler, payload []byte, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", strings.NewReader(string(payload)))
	if header != "" {
		req.Header.Set("Stripe-Signature", header)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func signed(payload []byte, secret string) string {
	return webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: payload, Secret: secret}).Header
}

func TestWebhookRejectsBadSignaturesAndChangesNothing(t *testing.T) {
	r, st := newRouter(t)
	payload := fixture(t, "checkout_payment.json")
	cases := map[string]string{
		"missing header":   "",
		"garbage header":   "nonsense",
		"wrong secret":     signed(payload, "whsec_other"),
		"payload tampered": signed([]byte(strings.Replace(string(payload), "user-1", "user-9", 1)), webhookSecret),
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			mustCode(t, postWebhook(r, payload, header), http.StatusBadRequest)
		})
	}
	if gs, _ := st.GrantsByExternal(context.Background(), models.SourceStripePurchase, "pi_1"); len(gs) != 0 {
		t.Error("a grant was created from an unverified request")
	}
}

func TestWebhookAcceptsSignedEventAndCreatesGrant(t *testing.T) {
	r, st := newRouter(t)
	payload := fixture(t, "checkout_payment.json")
	mustCode(t, postWebhook(r, payload, signed(payload, webhookSecret)), http.StatusOK)
	gs, err := st.GrantsByExternal(context.Background(), models.SourceStripePurchase, "pi_1")
	if err != nil || len(gs) != 1 || gs[0].UserID != "user-1" {
		t.Fatalf("grants = %+v err = %v", gs, err)
	}
}

func TestWebhookReplayIsAccepted(t *testing.T) {
	r, _ := newRouter(t)
	payload := fixture(t, "checkout_payment.json")
	h := signed(payload, webhookSecret)
	mustCode(t, postWebhook(r, payload, h), http.StatusOK)
	mustCode(t, postWebhook(r, payload, h), http.StatusOK)
}

func TestWebhookNeedsNoUserToken(t *testing.T) {
	r, _ := newRouter(t)
	payload := fixture(t, "unhandled.json")
	mustCode(t, postWebhook(r, payload, signed(payload, webhookSecret)), http.StatusOK)
}

func TestBillingRoutesRequireUserToken(t *testing.T) {
	r, _ := newRouter(t)
	mustCode(t, do(r, "POST", "/billing/checkout", "", `{"tier_id":"plus"}`), http.StatusUnauthorized)
	mustCode(t, do(r, "POST", "/billing/checkout", "bogus", `{"tier_id":"plus"}`), http.StatusUnauthorized)
	mustCode(t, do(r, "POST", "/billing/portal", "", ``), http.StatusUnauthorized)
}

func TestCheckoutUsesVerifiedUserNotRequestBody(t *testing.T) {
	r, st := newRouter(t)
	if err := st.InsertTier(context.Background(), &models.Tier{Slug: "plus", Name: "Plus", StripePriceID: "price_plus"}, "admin"); err != nil {
		t.Fatal(err)
	}
	rec := do(r, "POST", "/billing/checkout", userTok, `{"tier_id":"plus","user_id":"someone-else"}`)
	mustCode(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "canonical-"+userTok+"/plus") {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestCheckoutRejectsBadRequests(t *testing.T) {
	r, st := newRouter(t)
	if err := st.InsertTier(context.Background(), &models.Tier{Slug: "free", Name: "Free"}, "admin"); err != nil {
		t.Fatal(err)
	}
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{}`), http.StatusBadRequest)
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{not json`), http.StatusBadRequest)
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{"tier_id":"free"}`), http.StatusBadRequest)
}

func TestPortalNotFoundWithoutCustomerThenOpens(t *testing.T) {
	r, st := newRouter(t)
	mustCode(t, do(r, "POST", "/billing/portal", userTok, ``), http.StatusNotFound)
	if err := st.SaveStripeCustomer(context.Background(), "canonical-"+userTok, "cus_7", "system"); err != nil {
		t.Fatal(err)
	}
	rec := do(r, "POST", "/billing/portal", userTok, ``)
	mustCode(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "cus_7") {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestTokenEmail(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	cases := map[string]string{
		"h." + enc(`{"email":"a@example.test"}`) + ".s": "a@example.test",
		"h." + enc(`{"sub":"x"}`) + ".s":                "",
		"h.!!!.s":                                       "",
		"h." + enc(`not json`) + ".s":                   "",
		"opaque-token":                                  "",
	}
	for tok, want := range cases {
		if got := tokenEmail(tok); got != want {
			t.Errorf("tokenEmail(%q) = %q, want %q", tok, got, want)
		}
	}
}

func TestMaxExceededIsA409WithUnitsRemaining(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	writeError(c, fmt.Errorf("wrapped: %w", catalog.MaxExceededError{Key: "initiative.sessions_per_gm", Remaining: 2}))
	mustCode(t, rec, http.StatusConflict)
	var vo MaxExceededVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if vo.Error != "exceeds_maximum" || vo.UnitsRemaining != 2 || vo.Key != "initiative.sessions_per_gm" {
		t.Errorf("body = %+v", vo)
	}
}

func TestAdminCanSetAndClearAKeyMaximum(t *testing.T) {
	r, _ := newRouter(t)
	rec := do(r, "POST", "/admin/keys", adminTok, `{"key":"a.cap","type":"limit","max":5}`)
	mustCode(t, rec, http.StatusCreated)
	var k models.Key
	_ = json.Unmarshal(rec.Body.Bytes(), &k)
	if k.Max == nil || *k.Max != 5 {
		t.Fatalf("max = %v", k.Max)
	}
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"a.flag","type":"flag","max":5}`), http.StatusBadRequest)
	rec = do(r, "PUT", "/admin/keys/a.cap", adminTok, `{"description":"d"}`)
	mustCode(t, rec, http.StatusOK)
	k = models.Key{}
	_ = json.Unmarshal(rec.Body.Bytes(), &k)
	if k.Max != nil {
		t.Errorf("max after clearing = %v", *k.Max)
	}
}

func adminProduct(t *testing.T, r http.Handler, body string) {
	t.Helper()
	mustCode(t, do(r, "POST", "/admin/products", adminTok, body), http.StatusCreated)
}

func TestAdminProductEndpoints(t *testing.T) {
	r, _ := newRouter(t)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"a.slots","type":"limit"}`), http.StatusCreated)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"a.pro","type":"flag"}`), http.StatusCreated)

	body := `{"id":"pack","name":"Pack","billing":"one_off","stripe_price_id":"price_1","entitlements":{"a.slots":2,"a.pro":true}}`
	mustCode(t, do(r, "POST", "/admin/products", "", body), http.StatusUnauthorized)
	mustCode(t, do(r, "POST", "/admin/products", userTok, body), http.StatusForbidden)
	rec := do(r, "POST", "/admin/products", adminTok, body)
	mustCode(t, rec, http.StatusCreated)
	var vo ProductVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if vo.ID != "pack" || vo.Entitlements["a.slots"] != float64(2) || vo.Entitlements["a.pro"] != true {
		t.Errorf("vo = %+v", vo)
	}
	mustCode(t, do(r, "POST", "/admin/products", adminTok, body), http.StatusConflict)
	mustCode(t, do(r, "POST", "/admin/products", adminTok, `{"id":"bad","name":"B","billing":"one_off","entitlements":{"no.such":1}}`), http.StatusBadRequest)
	mustCode(t, do(r, "POST", "/admin/products", adminTok, `{not json`), http.StatusBadRequest)

	mustCode(t, do(r, "PUT", "/admin/products/pack", adminTok, `{"name":"Pack 2","billing":"recurring","repeatable":true,"entitlements":{"a.slots":4}}`), http.StatusOK)
	mustCode(t, do(r, "PUT", "/admin/products/ghost", adminTok, `{"name":"G","billing":"one_off","entitlements":{"a.slots":1}}`), http.StatusNotFound)
	mustCode(t, do(r, "PUT", "/admin/products/pack", adminTok, `{not json`), http.StatusBadRequest)

	rec = do(r, "GET", "/admin/products", adminTok, "")
	mustCode(t, rec, http.StatusOK)
	var list []ProductVO
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Name != "Pack 2" || !list[0].Repeatable {
		t.Errorf("list = %+v", list)
	}
	mustCode(t, do(r, "DELETE", "/admin/products/pack", adminTok, ""), http.StatusNoContent)
	mustCode(t, do(r, "DELETE", "/admin/products/pack", adminTok, ""), http.StatusNotFound)
}

func TestProductCheckoutOverHTTPAndMaximumRefusal(t *testing.T) {
	r, _ := newRouter(t)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"a.slots","type":"limit","max":5}`), http.StatusCreated)
	adminProduct(t, r, `{"id":"slot","name":"Slot","billing":"one_off","repeatable":true,"stripe_price_id":"price_s","entitlements":{"a.slots":2}}`)

	rec := do(r, "POST", "/billing/checkout", userTok, `{"product_id":"slot","quantity":2}`)
	mustCode(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "/slot") {
		t.Errorf("body = %s", rec.Body)
	}

	rec = do(r, "POST", "/billing/checkout", userTok, `{"product_id":"slot","quantity":3}`)
	mustCode(t, rec, http.StatusConflict)
	var vo MaxExceededVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if vo.Error != "exceeds_maximum" || vo.UnitsRemaining != 5 || vo.Key != "a.slots" {
		t.Errorf("body = %+v", vo)
	}
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{"product_id":"slot","tier_id":"x"}`), http.StatusBadRequest)
}

func TestAdminProductRendersAtLeastEntitlements(t *testing.T) {
	r, _ := newRouter(t)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"a.members","type":"limit"}`), http.StatusCreated)
	rec := do(r, "POST", "/admin/products", adminTok,
		`{"id":"m10","name":"M10","billing":"recurring","entitlements":{"a.members":{"value":10,"mode":"at_least"}}}`)
	mustCode(t, rec, http.StatusCreated)
	var vo ProductVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	got, _ := vo.Entitlements["a.members"].(map[string]any)
	if got["value"] != float64(10) || got["mode"] != "at_least" {
		t.Errorf("entitlements = %+v", vo.Entitlements)
	}
	mustCode(t, do(r, "POST", "/admin/products", adminTok,
		`{"id":"bad","name":"B","billing":"recurring","entitlements":{"a.members":{"value":10,"mode":"nope"}}}`), http.StatusBadRequest)
}

func TestCheckoutNoIncreaseIs409(t *testing.T) {
	r, st := newRouter(t)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"a.members","type":"limit"}`), http.StatusCreated)
	adminProduct(t, r, `{"id":"m10","name":"M10","billing":"recurring","stripe_price_id":"price_m","entitlements":{"a.members":{"value":10,"mode":"at_least"}}}`)
	st.AddGrant(models.Grant{UserID: "canonical-" + userTok, Key: "a.members", Value: 10, Mode: models.ModeAtLeast, Status: models.GrantActive})
	rec := do(r, "POST", "/billing/checkout", userTok, `{"product_id":"m10"}`)
	mustCode(t, rec, http.StatusConflict)
	if !strings.Contains(rec.Body.String(), "no_increase") {
		t.Errorf("body = %s", rec.Body)
	}
}

func TestCheckoutInAPlanGroupSwitchesInsteadOfCreatingASession(t *testing.T) {
	r, st := newRouter(t)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"a.members","type":"limit"}`), http.StatusCreated)
	for _, n := range []string{"10", "20"} {
		adminProduct(t, r, `{"id":"m`+n+`","name":"M`+n+`","billing":"recurring","plan_group":"members","stripe_price_id":"price_`+n+
			`","entitlements":{"a.members":{"value":`+n+`,"mode":"at_least"}}}`)
	}
	st.AddGrant(models.Grant{UserID: "canonical-" + userTok, Key: "a.members", Value: 10, Mode: models.ModeAtLeast, ProductID: "m10",
		Source: models.SourceStripeSubscription, ExternalID: "sub_1", Status: models.GrantActive})

	rec := do(r, "POST", "/billing/checkout", userTok, `{"product_id":"m20"}`)
	mustCode(t, rec, http.StatusOK)
	var vo SessionVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if !vo.Switched || vo.URL != "" {
		t.Errorf("body = %s", rec.Body)
	}
	mustCode(t, do(r, "POST", "/billing/checkout", userTok, `{"product_id":"m20"}`), http.StatusConflict)

	rec = do(r, "GET", "/admin/products", adminTok, "")
	if !strings.Contains(rec.Body.String(), `"plan_group":"members"`) {
		t.Errorf("plan_group missing from %s", rec.Body)
	}
}

func TestStripeFailureOnCheckoutIs502WithASafeMessage(t *testing.T) {
	r, _ := newRouter(t)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"a.members","type":"limit"}`), http.StatusCreated)
	adminProduct(t, r, `{"id":"m10","name":"M10","billing":"recurring","stripe_price_id":"members_10","entitlements":{"a.members":{"value":10,"mode":"at_least"}}}`)
	old := fakeStripeErr
	fakeStripeErr = fmt.Errorf("retrieve price: %w", &stripe.Error{Code: stripe.ErrorCodeResourceMissing, Msg: "No such price: 'members_10'", RequestID: "req_zzz"})
	t.Cleanup(func() { fakeStripeErr = old })

	rec := do(r, "POST", "/billing/checkout", userTok, `{"product_id":"m10"}`)
	mustCode(t, rec, http.StatusBadGateway)
	var vo apiv.ErrorVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if vo.Error != "payment_provider_error" || vo.Message != "the product's price is not valid" {
		t.Errorf("body = %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "req_zzz") || strings.Contains(rec.Body.String(), "members_10") {
		t.Errorf("body leaks provider details: %s", rec.Body)
	}

	fakeStripeErr = errors.New("dial tcp: timeout")
	rec = do(r, "POST", "/billing/portal", userTok, ``)
	if rec.Code != http.StatusNotFound { // no customer yet: the portal never reaches the provider
		t.Errorf("portal without a customer = %d", rec.Code)
	}
}

func TestFailedUpgradePaymentIs402(t *testing.T) {
	r, st := newRouter(t)
	mustCode(t, do(r, "POST", "/admin/keys", adminTok, `{"key":"a.members","type":"limit"}`), http.StatusCreated)
	for _, n := range []string{"10", "20"} {
		adminProduct(t, r, `{"id":"m`+n+`","name":"M`+n+`","billing":"recurring","plan_group":"members","stripe_price_id":"price_`+n+
			`","entitlements":{"a.members":{"value":`+n+`,"mode":"at_least"}}}`)
	}
	st.AddGrant(models.Grant{UserID: "canonical-" + userTok, Key: "a.members", Value: 10, Mode: models.ModeAtLeast, ProductID: "m10",
		Source: models.SourceStripeSubscription, ExternalID: "sub_1", Status: models.GrantActive})
	// price_20 costs more than the subscription's 10.00, so m20 is an upgrade that is charged now.
	old := fakeSwitchErr
	fakeSwitchErr = &stripe.Error{Type: stripe.ErrorTypeCard, Code: stripe.ErrorCodeCardDeclined, Msg: "declined", RequestID: "req_x"}
	t.Cleanup(func() { fakeSwitchErr = old })

	rec := do(r, "POST", "/billing/checkout", userTok, `{"product_id":"m20"}`)
	mustCode(t, rec, http.StatusPaymentRequired)
	var vo apiv.ErrorVO
	_ = json.Unmarshal(rec.Body.Bytes(), &vo)
	if vo.Error != "payment_failed" || vo.Message != "the payment could not be completed, so the plan was not changed" {
		t.Errorf("body = %s", rec.Body)
	}
	if strings.Contains(rec.Body.String(), "req_x") || strings.Contains(rec.Body.String(), "declined") {
		t.Errorf("body leaks provider details: %s", rec.Body)
	}
	gs, _ := st.GrantsByExternal(context.Background(), models.SourceStripeSubscription, "sub_1")
	if len(gs) != 1 || gs[0].ProductID != "m10" {
		t.Errorf("a failed payment changed the grants: %+v", gs)
	}
}
