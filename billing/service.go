package billing

import (
	"context"
	"fmt"
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
	CheckPurchase(ctx context.Context, userID string, adds []catalog.Units) error
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

// Checkout returns a Stripe-hosted checkout URL tied to userID. Anything not purchasable (no
// such tier or product, no Stripe price, a quantity the product does not allow) is rejected as
// invalid; a product the user already holds and cannot repeat, or one that would pass a key's
// maximum, is refused before any Stripe session is created. email may be empty.
func (s *Service) Checkout(ctx context.Context, userID, email string, req CheckoutRequest) (string, error) {
	if (req.TierID == "") == (req.ProductID == "") {
		return "", fmt.Errorf("%w: provide either tier_id or product_id", models.ErrInvalid)
	}
	params := CheckoutParams{UserID: userID, Email: email, SuccessURL: s.urls.CheckoutSuccess, CancelURL: s.urls.CheckoutCancel}
	var err error
	if req.TierID != "" {
		err = s.tierParams(ctx, req.TierID, &params)
	} else {
		err = s.productParams(ctx, userID, req, &params)
	}
	if err != nil {
		return "", err
	}
	if params.CustomerID, err = s.store.StripeCustomer(ctx, userID); err != nil {
		return "", err
	}
	return s.gw.CreateCheckout(ctx, params)
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

func (s *Service) productParams(ctx context.Context, userID string, req CheckoutRequest, p *CheckoutParams) error {
	qty := req.Quantity
	if qty == 0 {
		qty = 1
	}
	if qty < 1 || qty > constants.MaxPurchaseQuantity {
		return fmt.Errorf("%w: quantity must be between 1 and %d", models.ErrInvalid, constants.MaxPurchaseQuantity)
	}
	products, err := s.store.GetProducts(ctx, []string{req.ProductID})
	if err != nil {
		return err
	}
	if len(products) == 0 || products[0].StripePriceID == "" {
		return fmt.Errorf("%w: product is not available for purchase", models.ErrInvalid)
	}
	prod := products[0]
	if !prod.Repeatable {
		if qty != 1 {
			return fmt.Errorf("%w: this product cannot be bought with a quantity", models.ErrInvalid)
		}
		if err := s.checkNotHeld(ctx, userID, prod.Slug); err != nil {
			return err
		}
	}
	adds := make([]catalog.Units, 0, len(prod.Entitlements))
	for _, e := range prod.Entitlements {
		if e.Type == models.TypeLimit {
			adds = append(adds, catalog.Units{Key: e.Key, Units: e.Value * qty})
		}
	}
	if err := s.limits.CheckPurchase(ctx, userID, adds); err != nil {
		return err
	}
	p.PriceID, p.ProductID, p.Quantity = prod.StripePriceID, prod.Slug, qty
	return nil
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
	return s.gw.CreatePortal(ctx, customer, s.urls.PortalReturn)
}

// systemActor stamps writes made by webhook events, which no user drives.
const systemActor = mcore.SystemActor
