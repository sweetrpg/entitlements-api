package billing

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sweetrpg/common.go/logging"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

// Fixtures in testdata are hand-written to Stripe's documented event shapes (no live Stripe
// calls, nothing captured from an account).

func loadEvent(t *testing.T, name string) Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	return Event{ID: env.ID, Type: env.Type, Object: env.Data.Object}
}

type fakeGateway struct {
	checkout   CheckoutParams
	portalCust string
	portalBack string
}

func (f *fakeGateway) CreateCheckout(_ context.Context, p CheckoutParams) (string, error) {
	f.checkout = p
	return "https://checkout.example/session", nil
}

func (f *fakeGateway) CreatePortal(_ context.Context, customerID, returnURL string) (string, error) {
	f.portalCust, f.portalBack = customerID, returnURL
	return "https://portal.example/session", nil
}

var urls = URLs{CheckoutSuccess: "https://app.test/ok", CheckoutCancel: "https://app.test/cancel", PortalReturn: "https://app.test/account"}

func setup(t *testing.T) (*Service, *data.MemoryStore, *fakeGateway) {
	t.Helper()
	logging.Init()
	st := data.NewMemoryStore()
	gw := &fakeGateway{}
	return New(st, gw, catalog.New(st), urls), st, gw
}

func apply(t *testing.T, s *Service, files ...string) {
	t.Helper()
	for _, f := range files {
		if err := s.HandleEvent(context.Background(), loadEvent(t, f)); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
}

func grant(t *testing.T, st *data.MemoryStore, source, ext string) *models.Grant {
	t.Helper()
	gs, err := st.GrantsByExternal(context.Background(), source, ext)
	if err != nil || len(gs) == 0 {
		t.Fatalf("grant %s/%s: %v (found %d)", source, ext, err, len(gs))
	}
	return &gs[0]
}

func activeCount(t *testing.T, st *data.MemoryStore, user string) int {
	t.Helper()
	gs, err := st.ActiveGrants(context.Background(), user, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return len(gs)
}

func TestSubscriptionCheckoutCreatesGrantAndRemembersCustomer(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_subscription.json")

	g := grant(t, st, models.SourceStripeSubscription, "sub_1")
	if g.UserID != "user-1" || g.TierID != "plus" || g.Status != models.GrantActive {
		t.Errorf("grant = %+v", g)
	}
	if c, _ := st.StripeCustomer(context.Background(), "user-1"); c != "cus_1" {
		t.Errorf("customer = %q", c)
	}
}

func TestPurchaseCheckoutCreatesGrantKeyedOnPaymentIntent(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_payment.json")

	g := grant(t, st, models.SourceStripePurchase, "pi_1")
	if g.TierID != "deluxe" || g.ValidUntil != nil {
		t.Errorf("grant = %+v", g)
	}
}

func TestUnpaidCheckoutCreatesNothing(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_unpaid.json")
	if gs, _ := st.GrantsByExternal(context.Background(), models.SourceStripePurchase, "pi_2"); len(gs) != 0 {
		t.Errorf("grants = %v, want none", gs)
	}
}

func TestInvoicePaidSetsAndExtendsValidUntil(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_subscription.json", "invoice_paid.json")
	g := grant(t, st, models.SourceStripeSubscription, "sub_1")
	if g.ValidUntil == nil || g.ValidUntil.Unix() != 1792592000 {
		t.Fatalf("valid_until = %v", g.ValidUntil)
	}
	for _, ref := range []string{"cs_sub_1", "in_1", "pi_inv_1", "ch_inv_1"} {
		if len(mustRefs(t, st, ref)) != 1 {
			t.Errorf("ref %s does not resolve to the grant", ref)
		}
	}

	apply(t, s, "invoice_renewed.json")
	if g = grant(t, st, models.SourceStripeSubscription, "sub_1"); g.ValidUntil.Unix() != 1795184000 {
		t.Errorf("after renewal valid_until = %v", g.ValidUntil)
	}
	apply(t, s, "invoice_older.json")
	if g = grant(t, st, models.SourceStripeSubscription, "sub_1"); g.ValidUntil.Unix() != 1795184000 {
		t.Errorf("an older invoice shortened valid_until to %v", g.ValidUntil)
	}
}

func mustRefs(t *testing.T, st *data.MemoryStore, ref string) []models.Grant {
	t.Helper()
	gs, err := st.GrantsByRef(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	return gs
}

func TestInvoiceBeforeCheckoutCreatesGrantFromParentMetadata(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "invoice_paid_parent.json")
	g := grant(t, st, models.SourceStripeSubscription, "sub_2")
	if g.UserID != "user-2" || g.TierID != "plus" || g.ValidUntil.Unix() != 1795000000 {
		t.Errorf("grant = %+v", g)
	}
}

func TestInvoiceWithoutSubscriptionCreatesNothing(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "invoice_no_subscription.json")
	if gs := mustRefs(t, st, "in_9"); len(gs) != 0 {
		t.Errorf("grants = %v", gs)
	}
}

func TestGrantWhosePeriodAlreadyEndedDoesNotCount(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "invoice_older.json")
	grant(t, st, models.SourceStripeSubscription, "sub_1")
	if activeCount(t, st, "user-1") != 0 {
		t.Error("expired grant counted as active")
	}
}

func TestEventWithoutUserCreatesNoGrant(t *testing.T) {
	s, st, _ := setup(t)
	ev := loadEvent(t, "checkout_payment.json")
	ev.Object = json.RawMessage(`{"id":"cs_x","mode":"payment","payment_status":"paid","payment_intent":"pi_x","metadata":{}}`)
	if err := s.HandleEvent(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if gs, _ := st.GrantsByExternal(context.Background(), models.SourceStripePurchase, "pi_x"); len(gs) != 0 {
		t.Errorf("grants = %v, want none", gs)
	}
}

func TestCancelAndLapseEndTheGrant(t *testing.T) {
	for _, ending := range []string{"subscription_deleted.json", "subscription_unpaid.json"} {
		t.Run(ending, func(t *testing.T) {
			s, st, _ := setup(t)
			apply(t, s, "checkout_subscription.json", "invoice_paid.json")
			apply(t, s, ending)
			if g := grant(t, st, models.SourceStripeSubscription, "sub_1"); g.Status != models.GrantEnded {
				t.Errorf("status = %s", g.Status)
			}
		})
	}
}

func TestPastDueKeepsTheGrantWhileStripeRetries(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_subscription.json", "subscription_past_due.json")
	if g := grant(t, st, models.SourceStripeSubscription, "sub_1"); g.Status != models.GrantActive {
		t.Errorf("status = %s", g.Status)
	}
}

func TestRenewalAfterLapseReactivates(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_subscription.json", "subscription_deleted.json", "invoice_renewed.json")
	if g := grant(t, st, models.SourceStripeSubscription, "sub_1"); g.Status != models.GrantActive {
		t.Errorf("status = %s", g.Status)
	}
}

func TestFullRefundRevokesPurchase(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_payment.json", "charge_refunded_purchase.json")
	if g := grant(t, st, models.SourceStripePurchase, "pi_1"); g.Status != models.GrantRevoked {
		t.Errorf("status = %s", g.Status)
	}
	if activeCount(t, st, "user-1") != 0 {
		t.Error("revoked grant still counts")
	}
}

func TestRefundOfSubscriptionPaymentRevokesAndStaysRevoked(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_subscription.json", "invoice_paid.json", "charge_refunded_subscription.json")
	if g := grant(t, st, models.SourceStripeSubscription, "sub_1"); g.Status != models.GrantRevoked {
		t.Fatalf("status = %s", g.Status)
	}
	apply(t, s, "invoice_renewed.json", "subscription_deleted.json")
	if g := grant(t, st, models.SourceStripeSubscription, "sub_1"); g.Status != models.GrantRevoked {
		t.Errorf("later events changed a revoked grant to %s", g.Status)
	}
}

func TestPartialRefundLeavesGrantAlone(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_payment.json", "charge_partially_refunded.json")
	if g := grant(t, st, models.SourceStripePurchase, "pi_1"); g.Status != models.GrantActive {
		t.Errorf("status = %s", g.Status)
	}
}

func TestDisputeRevokesGrant(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_payment.json", "dispute_created.json")
	if g := grant(t, st, models.SourceStripePurchase, "pi_1"); g.Status != models.GrantRevoked {
		t.Errorf("status = %s", g.Status)
	}
}

func TestReplayedEventChangesNothingMore(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "checkout_payment.json")
	before := grant(t, st, models.SourceStripePurchase, "pi_1")
	apply(t, s, "checkout_payment.json", "checkout_payment.json")
	after := grant(t, st, models.SourceStripePurchase, "pi_1")
	if !after.UpdatedAt.Equal(before.UpdatedAt) || activeCount(t, st, "user-1") != 1 {
		t.Errorf("replay modified state: before %+v after %+v", before, after)
	}
	if done, _ := st.EventProcessed(context.Background(), "evt_co_pay"); !done {
		t.Error("event id not recorded")
	}
}

type failingInsert struct{ data.Store }

func (failingInsert) InsertGrant(context.Context, *models.Grant, string) error {
	return errors.New("db down")
}

func TestFailedEventIsNotRecordedSoStripeCanRetry(t *testing.T) {
	logging.Init()
	st := data.NewMemoryStore()
	broken := New(failingInsert{st}, &fakeGateway{}, catalog.New(st), urls)
	if err := broken.HandleEvent(context.Background(), loadEvent(t, "checkout_payment.json")); err == nil {
		t.Fatal("expected error")
	}
	if done, _ := st.EventProcessed(context.Background(), "evt_co_pay"); done {
		t.Fatal("failed event was recorded")
	}
	healthy := New(st, &fakeGateway{}, catalog.New(st), urls)
	apply(t, healthy, "checkout_payment.json")
	grant(t, st, models.SourceStripePurchase, "pi_1")
}

func TestUnhandledEventIsIgnoredAndNotRecorded(t *testing.T) {
	s, st, _ := setup(t)
	apply(t, s, "unhandled.json")
	if done, _ := st.EventProcessed(context.Background(), "evt_other"); done {
		t.Error("unhandled event recorded")
	}
}

func seedTier(t *testing.T, st *data.MemoryStore, slug, price string) {
	t.Helper()
	if err := st.InsertTier(context.Background(), &models.Tier{Slug: slug, Name: slug, StripePriceID: price}, "admin"); err != nil {
		t.Fatal(err)
	}
}

func TestCheckoutRejectsUnmappedOrMissingTier(t *testing.T) {
	s, st, _ := setup(t)
	seedTier(t, st, "free", "")
	for _, tier := range []string{"free", "ghost"} {
		if _, err := s.Checkout(context.Background(), "user-1", "", CheckoutRequest{TierID: tier}); !errors.Is(err, models.ErrInvalid) {
			t.Errorf("tier %s: err = %v, want invalid", tier, err)
		}
	}
}

func TestCheckoutPassesUserTierPriceAndFixedURLs(t *testing.T) {
	s, st, gw := setup(t)
	seedTier(t, st, "plus", "price_plus")
	url, err := s.Checkout(context.Background(), "user-1", "a@example.test", CheckoutRequest{TierID: "plus"})
	if err != nil || url == "" {
		t.Fatalf("url = %q err = %v", url, err)
	}
	want := CheckoutParams{PriceID: "price_plus", UserID: "user-1", TierID: "plus", Email: "a@example.test", SuccessURL: urls.CheckoutSuccess, CancelURL: urls.CheckoutCancel}
	if gw.checkout.PriceID != want.PriceID || gw.checkout.UserID != want.UserID || gw.checkout.TierID != want.TierID ||
		gw.checkout.Email != want.Email || gw.checkout.SuccessURL != want.SuccessURL || gw.checkout.CancelURL != want.CancelURL || gw.checkout.CustomerID != "" {
		t.Errorf("params = %+v", gw.checkout)
	}
}

func TestCheckoutReusesKnownStripeCustomer(t *testing.T) {
	s, st, gw := setup(t)
	seedTier(t, st, "plus", "price_plus")
	apply(t, s, "checkout_subscription.json")
	if _, err := s.Checkout(context.Background(), "user-1", "", CheckoutRequest{TierID: "plus"}); err != nil {
		t.Fatal(err)
	}
	if gw.checkout.CustomerID != "cus_1" {
		t.Errorf("customer = %q", gw.checkout.CustomerID)
	}
}

func TestPortalNeedsAnExistingCustomer(t *testing.T) {
	s, _, gw := setup(t)
	if _, err := s.Portal(context.Background(), "user-1"); !errors.Is(err, data.ErrNotFound) {
		t.Fatalf("err = %v, want not found", err)
	}
	apply(t, s, "checkout_subscription.json")
	if _, err := s.Portal(context.Background(), "user-1"); err != nil {
		t.Fatal(err)
	}
	if gw.portalCust != "cus_1" || gw.portalBack != urls.PortalReturn {
		t.Errorf("portal = %q %q", gw.portalCust, gw.portalBack)
	}
}
