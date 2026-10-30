package billing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v87"

	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

// subscribed sets up a paid-up subscription: checkout, then the first invoice (in_1, paid with
// charge ch_inv_1) and a later plan-change proration invoice (in_up, charge ch_up).
func subscribed(t *testing.T) (*Service, *data.MemoryStore, *fakeGateway) {
	t.Helper()
	s, st, gw := setup(t)
	gw.invoices = map[string]InvoiceInfo{
		"in_1":  {ID: "in_1", BillingReason: "subscription_create"},
		"in_up": {ID: "in_up", BillingReason: "subscription_update"},
		"in_3":  {ID: "in_3", BillingReason: "subscription_cycle"},
	}
	apply(t, s, "checkout_subscription.json", "invoice_paid.json", "invoice_paid_proration.json")
	return s, st, gw
}

func subGrant(t *testing.T, st *data.MemoryStore) models.Grant {
	t.Helper()
	return *grant(t, st, models.SourceStripeSubscription, "sub_1")
}

// withID replays an event under a new event ID, the way Stripe redelivers after an outage.
func withID(t *testing.T, name, id string) Event {
	t.Helper()
	ev := loadEvent(t, name)
	ev.ID = id
	return ev
}

func TestFullRefundOfTheFirstPaymentRevokesAndCancelsTheSubscription(t *testing.T) {
	s, st, gw := subscribed(t)
	apply(t, s, "charge_refunded_subscription.json")
	if g := subGrant(t, st); g.Status != models.GrantRevoked {
		t.Errorf("status = %s, want revoked", g.Status)
	}
	if len(gw.canceled) != 1 || gw.canceled[0] != "sub_1" {
		t.Errorf("canceled = %v, want sub_1 once", gw.canceled)
	}
}

func TestFullRefundOfARenewalPaymentAlsoCancels(t *testing.T) {
	s, st, gw := subscribed(t)
	renewal := loadEvent(t, "charge_refunded_subscription.json")
	renewal.ID = "evt_ren"
	renewal.Object = json.RawMessage(`{"id":"ch_inv_1","payment_intent":"pi_inv_1","invoice":"in_3","refunded":true}`)
	if err := s.HandleEvent(context.Background(), renewal); err != nil {
		t.Fatal(err)
	}
	if g := subGrant(t, st); g.Status != models.GrantRevoked || len(gw.canceled) != 1 {
		t.Errorf("status = %s, canceled = %v", g.Status, gw.canceled)
	}
}

func TestRefundOfAPlanChangeChargeKeepsTheGrantAndTheSubscription(t *testing.T) {
	s, st, gw := subscribed(t)
	logs := captureLogs(t)
	apply(t, s, "charge_refunded_proration.json")
	if g := subGrant(t, st); g.Status != models.GrantActive {
		t.Errorf("status = %s, want active: a proration refund is not a refund of the plan", g.Status)
	}
	if len(gw.canceled) != 0 {
		t.Errorf("canceled = %v, want none", gw.canceled)
	}
	out := strings.ToLower(logs.String())
	if !strings.Contains(out, "level=info") || !strings.Contains(out, "subscription_update") {
		t.Errorf("not logged at INFO with the reason:\n%s", logs.String())
	}
}

func TestRefundWithNoInvoiceReferenceDoesNothingAndWarns(t *testing.T) {
	s, st, gw := subscribed(t)
	logs := captureLogs(t)
	apply(t, s, "charge_refunded_no_invoice.json")
	if g := subGrant(t, st); g.Status != models.GrantActive || len(gw.canceled) != 0 {
		t.Errorf("status = %s, canceled = %v", g.Status, gw.canceled)
	}
	if !strings.Contains(strings.ToLower(logs.String()), "level=warn") {
		t.Errorf("not logged at WARN:\n%s", logs.String())
	}
}

func TestRefundWhoseInvoiceCannotBeFoundDoesNothingAndWarns(t *testing.T) {
	s, st, gw := subscribed(t)
	logs := captureLogs(t)
	gw.invoiceErr = &stripe.Error{Code: stripe.ErrorCodeResourceMissing, Msg: "No such invoice"}
	apply(t, s, "charge_refunded_subscription.json")
	if g := subGrant(t, st); g.Status != models.GrantActive || len(gw.canceled) != 0 {
		t.Errorf("status = %s, canceled = %v", g.Status, gw.canceled)
	}
	if !strings.Contains(strings.ToLower(logs.String()), "level=warn") {
		t.Errorf("not logged at WARN:\n%s", logs.String())
	}
}

func TestTransientInvoiceLookupFailureIsRetried(t *testing.T) {
	s, st, gw := subscribed(t)
	gw.invoiceErr = errors.New("stripe timeout")
	if err := s.HandleEvent(context.Background(), loadEvent(t, "charge_refunded_subscription.json")); err == nil {
		t.Fatal("a failed invoice lookup must return an error so Stripe redelivers")
	}
	if done, _ := st.EventProcessed(context.Background(), "evt_ch_ref_sub"); done {
		t.Error("the failed event was recorded")
	}
	gw.invoiceErr = nil
	apply(t, s, "charge_refunded_subscription.json")
	if g := subGrant(t, st); g.Status != models.GrantRevoked || len(gw.canceled) != 1 {
		t.Errorf("after retry: status = %s, canceled = %v", g.Status, gw.canceled)
	}
}

func TestDisputeAlwaysRevokesAndCancels(t *testing.T) {
	for _, dispute := range []string{"dispute_subscription.json", "dispute_proration.json"} {
		t.Run(dispute, func(t *testing.T) {
			s, st, gw := subscribed(t)
			apply(t, s, dispute)
			if g := subGrant(t, st); g.Status != models.GrantRevoked {
				t.Errorf("status = %s, want revoked", g.Status)
			}
			if len(gw.canceled) != 1 || gw.canceled[0] != "sub_1" {
				t.Errorf("canceled = %v", gw.canceled)
			}
		})
	}
}

func TestCancelFailureReturnsAnErrorAndIsRetriedOnRedelivery(t *testing.T) {
	s, st, gw := subscribed(t)
	gw.cancelErr, gw.cancelFail = &stripe.Error{Type: stripe.ErrorTypeAPI, Code: "lock_timeout", Msg: "busy"}, 1

	err := s.HandleEvent(context.Background(), loadEvent(t, "charge_refunded_subscription.json"))
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Op != "cancel subscription" {
		t.Fatalf("err = %v, want a ProviderError for the cancel", err)
	}
	if g := subGrant(t, st); g.Status != models.GrantRevoked {
		t.Errorf("entitlements stay revoked while the cancel is pending: %s", g.Status)
	}
	if done, _ := st.EventProcessed(context.Background(), "evt_ch_ref_sub"); done {
		t.Fatal("the failed event was recorded, so Stripe's redelivery would be skipped")
	}

	if err := s.HandleEvent(context.Background(), loadEvent(t, "charge_refunded_subscription.json")); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	if len(gw.canceled) != 2 {
		t.Errorf("canceled = %v, want the cancel retried", gw.canceled)
	}
	if done, _ := st.EventProcessed(context.Background(), "evt_ch_ref_sub"); !done {
		t.Error("the succeeded event was not recorded")
	}
}

func TestAlreadyCanceledOrMissingSubscriptionCountsAsSuccess(t *testing.T) {
	for name, cancelErr := range map[string]error{
		"missing":  &stripe.Error{Type: stripe.ErrorTypeInvalidRequest, Code: stripe.ErrorCodeResourceMissing, Msg: "No such subscription: 'sub_1'"},
		"canceled": &stripe.Error{Type: stripe.ErrorTypeInvalidRequest, Msg: "A canceled subscription can only update its cancellation_details."},
	} {
		t.Run(name, func(t *testing.T) {
			s, st, gw := subscribed(t)
			gw.cancelErr, gw.cancelFail = cancelErr, -1
			apply(t, s, "charge_refunded_subscription.json")
			if g := subGrant(t, st); g.Status != models.GrantRevoked {
				t.Errorf("status = %s", g.Status)
			}
			if done, _ := st.EventProcessed(context.Background(), "evt_ch_ref_sub"); !done {
				t.Error("event not recorded")
			}
		})
	}
}

func TestRepeatedDeliveriesAreNoOps(t *testing.T) {
	s, st, gw := subscribed(t)
	apply(t, s, "charge_refunded_subscription.json", "charge_refunded_subscription.json") // same event ID
	if len(gw.canceled) != 1 {
		t.Errorf("a replayed event ID cancelled %d times, want 1", len(gw.canceled))
	}
	// A different event for the same refund (Stripe resends under a new ID after a long outage).
	if err := s.HandleEvent(context.Background(), withID(t, "charge_refunded_subscription.json", "evt_again")); err != nil {
		t.Fatal(err)
	}
	if err := s.HandleEvent(context.Background(), withID(t, "dispute_subscription.json", "evt_dp_again")); err != nil {
		t.Fatal(err)
	}
	if g := subGrant(t, st); g.Status != models.GrantRevoked {
		t.Errorf("status = %s", g.Status)
	}
}

func TestOneOffRefundsAndDisputesCancelNothing(t *testing.T) {
	for _, ev := range []string{"charge_refunded_purchase.json", "dispute_created.json"} {
		t.Run(ev, func(t *testing.T) {
			s, st, gw := setup(t)
			apply(t, s, "checkout_payment.json", ev)
			if g := grant(t, st, models.SourceStripePurchase, "pi_1"); g.Status != models.GrantRevoked {
				t.Errorf("status = %s", g.Status)
			}
			if len(gw.canceled) != 0 {
				t.Errorf("canceled = %v, want none for a one-off purchase", gw.canceled)
			}
		})
	}
}

func TestPartialRefundCancelsNothing(t *testing.T) {
	s, st, gw := subscribed(t)
	partial := loadEvent(t, "charge_partially_refunded.json")
	partial.Object = json.RawMessage(`{"id":"ch_inv_1","payment_intent":"pi_inv_1","invoice":"in_1","refunded":false}`)
	if err := s.HandleEvent(context.Background(), partial); err != nil {
		t.Fatal(err)
	}
	if g := subGrant(t, st); g.Status != models.GrantActive || len(gw.canceled) != 0 {
		t.Errorf("status = %s, canceled = %v", g.Status, gw.canceled)
	}
}

func TestABundleSubscriptionIsCanceledOnceNotPerGrant(t *testing.T) {
	s, cat, st, gw := productSetup(t)
	seedMembersProducts(t, cat)
	_, err := cat.CreateProduct(context.Background(), "admin", productInputBundleSub())
	if err != nil {
		t.Fatal(err)
	}
	ev := loadEvent(t, "product_subscription_checkout.json")
	ev.ID = "evt_bundle_sub"
	ev.Object = json.RawMessage(`{"id":"cs_bs","mode":"subscription","payment_status":"paid","customer":"cus_b","subscription":"sub_bs","client_reference_id":"user-9","metadata":{"user_id":"user-9","product_id":"bundle-sub","quantity":"1"}}`)
	if err := s.HandleEvent(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if n := len(grantsOf(t, st, models.SourceStripeSubscription, "sub_bs")); n != 2 {
		t.Fatalf("grants = %d, want 2", n)
	}
	refund := loadEvent(t, "charge_refunded_subscription.json")
	refund.ID = "evt_bs_ref"
	refund.Object = json.RawMessage(`{"id":"ch_bs","payment_intent":"pi_bs","invoice":"in_bs","refunded":true}`)
	// Link the refund to the grants the way invoice.paid would have.
	gs, _ := st.GrantsByExternal(context.Background(), models.SourceStripeSubscription, "sub_bs")
	for i := range gs {
		gs[i].Refs = append(gs[i].Refs, "in_bs")
		_ = st.UpdateGrant(context.Background(), &gs[i], "system")
	}
	if err := s.HandleEvent(context.Background(), refund); err != nil {
		t.Fatal(err)
	}
	for k, g := range grantsOf(t, st, models.SourceStripeSubscription, "sub_bs") {
		if g.Status != models.GrantRevoked {
			t.Errorf("%s status = %s", k, g.Status)
		}
	}
	if len(gw.canceled) != 1 || gw.canceled[0] != "sub_bs" {
		t.Errorf("canceled = %v, want sub_bs once", gw.canceled)
	}
}

func productInputBundleSub() catalog.ProductInput {
	return catalog.ProductInput{Slug: "bundle-sub", Name: "Bundle sub", Billing: models.BillingRecurring, StripePriceID: "price_bs",
		Entitlements: map[string]any{"initiative.packaged_themes": true, "initiative.customization": true}}
}
