package data

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sweetrpg/entitlements-api/models"
)

var ctx = context.Background()

func stripeGrant(ext string) *models.Grant {
	return &models.Grant{UserID: "u1", TierID: "plus", Source: models.SourceStripeSubscription, ExternalID: ext, Status: models.GrantActive}
}

func TestInsertGrantRejectsDuplicateSourceAndExternalID(t *testing.T) {
	m := NewMemoryStore()
	if err := m.InsertGrant(ctx, stripeGrant("sub_1"), "system"); err != nil {
		t.Fatal(err)
	}
	if err := m.InsertGrant(ctx, stripeGrant("sub_1"), "system"); !errors.Is(err, ErrDuplicate) {
		t.Errorf("err = %v, want ErrDuplicate", err)
	}
}

func TestInsertGrantStampsAuditFields(t *testing.T) {
	m := NewMemoryStore()
	g := stripeGrant("sub_1")
	_ = m.InsertGrant(ctx, g, "system")
	if g.CreatedBy != "system" || g.UpdatedBy != "system" || g.CreatedAt.IsZero() {
		t.Errorf("audit = %+v", g.Auditable)
	}
}

func TestGrantByExternalAndUpdate(t *testing.T) {
	m := NewMemoryStore()
	_ = m.InsertGrant(ctx, stripeGrant("sub_1"), "system")
	g, err := m.GrantByExternal(ctx, models.SourceStripeSubscription, "sub_1")
	if err != nil {
		t.Fatal(err)
	}
	g.Status = models.GrantEnded
	if err := m.UpdateGrant(ctx, g, "system"); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GrantByExternal(ctx, models.SourceStripeSubscription, "sub_1"); got.Status != models.GrantEnded {
		t.Errorf("status = %s", got.Status)
	}
	if _, err := m.GrantByExternal(ctx, models.SourceStripePurchase, "sub_1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	if err := m.UpdateGrant(ctx, stripeGrant("missing"), "system"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestGrantsByRefMatchesExternalIDAndRefs(t *testing.T) {
	m := NewMemoryStore()
	g := stripeGrant("sub_1")
	g.Refs = []string{"in_1", "ch_1"}
	_ = m.InsertGrant(ctx, g, "system")
	for _, ref := range []string{"sub_1", "in_1", "ch_1"} {
		if gs, _ := m.GrantsByRef(ctx, ref); len(gs) != 1 {
			t.Errorf("ref %s matched %d grants", ref, len(gs))
		}
	}
	if gs, _ := m.GrantsByRef(ctx, "nope"); len(gs) != 0 {
		t.Errorf("unknown ref matched %d grants", len(gs))
	}
}

func TestActiveGrantsHonorsStatusAndExpiry(t *testing.T) {
	m := NewMemoryStore()
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for i, g := range []models.Grant{
		{UserID: "u1", Status: models.GrantActive},
		{UserID: "u1", Status: models.GrantActive, ValidUntil: &future},
		{UserID: "u1", Status: models.GrantActive, ValidUntil: &past},
		{UserID: "u1", Status: models.GrantEnded},
		{UserID: "u1", Status: models.GrantRevoked},
		{UserID: "u2", Status: models.GrantActive},
	} {
		g.ExternalID = string(rune('a' + i))
		m.AddGrant(g)
	}
	if gs, _ := m.ActiveGrants(ctx, "u1", now); len(gs) != 2 {
		t.Errorf("active grants = %d, want 2", len(gs))
	}
}

func TestStripeCustomerRoundTrip(t *testing.T) {
	m := NewMemoryStore()
	if c, err := m.StripeCustomer(ctx, "u1"); c != "" || err != nil {
		t.Fatalf("customer = %q err = %v", c, err)
	}
	_ = m.SaveStripeCustomer(ctx, "u1", "cus_1", "system")
	_ = m.SaveStripeCustomer(ctx, "u1", "cus_2", "system")
	if c, _ := m.StripeCustomer(ctx, "u1"); c != "cus_2" {
		t.Errorf("customer = %q, want latest", c)
	}
}

func TestEventRecordingIsIdempotent(t *testing.T) {
	m := NewMemoryStore()
	if done, _ := m.EventProcessed(ctx, "evt_1"); done {
		t.Fatal("unrecorded event reported processed")
	}
	_ = m.RecordEvent(ctx, "evt_1", "invoice.paid", time.Now())
	if err := m.RecordEvent(ctx, "evt_1", "invoice.paid", time.Now()); err != nil {
		t.Errorf("re-recording errored: %v", err)
	}
	if done, _ := m.EventProcessed(ctx, "evt_1"); !done {
		t.Error("recorded event not reported processed")
	}
}
