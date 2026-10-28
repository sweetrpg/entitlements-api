package billing

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/sweetrpg/common.go/logging"
	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
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
	g := models.Grant{UserID: user, TierID: cs.Metadata["tier_id"], Refs: []string{cs.ID}}
	switch cs.Mode {
	case "subscription":
		g.Source, g.ExternalID = models.SourceStripeSubscription, cs.Subscription
	case "payment":
		g.Source, g.ExternalID = models.SourceStripePurchase, cs.PaymentIntent
	default:
		return nil
	}
	if g.ExternalID == "" {
		return nil
	}
	return s.upsertGrant(ctx, g)
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
	g := models.Grant{
		Source: models.SourceStripeSubscription, ExternalID: sub,
		UserID: meta["user_id"], TierID: meta["tier_id"],
		Refs: nonEmpty(inv.ID, inv.PaymentIntent, inv.Charge),
	}
	if end := inv.periodEnd(); end > 0 {
		t := time.Unix(end, 0).UTC()
		g.ValidUntil = &t
	}
	return s.upsertGrant(ctx, g)
}

// onSubscriptionUpdated ends the grant when Stripe gives up on a subscription. past_due is
// left active: Stripe is still retrying the payment.
func (s *Service) onSubscriptionUpdated(ctx context.Context, raw json.RawMessage) error {
	var sub subscription
	if err := json.Unmarshal(raw, &sub); err != nil {
		return err
	}
	switch sub.Status {
	case "unpaid", "canceled", "incomplete_expired":
		return s.endGrant(ctx, models.SourceStripeSubscription, sub.ID)
	}
	return nil
}

func (s *Service) onSubscriptionDeleted(ctx context.Context, raw json.RawMessage) error {
	var sub subscription
	if err := json.Unmarshal(raw, &sub); err != nil {
		return err
	}
	return s.endGrant(ctx, models.SourceStripeSubscription, sub.ID)
}

// onChargeRefunded revokes only on a full refund; a partial refund leaves the grant alone.
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

// upsertGrant creates the grant for (source, external id) or folds in a later event: it
// reactivates an ended grant, only ever extends valid_until, and unions refs. A revoked grant
// is never touched. An event that would create a grant without knowing the user is ignored.
func (s *Service) upsertGrant(ctx context.Context, in models.Grant) error {
	cur, err := s.store.GrantByExternal(ctx, in.Source, in.ExternalID)
	if errors.Is(err, data.ErrNotFound) {
		if in.UserID == "" {
			logging.Logger.Warn("stripe event cannot create a grant without a user", "external_id", in.ExternalID)
			return nil
		}
		in.Status = models.GrantActive
		return s.store.InsertGrant(ctx, &in, systemActor)
	}
	if err != nil || cur.Status == models.GrantRevoked {
		return err
	}
	cur.Status = models.GrantActive
	if in.UserID != "" {
		cur.UserID = in.UserID
	}
	if in.TierID != "" {
		cur.TierID = in.TierID
	}
	if in.ValidUntil != nil && (cur.ValidUntil == nil || in.ValidUntil.After(*cur.ValidUntil)) {
		cur.ValidUntil = in.ValidUntil
	}
	for _, r := range in.Refs {
		if !slices.Contains(cur.Refs, r) {
			cur.Refs = append(cur.Refs, r)
		}
	}
	return s.store.UpdateGrant(ctx, cur, systemActor)
}

func (s *Service) endGrant(ctx context.Context, source, externalID string) error {
	g, err := s.store.GrantByExternal(ctx, source, externalID)
	if errors.Is(err, data.ErrNotFound) {
		return nil
	}
	if err != nil || g.Status != models.GrantActive {
		return err
	}
	g.Status = models.GrantEnded
	return s.store.UpdateGrant(ctx, g, systemActor)
}

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
