package billing

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"time"

	"github.com/sweetrpg/common.go/logging"
	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
	mcore "github.com/sweetrpg/model-core.go/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Event is a verified Stripe event reduced to what the handlers need.
type Event struct {
	ID     string
	Type   string
	Object json.RawMessage
}

// HandleEvent applies one Stripe event. Delivery is at-least-once, so it is idempotent twice
// over: an event ID already recorded is skipped, and every handler converges on the same grant
// state when replayed (the ID is recorded only after the handler succeeds, so a failed event is
// retried). Event types the service does not act on are ignored without being recorded.
func (s *Service) HandleEvent(ctx context.Context, ev Event) error {
	handle, ok := s.handlers()[ev.Type]
	if !ok {
		return nil
	}
	done, err := s.store.EventProcessed(ctx, ev.ID)
	if err != nil || done {
		return err
	}
	if err := handle(ctx, ev.Object); err != nil {
		return err
	}
	return s.store.RecordEvent(ctx, ev.ID, ev.Type, time.Now().UTC())
}

type handlerFunc func(ctx context.Context, object json.RawMessage) error

func (s *Service) handlers() map[string]handlerFunc {
	return map[string]handlerFunc{
		"checkout.session.completed":               s.onCheckout,
		"checkout.session.async_payment_succeeded": s.onCheckout,
		"invoice.paid":                             s.onInvoicePaid,
		"customer.subscription.updated":            s.onSubscriptionUpdated,
		"customer.subscription.deleted":            s.onSubscriptionDeleted,
		"charge.refunded":                          s.onChargeRefunded,
		"charge.dispute.created":                   s.onDispute,
	}
}

// purchase is what a Stripe event says a user bought: one Stripe object (the subscription or the
// payment intent) that becomes one grant per entitlement of a product, or one tier grant.
type purchase struct {
	source     string
	externalID string
	userID     string
	tierID     string
	productID  string
	quantity   int64
	validUntil *time.Time
	refs       []string
}

func (s *Service) onCheckout(ctx context.Context, raw json.RawMessage) error {
	var cs checkoutSession
	if err := json.Unmarshal(raw, &cs); err != nil {
		return err
	}
	if cs.PaymentStatus != "paid" && cs.PaymentStatus != "no_payment_required" {
		return nil // async payment pending; async_payment_succeeded follows
	}
	user := cs.Metadata["user_id"]
	if user == "" {
		user = cs.ClientReferenceID
	}
	if user == "" {
		logging.Logger.Warn("stripe checkout session has no user, ignoring", "session", cs.ID)
		return nil
	}
	if cs.Customer != "" {
		if err := s.store.SaveStripeCustomer(ctx, user, cs.Customer, systemActor); err != nil {
			return err
		}
	}
	qty, _ := strconv.ParseInt(cs.Metadata["quantity"], 10, 64)
	p := purchase{
		userID: user, tierID: cs.Metadata["tier_id"], productID: cs.Metadata["product_id"],
		quantity: max(qty, 1), refs: nonEmpty(cs.ID),
	}
	switch cs.Mode {
	case "subscription":
		p.source, p.externalID = models.SourceStripeSubscription, cs.Subscription
	case "payment":
		p.source, p.externalID = models.SourceStripePurchase, cs.PaymentIntent
	default:
		return nil
	}
	if p.externalID == "" {
		return nil
	}
	return s.applyPurchase(ctx, p)
}

func (s *Service) onInvoicePaid(ctx context.Context, raw json.RawMessage) error {
	var inv invoice
	if err := json.Unmarshal(raw, &inv); err != nil {
		return err
	}
	sub, meta := inv.details()
	if sub == "" {
		return nil // not a subscription invoice
	}
	p := purchase{
		source: models.SourceStripeSubscription, externalID: sub,
		userID: meta["user_id"], tierID: meta["tier_id"], productID: meta["product_id"],
		quantity: inv.quantity(), refs: nonEmpty(inv.ID, inv.PaymentIntent, inv.Charge),
	}
	if end := inv.periodEnd(); end > 0 {
		t := time.Unix(end, 0).UTC()
		p.validUntil = &t
	}
	return s.applyPurchase(ctx, p)
}

// onSubscriptionUpdated ends the grants when Stripe gives up on a subscription, and follows a
// quantity change. past_due is left active: Stripe is still retrying the payment.
func (s *Service) onSubscriptionUpdated(ctx context.Context, raw json.RawMessage) error {
	var sub subscription
	if err := json.Unmarshal(raw, &sub); err != nil {
		return err
	}
	switch sub.Status {
	case "unpaid", "canceled", "incomplete_expired":
		return s.endGrants(ctx, models.SourceStripeSubscription, sub.ID)
	}
	return s.followSubscription(ctx, sub)
}

func (s *Service) onSubscriptionDeleted(ctx context.Context, raw json.RawMessage) error {
	var sub subscription
	if err := json.Unmarshal(raw, &sub); err != nil {
		return err
	}
	return s.endGrants(ctx, models.SourceStripeSubscription, sub.ID)
}

// onChargeRefunded revokes only on a full refund; a partial refund leaves the grants alone.
func (s *Service) onChargeRefunded(ctx context.Context, raw json.RawMessage) error {
	var ch charge
	if err := json.Unmarshal(raw, &ch); err != nil {
		return err
	}
	if !ch.Refunded {
		return nil
	}
	return s.revoke(ctx, ch.ID, ch.PaymentIntent, ch.Invoice)
}

func (s *Service) onDispute(ctx context.Context, raw json.RawMessage) error {
	var d dispute
	if err := json.Unmarshal(raw, &d); err != nil {
		return err
	}
	return s.revoke(ctx, d.Charge, d.PaymentIntent)
}

// wanted expands a purchase into the grants it should have: one key-level grant per entitlement
// of a product (value x quantity; a flag is simply on), or a single tier grant. A product that
// no longer exists yields none.
func (s *Service) wanted(ctx context.Context, p purchase) ([]models.Grant, error) {
	base := models.Grant{
		UserID: p.userID, Source: p.source, ExternalID: p.externalID,
		ValidUntil: p.validUntil, Refs: p.refs, Status: models.GrantActive,
	}
	switch {
	case p.productID != "":
		products, err := s.store.GetProducts(ctx, []string{p.productID})
		if err != nil {
			return nil, err
		}
		if len(products) == 0 {
			logging.Logger.Warn("stripe event names a product that is not in the catalog", "product", p.productID, "external_id", p.externalID)
			return nil, nil
		}
		out := make([]models.Grant, 0, len(products[0].Entitlements))
		for _, e := range products[0].Entitlements {
			g := base
			g.Key, g.Value, g.ProductID, g.Quantity, g.Mode = e.Key, grantValue(e, p.quantity), p.productID, p.quantity, e.Mode
			out = append(out, g)
		}
		return out, nil
	case p.tierID != "":
		g := base
		g.TierID = p.tierID
		return []models.Grant{g}, nil
	}
	return nil, nil
}

// grantValue is what one purchase grants: a flag is on, an at_least floor is the same whatever the
// quantity, and an add entitlement is its value per unit x quantity.
func grantValue(e models.ProductEntitlement, quantity int64) int64 {
	if e.Type == models.TypeFlag {
		return 1
	}
	if e.IsAtLeast() {
		return e.Value
	}
	return e.Value * quantity
}

// applyPurchase creates the grants a purchase should have and folds a later event into the ones
// that exist: it reactivates an ended grant, only ever extends valid_until, and unions refs. A
// revoked grant is never touched. Existing grants are updated even when the event carries no
// product or tier (a renewal invoice), and an event that would create grants without knowing
// the user is ignored.
func (s *Service) applyPurchase(ctx context.Context, p purchase) error {
	existing, err := s.store.GrantsByExternal(ctx, p.source, p.externalID)
	if err != nil {
		return err
	}
	want, err := s.wanted(ctx, p)
	if err != nil {
		return err
	}
	for i := range existing {
		if err := s.mergeGrant(ctx, &existing[i], p); err != nil {
			return err
		}
	}
	for _, g := range want {
		if slices.ContainsFunc(existing, func(e models.Grant) bool { return e.Key == g.Key && e.TierID == g.TierID }) {
			continue
		}
		if g.UserID == "" {
			logging.Logger.Warn("stripe event cannot create a grant without a user", "external_id", p.externalID)
			return nil
		}
		if err := s.store.InsertGrant(ctx, &g, systemActor); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) mergeGrant(ctx context.Context, cur *models.Grant, p purchase) error {
	if cur.Status == models.GrantRevoked {
		return nil
	}
	cur.Status = models.GrantActive
	if p.userID != "" {
		cur.UserID = p.userID
	}
	if p.validUntil != nil && (cur.ValidUntil == nil || p.validUntil.After(*cur.ValidUntil)) {
		cur.ValidUntil = p.validUntil
	}
	for _, r := range p.refs {
		if !slices.Contains(cur.Refs, r) {
			cur.Refs = append(cur.Refs, r)
		}
	}
	return s.store.UpdateGrant(ctx, cur, systemActor)
}

// followSubscription reconciles a subscription's grants with its current price and quantity. The
// price decides the product (a customer who changed plan in the billing portal changes the
// price, not our metadata); metadata is the fallback. Stripe has already accepted the change, so
// a result above a key's maximum cannot be refused here; it is logged and checks clamp it.
func (s *Service) followSubscription(ctx context.Context, sub subscription) error {
	qty := sub.quantity()
	if qty < 1 {
		return nil
	}
	prod, err := s.productFor(ctx, sub)
	if err != nil || prod == nil {
		return err
	}
	grants, err := s.store.GrantsByExternal(ctx, models.SourceStripeSubscription, sub.ID)
	if err != nil || len(grants) == 0 {
		return err
	}
	return s.replaceEntitlements(ctx, grants, *prod, qty)
}

func (s *Service) productFor(ctx context.Context, sub subscription) (*models.Product, error) {
	if price := sub.priceID(); price != "" {
		p, err := s.store.ProductByPriceID(ctx, price)
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, data.ErrNotFound) {
			return nil, err
		}
	}
	if id := sub.Metadata["product_id"]; id != "" {
		products, err := s.store.GetProducts(ctx, []string{id})
		if err != nil || len(products) == 0 {
			return nil, err
		}
		return &products[0], nil
	}
	return nil, nil
}

// replaceEntitlements makes a purchase's grants match a product at a quantity: a grant for each
// of the product's keys (updated in place, or created), and any grant for a key it no longer
// has ended. It keeps one set of grants per Stripe object, sharing its ID and validity. A revoked
// purchase is left alone.
func (s *Service) replaceEntitlements(ctx context.Context, grants []models.Grant, prod models.Product, qty int64) error {
	if len(grants) == 0 || slices.ContainsFunc(grants, func(g models.Grant) bool { return g.Status == models.GrantRevoked }) {
		return nil
	}
	keep := map[string]bool{}
	for _, e := range prod.Entitlements {
		keep[e.Key] = true
		want := grantValue(e, qty)
		i := slices.IndexFunc(grants, func(g models.Grant) bool { return g.Key == e.Key })
		if i < 0 {
			g := grants[0]
			g.ID, g.Auditable, g.TierID, g.Status = primitive.NilObjectID, mcore.Auditable{}, "", models.GrantActive
			g.Key, g.Value, g.ProductID, g.Quantity, g.Mode = e.Key, want, prod.Slug, qty, e.Mode
			if err := s.store.InsertGrant(ctx, &g, systemActor); err != nil {
				return err
			}
			continue
		}
		g := &grants[i]
		if g.Value == want && g.Quantity == qty && g.ProductID == prod.Slug && g.Mode == e.Mode && g.Status == models.GrantActive {
			continue
		}
		g.Value, g.Quantity, g.ProductID, g.Mode, g.Status = want, qty, prod.Slug, e.Mode, models.GrantActive
		if err := s.store.UpdateGrant(ctx, g, systemActor); err != nil {
			return err
		}
		s.warnIfOverMaximum(ctx, g)
	}
	for i := range grants {
		if keep[grants[i].Key] || grants[i].Status != models.GrantActive {
			continue
		}
		grants[i].Status = models.GrantEnded
		if err := s.store.UpdateGrant(ctx, &grants[i], systemActor); err != nil {
			return err
		}
	}
	return nil
}

// quantityOf is the quantity a purchase was made at, 1 if unrecorded.
func quantityOf(grants []models.Grant) int64 {
	for _, g := range grants {
		if g.Quantity > 0 {
			return g.Quantity
		}
	}
	return 1
}

// warnIfOverMaximum probes whether the user now holds more than the key's maximum.
func (s *Service) warnIfOverMaximum(ctx context.Context, g *models.Grant) {
	var exceeded catalog.MaxExceededError
	if err := s.limits.CheckPurchase(ctx, g.UserID, []catalog.Units{{Key: g.Key}}); errors.As(err, &exceeded) {
		logging.Logger.Warn("subscription quantity change passes the key maximum; checks clamp it",
			"user", g.UserID, "key", g.Key, "external_id", g.ExternalID)
	}
}

// endGrants ends every active grant of one Stripe object together.
func (s *Service) endGrants(ctx context.Context, source, externalID string) error {
	grants, err := s.store.GrantsByExternal(ctx, source, externalID)
	if err != nil {
		return err
	}
	for i := range grants {
		if grants[i].Status != models.GrantActive {
			continue
		}
		grants[i].Status = models.GrantEnded
		if err := s.store.UpdateGrant(ctx, &grants[i], systemActor); err != nil {
			return err
		}
	}
	return nil
}

// revoke revokes every grant that names one of the Stripe objects, so a bundle goes together.
func (s *Service) revoke(ctx context.Context, refs ...string) error {
	for _, ref := range nonEmpty(refs...) {
		grants, err := s.store.GrantsByRef(ctx, ref)
		if err != nil {
			return err
		}
		for i := range grants {
			if grants[i].Status == models.GrantRevoked {
				continue
			}
			grants[i].Status = models.GrantRevoked
			if err := s.store.UpdateGrant(ctx, &grants[i], systemActor); err != nil {
				return err
			}
		}
	}
	return nil
}

func nonEmpty(vals ...string) []string {
	return slices.DeleteFunc(slices.Clone(vals), func(v string) bool { return v == "" })
}
