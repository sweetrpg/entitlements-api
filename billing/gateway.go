// Package billing connects the entitlements catalog to Stripe: Checkout and billing-portal
// sessions going out, signed webhook events coming in as grants.
package billing

import (
	"context"
	"fmt"
	"strconv"
	"time"

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
	// Price reads a price's amount, currency, and interval.
	Price(ctx context.Context, priceID string) (PriceInfo, error)
	// Subscription reads a subscription's line item, quantity, and period end.
	Subscription(ctx context.Context, subscriptionID string) (SubscriptionInfo, error)
	// Invoice reads why an invoice was created (billing_reason).
	Invoice(ctx context.Context, invoiceID string) (InvoiceInfo, error)
	// CancelSubscription ends a subscription immediately: no further invoices, nothing refunded.
	CancelSubscription(ctx context.Context, subscriptionID string) error
	// PreviewSwitch asks Stripe what the switch in p would invoice, without changing anything.
	PreviewSwitch(ctx context.Context, p SwitchParams) (Preview, error)
	// SwitchPlan moves an existing subscription to another price instead of creating a second
	// subscription, with the proration and payment behavior in p.
	SwitchPlan(ctx context.Context, p SwitchParams) error
}

// PriceInfo is what a switch needs to know about a Stripe price.
type PriceInfo struct {
	ID            string
	UnitAmount    int64 // minor units (cents)
	Currency      string
	Interval      string // day, week, month, or year; empty for a one-off price
	IntervalCount int64
}

// SubscriptionInfo is a subscription's single line item as Stripe holds it.
type SubscriptionInfo struct {
	ID               string
	CustomerID       string
	ItemID           string
	Price            PriceInfo
	Quantity         int64
	CurrentPeriodEnd time.Time
}

// Proration behaviors for a plan switch.
const (
	// ProrateCredit credits the unused part of the old plan to the next invoice.
	ProrateCredit = "create_prorations"
	// ProrateInvoiceNow invoices the proration immediately, so an upgrade is paid for now.
	ProrateInvoiceNow = "always_invoice"
)

// InvoiceInfo is what a refund decision needs to know about an invoice.
type InvoiceInfo struct {
	ID string
	// BillingReason is subscription_create or subscription_cycle for the plan's own payments, and
	// subscription_update for a charge made by changing plan.
	BillingReason string
}

// Preview is Stripe's invoice preview for a proposed switch.
type Preview struct {
	Currency string
	Lines    []PreviewLine
}

// PreviewLine is one line of a preview. Proration marks the lines created by the plan change
// itself (the unused time on the old plan and the remaining time on the new), as opposed to the
// renewal that would follow.
type PreviewLine struct {
	Description string
	Amount      int64 // minor units; negative is a credit
	Proration   bool
}

// SwitchParams is one subscription plan change.
type SwitchParams struct {
	SubscriptionID string
	CustomerID     string
	ItemID         string
	Quantity       int64
	PriceID        string
	ProductID      string
	UserID         string
	// Proration is ProrateCredit or ProrateInvoiceNow.
	Proration string
	// ChargeNow makes a failed payment fail the whole update (Stripe's error_if_incomplete)
	// rather than leaving the subscription changed and unpaid.
	ChargeNow bool
	// ProrationDate pins the moment proration is computed (unix seconds); 0 means now.
	ProrationDate int64
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

// Price reads a price.
func (g *StripeGateway) Price(ctx context.Context, priceID string) (PriceInfo, error) {
	price, err := g.client.V1Prices.Retrieve(ctx, priceID, nil)
	if err != nil {
		return PriceInfo{}, fmt.Errorf("retrieve price: %w", err)
	}
	return priceInfo(price), nil
}

func priceInfo(p *stripe.Price) PriceInfo {
	info := PriceInfo{ID: p.ID, UnitAmount: p.UnitAmount, Currency: string(p.Currency)}
	if p.Recurring != nil {
		info.Interval, info.IntervalCount = string(p.Recurring.Interval), p.Recurring.IntervalCount
	}
	return info
}

// Subscription reads a subscription and its first line item.
func (g *StripeGateway) Subscription(ctx context.Context, subscriptionID string) (SubscriptionInfo, error) {
	sub, err := g.client.V1Subscriptions.Retrieve(ctx, subscriptionID, nil)
	if err != nil {
		return SubscriptionInfo{}, fmt.Errorf("retrieve subscription: %w", err)
	}
	if sub.Items == nil || len(sub.Items.Data) == 0 || sub.Items.Data[0].Price == nil {
		return SubscriptionInfo{}, fmt.Errorf("subscription %s has no priced item", subscriptionID)
	}
	item := sub.Items.Data[0]
	info := SubscriptionInfo{
		ID: sub.ID, ItemID: item.ID, Price: priceInfo(item.Price), Quantity: max(item.Quantity, 1),
		CurrentPeriodEnd: time.Unix(item.CurrentPeriodEnd, 0).UTC(),
	}
	if sub.Customer != nil {
		info.CustomerID = sub.Customer.ID
	}
	return info, nil
}

// Invoice reads an invoice's billing reason.
func (g *StripeGateway) Invoice(ctx context.Context, invoiceID string) (InvoiceInfo, error) {
	inv, err := g.client.V1Invoices.Retrieve(ctx, invoiceID, nil)
	if err != nil {
		return InvoiceInfo{}, fmt.Errorf("retrieve invoice: %w", err)
	}
	return InvoiceInfo{ID: inv.ID, BillingReason: string(inv.BillingReason)}, nil
}

// CancelSubscription cancels now without a final invoice or proration refund; the refund that
// prompted it is left exactly as it is.
func (g *StripeGateway) CancelSubscription(ctx context.Context, subscriptionID string) error {
	_, err := g.client.V1Subscriptions.Cancel(ctx, subscriptionID, &stripe.SubscriptionCancelParams{
		InvoiceNow: stripe.Bool(false), Prorate: stripe.Bool(false),
	})
	if err != nil {
		return fmt.Errorf("cancel subscription: %w", err)
	}
	return nil
}

// PreviewSwitch previews the invoice the switch would produce with the same item change,
// proration behavior, and proration date the real switch would use.
func (g *StripeGateway) PreviewSwitch(ctx context.Context, p SwitchParams) (Preview, error) {
	details := &stripe.InvoiceCreatePreviewSubscriptionDetailsParams{
		Items: []*stripe.InvoiceCreatePreviewSubscriptionDetailsItemParams{{
			ID: stripe.String(p.ItemID), Price: stripe.String(p.PriceID), Quantity: stripe.Int64(max(p.Quantity, 1)),
		}},
		ProrationBehavior: stripe.String(p.Proration),
	}
	if p.ProrationDate != 0 {
		details.ProrationDate = stripe.Int64(p.ProrationDate)
	}
	inv, err := g.client.V1Invoices.CreatePreview(ctx, &stripe.InvoiceCreatePreviewParams{
		Customer:            stripe.String(p.CustomerID),
		Subscription:        stripe.String(p.SubscriptionID),
		SubscriptionDetails: details,
	})
	if err != nil {
		return Preview{}, fmt.Errorf("preview subscription change: %w", err)
	}
	out := Preview{Currency: string(inv.Currency)}
	if inv.Lines != nil {
		for _, l := range inv.Lines.Data {
			pl := PreviewLine{Description: l.Description, Amount: l.Amount}
			if l.Parent != nil && l.Parent.SubscriptionItemDetails != nil {
				pl.Proration = l.Parent.SubscriptionItemDetails.Proration
			}
			out.Lines = append(out.Lines, pl)
		}
	}
	return out, nil
}

// SwitchPlan replaces the subscription item's price, keeping its quantity, and records the new
// product in the subscription's metadata. A charge-now switch fails as a whole if the payment
// does not go through, so Stripe never holds a changed, unpaid subscription.
func (g *StripeGateway) SwitchPlan(ctx context.Context, p SwitchParams) error {
	params := &stripe.SubscriptionUpdateParams{
		Items: []*stripe.SubscriptionUpdateItemParams{{
			ID: stripe.String(p.ItemID), Price: stripe.String(p.PriceID), Quantity: stripe.Int64(max(p.Quantity, 1)),
		}},
		ProrationBehavior: stripe.String(p.Proration),
		Metadata:          map[string]string{"user_id": p.UserID, "product_id": p.ProductID},
	}
	if p.ChargeNow {
		params.PaymentBehavior = stripe.String("error_if_incomplete")
	}
	if p.ProrationDate != 0 {
		params.ProrationDate = stripe.Int64(p.ProrationDate)
	}
	if _, err := g.client.V1Subscriptions.Update(ctx, p.SubscriptionID, params); err != nil {
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
