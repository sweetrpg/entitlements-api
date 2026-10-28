package billing

import (
	"context"
	"fmt"

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

// Service starts Stripe sessions and turns Stripe events into grants.
type Service struct {
	store data.Store
	gw    Gateway
	urls  URLs
}

// New returns a Service.
func New(store data.Store, gw Gateway, urls URLs) *Service {
	return &Service{store: store, gw: gw, urls: urls}
}

// Checkout returns a Stripe-hosted checkout URL for a tier, tied to userID. A tier with no
// Stripe price ID, or one that does not exist, is rejected as invalid. email may be empty.
func (s *Service) Checkout(ctx context.Context, userID, email, tierID string) (string, error) {
	tiers, err := s.store.GetTiers(ctx, []string{tierID})
	if err != nil {
		return "", err
	}
	if len(tiers) == 0 || tiers[0].StripePriceID == "" {
		return "", fmt.Errorf("%w: tier is not available for purchase", models.ErrInvalid)
	}
	customer, err := s.store.StripeCustomer(ctx, userID)
	if err != nil {
		return "", err
	}
	return s.gw.CreateCheckout(ctx, CheckoutParams{
		PriceID: tiers[0].StripePriceID, UserID: userID, TierID: tierID, Email: email, CustomerID: customer,
		SuccessURL: s.urls.CheckoutSuccess, CancelURL: s.urls.CheckoutCancel,
	})
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
