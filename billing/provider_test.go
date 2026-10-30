package billing

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stripe/stripe-go/v87"
	"github.com/sweetrpg/common.go/logging"
	"github.com/zerodha/logf"

	"github.com/sweetrpg/entitlements-api/models"
)

// captureLogs replaces the logger with one writing to a buffer.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	old := logging.Logger
	var buf bytes.Buffer
	logging.Logger = logf.New(logf.Opts{Writer: &buf, Level: logf.DebugLevel})
	t.Cleanup(func() { logging.Logger = old })
	return &buf
}

func noSuchPrice() error {
	return fmt.Errorf("retrieve price: %w", &stripe.Error{
		Type: stripe.ErrorTypeInvalidRequest, Code: stripe.ErrorCodeResourceMissing, Param: "id",
		Msg: "No such price: 'members_10'", HTTPStatusCode: 404, RequestID: "req_abc123",
	})
}

func TestCheckoutTurnsAStripeErrorIntoATypedProviderErrorAndLogsTheCause(t *testing.T) {
	s, cat, _, gw := productSetup(t)
	seedMembersProducts(t, cat)
	logs := captureLogs(t) // after setup, which re-initializes the logger
	gw.err = noSuchPrice()

	_, err := s.Checkout(context.Background(), "user-3", "", CheckoutRequest{ProductID: "members-10"})
	var pe *ProviderError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a ProviderError", err)
	}
	if pe.Code != "resource_missing" || pe.RequestID != "req_abc123" || pe.Status != 404 || pe.Type != "invalid_request_error" {
		t.Errorf("provider error = %+v", pe)
	}
	if pe.PublicMessage() != "the product's price is not valid" {
		t.Errorf("public message = %q", pe.PublicMessage())
	}

	out := logs.String()
	if !strings.Contains(strings.ToLower(out), "level=error") {
		t.Errorf("not logged at ERROR:\n%s", out)
	}
	for _, want := range []string{"members-10", "resource_missing", "req_abc123", "invalid_request_error", "No such price", "checkout"} {
		if !strings.Contains(out, want) {
			t.Errorf("log missing %q:\n%s", want, out)
		}
	}
}

func TestPublicMessageNeverCarriesStripeDetails(t *testing.T) {
	for name, err := range map[string]error{
		"price missing": noSuchPrice(),
		"card":          &stripe.Error{Type: stripe.ErrorTypeCard, Code: stripe.ErrorCodeCardDeclined, Msg: "Your card was declined", RequestID: "req_secretish"},
		"other missing": &stripe.Error{Code: stripe.ErrorCodeResourceMissing, Msg: "No such customer: 'cus_9'"},
		"transport":     errors.New("dial tcp 10.0.0.1:443: i/o timeout"),
	} {
		pe := newProviderError("checkout", err)
		msg := pe.PublicMessage()
		for _, leak := range []string{"req_", "cus_9", "members_10", "10.0.0.1", "card"} {
			if strings.Contains(msg, leak) {
				t.Errorf("%s: public message %q leaks %q", name, msg, leak)
			}
		}
	}
	if m := newProviderError("checkout", errors.New("timeout")).PublicMessage(); m != "the payment provider could not be reached" {
		t.Errorf("transport message = %q", m)
	}
	if m := newProviderError("checkout", &stripe.Error{Code: stripe.ErrorCodeCardDeclined}).PublicMessage(); m != "the payment provider rejected the request" {
		t.Errorf("generic message = %q", m)
	}
	if m := newProviderError("checkout", &stripe.Error{Code: stripe.ErrorCodeResourceMissing, Msg: "No such customer"}).PublicMessage(); m != "the payment provider rejected the request" {
		t.Errorf("non-price missing message = %q", m)
	}
}

func TestPortalAndPlanSwitchErrorsAreTypedToo(t *testing.T) {
	s, _, st, gw := planSetup(t)
	subscribe(t, s, "user-6", "sub_m1", "members-10")
	logs := captureLogs(t)

	gw.err = errors.New("boom")
	var pe *ProviderError
	if _, err := s.Portal(context.Background(), "user-6"); !errors.As(err, &pe) || pe.Op != "portal" {
		t.Errorf("portal err = %v", err)
	}
	if _, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true}); !errors.As(err, &pe) || pe.Op != "plan switch" {
		t.Errorf("switch err = %v", err)
	}
	if g := grantsOf(t, st, models.SourceStripeSubscription, "sub_m1")[membersKey]; g.Value != 10 || g.ProductID != "members-10" {
		t.Errorf("a failed switch changed the grant: %+v", g)
	}
	for _, want := range []string{"portal", "plan switch", "members-20", "sub_m1"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("log missing %q:\n%s", want, logs.String())
		}
	}
}

func TestLoggedErrorHasNoSecretsOrWholeStripeError(t *testing.T) {
	s, cat, _, gw := productSetup(t)
	seedMembersProducts(t, cat)
	logs := captureLogs(t)
	// A stripe.Error serializes whole objects (payment intents, charges); only chosen fields may be logged.
	gw.err = &stripe.Error{Code: stripe.ErrorCodeCardDeclined, Msg: "declined", PaymentIntent: &stripe.PaymentIntent{ID: "pi_leak", ClientSecret: "pi_leak_secret_xyz"}}
	_, _ = s.Checkout(context.Background(), "user-3", "", CheckoutRequest{ProductID: "members-10"})
	if strings.Contains(logs.String(), "pi_leak") || strings.Contains(logs.String(), "secret") {
		t.Errorf("log leaks provider payload:\n%s", logs.String())
	}
}
