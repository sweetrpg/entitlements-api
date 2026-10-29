package billing

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/constants"
	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
	mcore "github.com/sweetrpg/model-core.go/models"
)

// URLs are the fixed redirect targets, taken from config so no request can choose one.
type URLs struct {
	CheckoutSuccess string
	CheckoutCancel  string
	PortalReturn    string
}

// Limits is the part of the catalog service billing needs: the per-key maximum check.
type Limits interface {
	CheckPurchase(ctx context.Context, userID string, adds []catalog.Units, opts ...catalog.PurchaseOption) error
	CheckRaise(ctx context.Context, userID string, ents []models.ProductEntitlement, opts ...catalog.PurchaseOption) error
}

// Service starts Stripe sessions and turns Stripe events into grants.
type Service struct {
	store  data.Store
	gw     Gateway
	limits Limits
	urls   URLs
}

// New returns a Service.
func New(store data.Store, gw Gateway, limits Limits, urls URLs) *Service {
	return &Service{store: store, gw: gw, limits: limits, urls: urls}
}

// CheckoutRequest names what to buy: exactly one of TierID or ProductID. Quantity applies to
// products and defaults to 1.
type CheckoutRequest struct {
	TierID    string
	ProductID string
	Quantity  int64
}

// CheckoutResult is either a Stripe-hosted checkout URL, or Switched when the user's existing
// subscription in the product's plan group was moved to the new price instead.
type CheckoutResult struct {
	URL      string
	Switched bool
}

// Checkout returns a Stripe-hosted checkout URL tied to userID. Anything not purchasable (no
// such tier or product, no Stripe price, a quantity the product does not allow) is rejected as
// invalid; a product the user already holds and cannot repeat, one that would pass a key's
// maximum, or one that would raise nothing, is refused before any Stripe session is created. A
// recurring product in a plan group the user already subscribes to switches that subscription
// to the new price (Stripe prorates) and returns Switched, creating no second subscription.
// email may be empty.
func (s *Service) Checkout(ctx context.Context, userID, email string, req CheckoutRequest) (CheckoutResult, error) {
	if (req.TierID == "") == (req.ProductID == "") {
		return CheckoutResult{}, fmt.Errorf("%w: provide either tier_id or product_id", models.ErrInvalid)
	}
	params := CheckoutParams{UserID: userID, Email: email, SuccessURL: s.urls.CheckoutSuccess, CancelURL: s.urls.CheckoutCancel}
	var (
		err error
		sw  *planSwitch
	)
	if req.TierID != "" {
		err = s.tierParams(ctx, req.TierID, &params)
	} else {
		sw, err = s.productParams(ctx, userID, req, &params)
	}
	if err != nil {
		return CheckoutResult{}, err
	}
	if sw != nil {
		return s.switchPlan(ctx, userID, sw, params)
	}
	if params.CustomerID, err = s.store.StripeCustomer(ctx, userID); err != nil {
		return CheckoutResult{}, err
	}
	url, err := s.gw.CreateCheckout(ctx, params)
	if err != nil {
		return CheckoutResult{}, providerFailed("checkout", err, "product", req.ProductID, "tier", req.TierID, "price_id", params.PriceID)
	}
	return CheckoutResult{URL: url}, nil
}

// planSwitch is the subscription a checkout would move to another product in its plan group.
type planSwitch struct {
	subscriptionID string
	product        models.Product
}

// switchPlan changes the Stripe subscription, then applies the new product's entitlements to its
// grants right away; the customer.subscription.updated event that follows finds nothing to change.
func (s *Service) switchPlan(ctx context.Context, userID string, sw *planSwitch, p CheckoutParams) (CheckoutResult, error) {
	err := s.gw.SwitchPlan(ctx, SwitchParams{SubscriptionID: sw.subscriptionID, PriceID: p.PriceID, ProductID: sw.product.Slug, UserID: userID})
	if err != nil {
		return CheckoutResult{}, providerFailed("plan switch", err, "product", sw.product.Slug, "subscription", sw.subscriptionID, "price_id", p.PriceID)
	}
	grants, err := s.store.GrantsByExternal(ctx, models.SourceStripeSubscription, sw.subscriptionID)
	if err != nil {
		return CheckoutResult{}, err
	}
	return CheckoutResult{Switched: true}, s.replaceEntitlements(ctx, grants, sw.product, quantityOf(grants))
}

func (s *Service) tierParams(ctx context.Context, tierID string, p *CheckoutParams) error {
	tiers, err := s.store.GetTiers(ctx, []string{tierID})
	if err != nil {
		return err
	}
	if len(tiers) == 0 || tiers[0].StripePriceID == "" {
		return fmt.Errorf("%w: tier is not available for purchase", models.ErrInvalid)
	}
	p.PriceID, p.TierID = tiers[0].StripePriceID, tierID
	return nil
}

func (s *Service) productParams(ctx context.Context, userID string, req CheckoutRequest, p *CheckoutParams) (*planSwitch, error) {
	qty := req.Quantity
	if qty == 0 {
		qty = 1
	}
	if qty < 1 || qty > constants.MaxPurchaseQuantity {
		return nil, fmt.Errorf("%w: quantity must be between 1 and %d", models.ErrInvalid, constants.MaxPurchaseQuantity)
	}
	products, err := s.store.GetProducts(ctx, []string{req.ProductID})
	if err != nil {
		return nil, err
	}
	if len(products) == 0 || products[0].StripePriceID == "" {
		return nil, fmt.Errorf("%w: product is not available for purchase", models.ErrInvalid)
	}
	prod := products[0]
	if !prod.Repeatable {
		if qty != 1 {
			return nil, fmt.Errorf("%w: this product cannot be bought with a quantity", models.ErrInvalid)
		}
		if err := s.checkNotHeld(ctx, userID, prod.Slug); err != nil {
			return nil, err
		}
	}
	sw, err := s.findPlanMember(ctx, userID, prod)
	if err != nil {
		return nil, err
	}
	var opts []catalog.PurchaseOption
	if sw != nil {
		opts = append(opts, catalog.Replacing(sw.subscriptionID))
	}
	adds := make([]catalog.Units, 0, len(prod.Entitlements))
	for _, e := range prod.Entitlements {
		if e.Type == models.TypeLimit && !e.IsAtLeast() {
			adds = append(adds, catalog.Units{Key: e.Key, Units: e.Value * qty})
		}
	}
	if err := s.limits.CheckPurchase(ctx, userID, adds, opts...); err != nil {
		return nil, err
	}
	if err := s.limits.CheckRaise(ctx, userID, prod.Entitlements, opts...); err != nil {
		return nil, err
	}
	p.PriceID, p.ProductID, p.Quantity = prod.StripePriceID, prod.Slug, qty
	if sw != nil {
		sw.product = prod
	}
	return sw, nil
}

// findPlanMember returns the user's active subscription to another product in prod's plan group,
// if any. Holding the same product again is refused. Products outside a group never switch.
func (s *Service) findPlanMember(ctx context.Context, userID string, prod models.Product) (*planSwitch, error) {
	if prod.PlanGroup == "" {
		return nil, nil
	}
	grants, err := s.store.ActiveGrants(ctx, userID, time.Now())
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, g := range grants {
		if g.Source == models.SourceStripeSubscription && g.ProductID != "" && !slices.Contains(ids, g.ProductID) {
			ids = append(ids, g.ProductID)
		}
	}
	if len(ids) == 0 {
		return nil, nil // no subscription to compare against
	}
	held, err := s.store.GetProducts(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, h := range held {
		if h.PlanGroup != prod.PlanGroup {
			continue
		}
		if h.Slug == prod.Slug {
			return nil, fmt.Errorf("%w: already subscribed to this product", catalog.ErrInUse)
		}
		for _, g := range grants {
			if g.Source == models.SourceStripeSubscription && g.ProductID == h.Slug {
				return &planSwitch{subscriptionID: g.ExternalID}, nil
			}
		}
	}
	return nil, nil
}

// checkNotHeld refuses a second purchase of a non-repeatable product.
func (s *Service) checkNotHeld(ctx context.Context, userID, productID string) error {
	grants, err := s.store.ActiveGrants(ctx, userID, time.Now())
	if err != nil {
		return err
	}
	for _, g := range grants {
		if g.ProductID == productID {
			return fmt.Errorf("%w: product already purchased", catalog.ErrInUse)
		}
	}
	return nil
}

// Portal returns a billing-portal URL for the user's Stripe customer, or data.ErrNotFound when
// the user has never completed a checkout.
func (s *Service) Portal(ctx context.Context, userID string) (string, error) {
	customer, err := s.store.StripeCustomer(ctx, userID)
	if err != nil {
		return "", err
	}
	if customer == "" {
		return "", data.ErrNotFound
	}
	url, err := s.gw.CreatePortal(ctx, customer, s.urls.PortalReturn)
	if err != nil {
		return "", providerFailed("portal", err, "customer", customer)
	}
	return url, nil
}

// systemActor stamps writes made by webhook events, which no user drives.
const systemActor = mcore.SystemActor
