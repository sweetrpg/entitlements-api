package server

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v87/webhook"
	"github.com/sweetrpg/entitlements-api/billing"
	"github.com/sweetrpg/entitlements-api/models"
)

type fakeStripe struct{}

func (fakeStripe) CreateCheckout(_ context.Context, p billing.CheckoutParams) (string, error) {
	return "https://checkout.example/" + p.UserID + "/" + p.TierID + "?email=" + p.Email, nil
}

func (fakeStripe) CreatePortal(_ context.Context, customerID, _ string) (string, error) {
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
	if _, err := st.GrantByExternal(context.Background(), models.SourceStripePurchase, "pi_1"); err == nil {
		t.Error("a grant was created from an unverified request")
	}
}

func TestWebhookAcceptsSignedEventAndCreatesGrant(t *testing.T) {
	r, st := newRouter(t)
	payload := fixture(t, "checkout_payment.json")
	mustCode(t, postWebhook(r, payload, signed(payload, webhookSecret)), http.StatusOK)
	g, err := st.GrantByExternal(context.Background(), models.SourceStripePurchase, "pi_1")
	if err != nil || g.UserID != "user-1" {
		t.Fatalf("grant = %+v err = %v", g, err)
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
