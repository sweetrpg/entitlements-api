package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v87"

	"github.com/sweetrpg/entitlements-api/models"
)

// switchSetup is planSetup with Stripe prices: Members 10 is 10.00 and Members 20 is 20.00 USD
// a month, and the user's subscription is on the one named.
func switchSetup(t *testing.T, onProduct string) (*Service, *fakeGateway, func(string) map[string]models.Grant) {
	t.Helper()
	s, _, st, gw := planSetup(t)
	gw.prices = map[string]PriceInfo{
		"price_members-10": {ID: "price_members-10", UnitAmount: 1000, Currency: "usd", Interval: "month", IntervalCount: 1},
		"price_members-20": {ID: "price_members-20", UnitAmount: 2000, Currency: "usd", Interval: "month", IntervalCount: 1},
	}
	subscribe(t, s, "user-6", "sub_m1", onProduct)
	gw.subs = map[string]SubscriptionInfo{"sub_m1": {
		ID: "sub_m1", CustomerID: "cus_6", ItemID: "si_6", Quantity: 1, Price: gw.prices["price_"+onProduct],
		CurrentPeriodEnd: time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC),
	}}
	return s, gw, func(sub string) map[string]models.Grant { return grantsOf(t, st, models.SourceStripeSubscription, sub) }
}

func TestUpgradeIsInvoicedAndPaidNow(t *testing.T) {
	s, gw, grants := switchSetup(t, "members-10")
	res, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true})
	if err != nil || !res.Switched {
		t.Fatalf("result = %+v err = %v", res, err)
	}
	p := gw.switched
	if p.Proration != ProrateInvoiceNow || !p.ChargeNow {
		t.Errorf("upgrade proration = %q chargeNow = %v, want always_invoice and charge now", p.Proration, p.ChargeNow)
	}
	if p.SubscriptionID != "sub_m1" || p.ItemID != "si_6" || p.CustomerID != "cus_6" || p.Quantity != 1 || p.PriceID != "price_members-20" || p.ProductID != "members-20" {
		t.Errorf("switch params = %+v", p)
	}
	if g := grants("sub_m1")[membersKey]; g.Value != 20 || g.ProductID != "members-20" {
		t.Errorf("grant = %+v", g)
	}
}

func TestDowngradeCreditsTheNextInvoiceInsteadOfChargingNow(t *testing.T) {
	s, gw, grants := switchSetup(t, "members-20")
	if res, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-10", Confirm: true}); err != nil || !res.Switched {
		t.Fatalf("result = %+v err = %v", res, err)
	}
	if gw.switched.Proration != ProrateCredit || gw.switched.ChargeNow {
		t.Errorf("downgrade proration = %q chargeNow = %v, want create_prorations and no charge", gw.switched.Proration, gw.switched.ChargeNow)
	}
	if g := grants("sub_m1")[membersKey]; g.Value != 10 || g.ProductID != "members-10" {
		t.Errorf("grant = %+v", g)
	}
}

func TestSamePriceCountsAsNotAnUpgrade(t *testing.T) {
	s, gw, _ := switchSetup(t, "members-10")
	gw.prices["price_members-20"] = gw.prices["price_members-10"]
	if _, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if gw.switched.Proration != ProrateCredit || gw.switched.ChargeNow {
		t.Errorf("equal-price switch = %+v", gw.switched)
	}
}

func TestSwitchKeepsTheSubscriptionsQuantity(t *testing.T) {
	s, gw, _ := switchSetup(t, "members-10")
	sub := gw.subs["sub_m1"]
	sub.Quantity = 3
	gw.subs["sub_m1"] = sub
	if _, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true}); err != nil {
		t.Fatal(err)
	}
	if gw.switched.Quantity != 3 {
		t.Errorf("quantity = %d, want the subscription's 3", gw.switched.Quantity)
	}
}

func TestFailedUpgradePaymentChangesNothing(t *testing.T) {
	s, gw, grants := switchSetup(t, "members-10")
	gw.switchErr = &stripe.Error{Type: stripe.ErrorTypeCard, Code: stripe.ErrorCodeCardDeclined, Msg: "Your card was declined.", RequestID: "req_decl"}

	res, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true})
	var failed *PaymentFailedError
	if !errors.As(err, &failed) || res.Switched {
		t.Fatalf("result = %+v err = %v, want a PaymentFailedError", res, err)
	}
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Code != "card_declined" {
		t.Errorf("the provider error is not reachable: %v", err)
	}
	if g := grants("sub_m1")[membersKey]; g.Value != 10 || g.ProductID != "members-10" || g.Status != models.GrantActive {
		t.Errorf("a failed payment changed the grant: %+v", g)
	}
	if failed.PublicMessage() != "the payment could not be completed, so the plan was not changed" {
		t.Errorf("message = %q", failed.PublicMessage())
	}
}

func TestACardNeedingAuthenticationIsAPaymentFailureOnUpgrade(t *testing.T) {
	s, gw, _ := switchSetup(t, "members-10")
	gw.switchErr = &stripe.Error{Type: stripe.ErrorTypeInvalidRequest, Code: "subscription_payment_intent_requires_action", Msg: "needs action"}
	var failed *PaymentFailedError
	if _, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true}); !errors.As(err, &failed) {
		t.Errorf("err = %v, want a PaymentFailedError", err)
	}
}

func TestOtherStripeErrorsOnUpgradeStayProviderErrors(t *testing.T) {
	s, gw, grants := switchSetup(t, "members-10")
	gw.switchErr = &stripe.Error{Type: stripe.ErrorTypeAPI, Code: "lock_timeout", Msg: "busy"}
	var failed *PaymentFailedError
	var pe *ProviderError
	_, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true})
	if errors.As(err, &failed) || !errors.As(err, &pe) {
		t.Errorf("err = %v, want a plain ProviderError", err)
	}
	if g := grants("sub_m1")[membersKey]; g.Value != 10 {
		t.Errorf("grant = %+v", g)
	}
}

func TestADowngradeDeclineIsNotCalledAPaymentFailure(t *testing.T) {
	s, gw, grants := switchSetup(t, "members-20")
	gw.switchErr = &stripe.Error{Type: stripe.ErrorTypeCard, Code: stripe.ErrorCodeCardDeclined}
	var failed *PaymentFailedError
	_, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-10", Confirm: true})
	if errors.As(err, &failed) {
		t.Errorf("a downgrade charges nothing, so it cannot be a payment failure: %v", err)
	}
	if g := grants("sub_m1")[membersKey]; g.Value != 20 {
		t.Errorf("grant = %+v", g)
	}
}

func TestSwitchBetweenCurrenciesIsRefusedBeforeStripeIsChanged(t *testing.T) {
	s, gw, _ := switchSetup(t, "members-10")
	p := gw.prices["price_members-20"]
	p.Currency = "eur"
	gw.prices["price_members-20"] = p
	if _, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true}); !errors.Is(err, models.ErrInvalid) {
		t.Errorf("err = %v, want invalid", err)
	}
	if gw.switched.SubscriptionID != "" {
		t.Error("the subscription was changed")
	}
}

func TestUnreadableSubscriptionStopsTheSwitchBeforeAnyChange(t *testing.T) {
	s, gw, grants := switchSetup(t, "members-10")
	gw.readErr = errors.New("stripe down")
	var pe *ProviderError
	if _, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true}); !errors.As(err, &pe) || pe.Op != "plan switch" {
		t.Errorf("err = %v", err)
	}
	if gw.switched.SubscriptionID != "" || grants("sub_m1")[membersKey].Value != 10 {
		t.Error("a failed read changed something")
	}
}
