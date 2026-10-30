package billing

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/models"
)

// Quote actions.
const (
	QuoteCheckout = "checkout" // a new purchase through Stripe Checkout
	QuoteSwitch   = "switch"   // the user's subscription in the plan group moves to the product
	QuoteNone     = "none"     // the user already holds the product; nothing to buy
)

// QuoteProduct names a product in a quote.
type QuoteProduct struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// QuoteLine is one proration line of a switch quote.
type QuoteLine struct {
	Description string `json:"description"`
	Amount      int64  `json:"amount"` // minor units; negative is a credit
}

// Quote tells a user what a purchase or plan change will cost before they commit to it. Amounts
// are in the currency's minor units (cents).
type Quote struct {
	Action         string        `json:"action"`
	CurrentProduct *QuoteProduct `json:"current_product,omitempty"`
	NewProduct     *QuoteProduct `json:"new_product,omitempty"`
	Quantity       int64         `json:"quantity"`
	Currency       string        `json:"currency,omitempty"`
	// AmountDueNow is what is charged when the user goes ahead: price x quantity for a checkout, the
	// prorated difference for an upgrade, and 0 for a downgrade.
	AmountDueNow int64 `json:"amount_due_now"`
	// CreditOnNextInvoice is the credit a downgrade earns against the next invoice.
	CreditOnNextInvoice int64       `json:"credit_on_next_invoice,omitempty"`
	ProrationLines      []QuoteLine `json:"proration_lines,omitempty"`
	// NextBillingDate is when the subscription renews next; empty for a one-off purchase.
	NextBillingDate *time.Time `json:"next_billing_date,omitempty"`
	// ProrationDate pins the proration to the moment of the quote. Send it back with confirm so the
	// charge matches the figure shown; omit it and Stripe prorates at the time of the request.
	ProrationDate int64 `json:"proration_date,omitempty"`
}

// ConfirmationRequiredError means a plan switch needs the user's explicit go-ahead. Nothing at
// Stripe has been changed; Quote is what to show them.
type ConfirmationRequiredError struct{ Quote *Quote }

func (e *ConfirmationRequiredError) Error() string { return "plan change needs confirmation" }

// Quote prices what Checkout would do for the same request, changing nothing. A product the user
// already holds is action "none". Every refusal Checkout makes (unknown product, no price, a
// quantity the product does not allow, over a maximum, nothing to raise) is returned the same way.
func (s *Service) Quote(ctx context.Context, userID string, req CheckoutRequest) (*Quote, error) {
	if req.ProductID == "" || req.TierID != "" {
		return nil, fmt.Errorf("%w: a quote needs a product_id", models.ErrInvalid)
	}
	var params CheckoutParams
	sw, err := s.productParams(ctx, userID, req, &params)
	if errors.Is(err, catalog.ErrInUse) {
		return s.heldQuote(ctx, req.ProductID)
	}
	if err != nil {
		return nil, err
	}
	if sw != nil {
		return s.switchQuote(ctx, userID, sw, params.PriceID, params.Quantity, req.ProrationDate)
	}
	return s.checkoutQuote(ctx, req.ProductID, params)
}

func (s *Service) productRef(ctx context.Context, id string) (models.Product, error) {
	products, err := s.store.GetProducts(ctx, []string{id})
	if err != nil || len(products) == 0 {
		return models.Product{}, fmt.Errorf("%w: product is not available", models.ErrInvalid)
	}
	return products[0], nil
}

func ref(p models.Product) *QuoteProduct { return &QuoteProduct{ID: p.Slug, Name: p.Name} }

func (s *Service) heldQuote(ctx context.Context, productID string) (*Quote, error) {
	p, err := s.productRef(ctx, productID)
	if err != nil {
		return nil, err
	}
	return &Quote{Action: QuoteNone, CurrentProduct: ref(p), NewProduct: ref(p), Quantity: 1}, nil
}

func (s *Service) checkoutQuote(ctx context.Context, productID string, params CheckoutParams) (*Quote, error) {
	p, err := s.productRef(ctx, productID)
	if err != nil {
		return nil, err
	}
	price, err := s.gw.Price(ctx, params.PriceID)
	if err != nil {
		return nil, providerFailed("quote", err, "product", productID, "price_id", params.PriceID)
	}
	qty := max(params.Quantity, 1)
	q := &Quote{
		Action: QuoteCheckout, NewProduct: ref(p), Quantity: qty,
		Currency: price.Currency, AmountDueNow: price.UnitAmount * qty,
	}
	if next, ok := nextPeriod(time.Now().UTC(), price); ok {
		q.NextBillingDate = &next
	}
	return q, nil
}

// nextPeriod is one billing interval after from, for a recurring price.
func nextPeriod(from time.Time, p PriceInfo) (time.Time, bool) {
	n := int(max(p.IntervalCount, 1))
	switch p.Interval {
	case "day":
		return from.AddDate(0, 0, n), true
	case "week":
		return from.AddDate(0, 0, 7*n), true
	case "month":
		return from.AddDate(0, n, 0), true
	case "year":
		return from.AddDate(n, 0, 0), true
	}
	return time.Time{}, false
}

// switchQuote previews the exact change a confirmed switch would make, using the proration and
// payment behavior decideSwitch chooses, so the figure is the one that will be charged.
func (s *Service) switchQuote(ctx context.Context, userID string, sw *planSwitch, priceID string, qty, prorationDate int64) (*Quote, error) {
	d, err := s.decideSwitch(ctx, userID, sw, priceID)
	if err != nil {
		return nil, err
	}
	if prorationDate == 0 {
		prorationDate = time.Now().Unix()
	}
	d.params.ProrationDate = prorationDate
	prev, err := s.gw.PreviewSwitch(ctx, d.params)
	if err != nil {
		return nil, providerFailed("quote", err, "product", sw.product.Slug, "subscription", sw.subscriptionID, "price_id", priceID)
	}

	q := &Quote{
		Action: QuoteSwitch, CurrentProduct: ref(sw.current), NewProduct: ref(sw.product),
		Quantity: d.params.Quantity, Currency: prev.Currency, ProrationDate: prorationDate,
	}
	if q.Currency == "" {
		q.Currency = d.price.Currency
	}
	next := d.sub.CurrentPeriodEnd
	if !next.IsZero() {
		q.NextBillingDate = &next
	}
	var sum int64
	for _, l := range prev.Lines {
		if l.Proration {
			sum += l.Amount
			q.ProrationLines = append(q.ProrationLines, QuoteLine{Description: l.Description, Amount: l.Amount})
		}
	}
	switch {
	case d.upgrade:
		q.AmountDueNow = max(sum, 0)
	case sum < 0:
		q.CreditOnNextInvoice = -sum
	}
	return q, nil
}
