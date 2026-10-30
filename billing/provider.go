package billing

import (
	"errors"
	"strings"

	"github.com/stripe/stripe-go/v87"
	"github.com/sweetrpg/common.go/logging"
)

// ProviderError is a failed call to the payment provider (Stripe): an API error, or a transport
// failure such as a timeout. It carries only the fields safe to log and none of Stripe's payload,
// which can hold payment objects. Handlers map it to a 502 with PublicMessage; the cause stays in
// the log.
type ProviderError struct {
	Op        string // checkout, portal, or plan switch
	Type      string // Stripe error type, empty for a transport failure
	Code      string // Stripe error code, e.g. resource_missing
	Param     string
	Message   string // Stripe's message, for the log only
	RequestID string
	Status    int // HTTP status Stripe answered with, 0 for a transport failure
	API       bool
	cause     error
}

func newProviderError(op string, err error) *ProviderError {
	pe := &ProviderError{Op: op, cause: err}
	var se *stripe.Error
	if errors.As(err, &se) {
		pe.API = true
		pe.Type, pe.Code, pe.Param = string(se.Type), string(se.Code), se.Param
		pe.Message, pe.RequestID, pe.Status = se.Msg, se.RequestID, se.HTTPStatusCode
	}
	return pe
}

func (e *ProviderError) Error() string {
	detail := "unreachable"
	if e.API {
		detail = e.Type + " " + e.Code
	}
	return "payment provider " + e.Op + " failed: " + strings.TrimSpace(detail)
}

// Unwrap exposes the underlying error to errors.Is and errors.As.
func (e *ProviderError) Unwrap() error { return e.cause }

// PublicMessage is short text safe to show a caller. A missing price is called out because it is
// a catalog mistake an administrator can fix; everything else stays generic.
func (e *ProviderError) PublicMessage() string {
	switch {
	case !e.API:
		return "the payment provider could not be reached"
	case e.Code == string(stripe.ErrorCodeResourceMissing) && strings.Contains(strings.ToLower(e.Message), "price"):
		return "the product's price is not valid"
	}
	return "the payment provider rejected the request"
}

// providerFailed logs why a provider call failed, with the caller's identifiers, and returns the
// typed error. Only chosen fields are logged: never the key, the secret, or Stripe's full error.
func providerFailed(op string, err error, fields ...any) error {
	pe := newProviderError(op, err)
	args := append([]any{"op", op}, fields...)
	args = append(args, "stripe_type", pe.Type, "stripe_code", pe.Code, "stripe_param", pe.Param,
		"stripe_status", pe.Status, "stripe_request_id", pe.RequestID, "stripe_message", pe.Message)
	if !pe.API {
		args = append(args, "cause", err.Error())
	}
	logging.Logger.Error("payment provider call failed", args...)
	return pe
}

// paymentFailureCodes are Stripe error codes that mean the customer's payment did not go through
// (as opposed to a mistake in the request): a decline, or a card that needs authentication, which
// an off-session charge cannot get.
var paymentFailureCodes = map[string]bool{
	"card_declined":                               true,
	"expired_card":                                true,
	"incorrect_cvc":                               true,
	"insufficient_funds":                          true,
	"authentication_required":                     true,
	"payment_intent_authentication_failure":       true,
	"subscription_payment_intent_requires_action": true,
	"payment_method_provider_decline":             true,
	"processing_error":                            true,
}

// PaymentFailed reports whether the provider refused the charge itself.
func (e *ProviderError) PaymentFailed() bool {
	return e.API && (e.Type == string(stripe.ErrorTypeCard) || paymentFailureCodes[e.Code])
}

// PaymentFailedError means a plan change that had to be paid for now was not paid, and so was not
// made: the subscription and the grants are unchanged.
type PaymentFailedError struct{ cause *ProviderError }

func (e *PaymentFailedError) Error() string { return "payment failed; the plan was not changed" }

// Unwrap exposes the provider error to errors.As.
func (e *PaymentFailedError) Unwrap() error { return e.cause }

// PublicMessage is safe to show a caller.
func (e *PaymentFailedError) PublicMessage() string {
	return "the payment could not be completed, so the plan was not changed"
}

// alreadyCanceled reports whether a cancel failed only because the subscription is already gone
// or already canceled, which is the state the cancel was after.
func alreadyCanceled(err error) bool {
	var se *stripe.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code == stripe.ErrorCodeResourceMissing ||
		(se.Type == stripe.ErrorTypeInvalidRequest && strings.Contains(strings.ToLower(se.Msg), "canceled subscription"))
}
