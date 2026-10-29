// Package billing connects the entitlements catalog to Stripe: Checkout and billing-portal
// sessions going out, signed webhook events coming in as grants.
package billing

import (
	"context"
	"fmt"
	"strconv"

	"github.com/stripe/stripe-go/v87"
)

// CheckoutParams is everything needed to start a Checkout session for one purchasable tier.
type CheckoutParams struct {
	PriceID    string
	UserID     string
	TierID     string
	ProductID  string
	Quantity   int64  // units of the price; 0 means 1
	Email      string // optional; Checkout asks for one when empty
	CustomerID string // optional; reuses the user's existing Stripe customer
	SuccessURL string
	CancelURL  string
}

// Gateway is the slice of Stripe's API this service calls. Tests use a fake; StripeGateway is
// the only implementation that touches the network.
type Gateway interface {
	CreateCheckout(ctx context.Context, p CheckoutParams) (url string, err error)
	CreatePortal(ctx context.Context, customerID, returnURL string) (url string, err error)
	// SwitchPlan moves an existing subscription to another price, prorated by Stripe, instead of
	// creating a second subscription.
	SwitchPlan(ctx context.Context, p SwitchParams) error
}

// SwitchParams names the subscription to change and the product it moves to.
type SwitchParams struct {
	SubscriptionID string
	PriceID        string
	ProductID      string
	UserID         string
}

// StripeGateway calls the live Stripe API. It is a thin translation layer with no rules of its
// own, so it is not unit-tested; the rules live in Service.
type StripeGateway struct{ client *stripe.Client }

// NewStripeGateway builds a gateway authenticated with the secret key.
func NewStripeGateway(secretKey string) *StripeGateway {
	return &StripeGateway{client: stripe.NewClient(secretKey)}
}

// CreateCheckout opens a subscription-mode session for a recurring price and a payment-mode
// session for a one-off price. The user and tier ride along as metadata so webhook events can
// be turned into grants without a lookup.
func (g *StripeGateway) CreateCheckout(ctx context.Context, p CheckoutParams) (string, error) {
	price, err := g.client.V1Prices.Retrieve(ctx, p.PriceID, nil)
	if err != nil {
		return "", fmt.Errorf("retrieve price: %w", err)
	}
	qty := max(p.Quantity, 1)
	meta := map[string]string{"user_id": p.UserID}
	if p.ProductID != "" {
		meta["product_id"] = p.ProductID
		meta["quantity"] = strconv.FormatInt(qty, 10)
	} else {
		meta["tier_id"] = p.TierID
	}
	params := &stripe.CheckoutSessionCreateParams{
		SuccessURL:        stripe.String(p.SuccessURL),
		CancelURL:         stripe.String(p.CancelURL),
		ClientReferenceID: stripe.String(p.UserID),
		Metadata:          meta,
		LineItems:         []*stripe.CheckoutSessionCreateLineItemParams{{Price: stripe.String(p.PriceID), Quantity: stripe.Int64(qty)}},
	}
	if price.Type == stripe.PriceTypeRecurring {
		params.Mode = stripe.String(string(stripe.CheckoutSessionModeSubscription))
		params.SubscriptionData = &stripe.CheckoutSessionCreateSubscriptionDataParams{Metadata: meta}
	} else {
		params.Mode = stripe.String(string(stripe.CheckoutSessionModePayment))
	}
	if p.CustomerID != "" {
		params.Customer = stripe.String(p.CustomerID)
	} else if p.Email != "" {
		params.CustomerEmail = stripe.String(p.Email)
	}
	sess, err := g.client.V1CheckoutSessions.Create(ctx, params)
	if err != nil {
		return "", fmt.Errorf("create checkout session: %w", err)
	}
	return sess.URL, nil
}

// SwitchPlan replaces the subscription's price with the new one, keeping its quantity, and
// records the new product in the subscription's metadata. Stripe prorates the change.
func (g *StripeGateway) SwitchPlan(ctx context.Context, p SwitchParams) error {
	sub, err := g.client.V1Subscriptions.Retrieve(ctx, p.SubscriptionID, nil)
	if err != nil {
		return fmt.Errorf("retrieve subscription: %w", err)
	}
	if sub.Items == nil || len(sub.Items.Data) == 0 {
		return fmt.Errorf("subscription %s has no items", p.SubscriptionID)
	}
	item := sub.Items.Data[0]
	_, err = g.client.V1Subscriptions.Update(ctx, p.SubscriptionID, &stripe.SubscriptionUpdateParams{
		Items: []*stripe.SubscriptionUpdateItemParams{{
			ID: stripe.String(item.ID), Price: stripe.String(p.PriceID), Quantity: stripe.Int64(max(item.Quantity, 1)),
		}},
		ProrationBehavior: stripe.String("create_prorations"),
		Metadata:          map[string]string{"user_id": p.UserID, "product_id": p.ProductID},
	})
	if err != nil {
		return fmt.Errorf("switch subscription: %w", err)
	}
	return nil
}

// CreatePortal opens the Stripe billing portal for a customer.
func (g *StripeGateway) CreatePortal(ctx context.Context, customerID, returnURL string) (string, error) {
	sess, err := g.client.V1BillingPortalSessions.Create(ctx, &stripe.BillingPortalSessionCreateParams{
		Customer:  stripe.String(customerID),
		ReturnURL: stripe.String(returnURL),
	})
	if err != nil {
		return "", fmt.Errorf("create portal session: %w", err)
	}
	return sess.URL, nil
}
