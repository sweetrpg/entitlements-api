// Package data is the persistence boundary. Filtering, sorting, and limits are arguments to the
// store, never applied by callers to an unbounded result (docs/data-conventions.md).
//
// Delete strategy: catalog keys and tiers are soft-deleted (deleted_at/deleted_by) and every
// read excludes tombstones. The catalog is admin-managed configuration, but PADR-0027's hard
// delete carve-outs do not list it, so it takes the default. The audit collection is
// append-only and has no update or delete path.
package data

import (
	"context"
	"errors"
	"time"

	"github.com/sweetrpg/entitlements-api/models"
)

var (
	// ErrNotFound means no live document matched.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate means a live document with the same key or slug exists.
	ErrDuplicate = errors.New("already exists")
)

// Store is everything the catalog and check logic need from persistence. Every mutating method
// takes the acting user, which is stamped into the audit fields.
type Store interface {
	InsertKey(ctx context.Context, k *models.Key, actor string) error
	GetKeys(ctx context.Context, names []string) ([]models.Key, error)
	// ListKeys returns live keys whose name starts with prefix, ordered by name.
	ListKeys(ctx context.Context, prefix string, limit, skip int64) ([]models.Key, error)
	UpdateKey(ctx context.Context, name, description string, ttlSeconds int, max *int64, actor string) (*models.Key, error)
	DeleteKey(ctx context.Context, name, actor string) error

	InsertTier(ctx context.Context, t *models.Tier, actor string) error
	GetTiers(ctx context.Context, slugs []string) ([]models.Tier, error)
	ListTiers(ctx context.Context, limit, skip int64) ([]models.Tier, error)
	UpdateTier(ctx context.Context, t *models.Tier, actor string) (*models.Tier, error)
	DeleteTier(ctx context.Context, slug, actor string) error

	// DefaultTierID returns "" when no default is configured.
	DefaultTierID(ctx context.Context) (string, error)
	SetDefaultTierID(ctx context.Context, slug, actor string) error

	// ActiveGrants returns the user's grants that count at now.
	ActiveGrants(ctx context.Context, userID string, now time.Time) ([]models.Grant, error)

	// GrantByExternal finds the grant created from one Stripe object; ErrNotFound if none.
	GrantByExternal(ctx context.Context, source, externalID string) (*models.Grant, error)
	// GrantsByRef finds grants whose ExternalID or Refs contain a Stripe object ID.
	GrantsByRef(ctx context.Context, ref string) ([]models.Grant, error)
	// InsertGrant returns ErrDuplicate when (source, external_id) already has a grant.
	InsertGrant(ctx context.Context, g *models.Grant, actor string) error
	UpdateGrant(ctx context.Context, g *models.Grant, actor string) error

	// StripeCustomer returns "" when the user has no customer record.
	StripeCustomer(ctx context.Context, userID string) (string, error)
	SaveStripeCustomer(ctx context.Context, userID, customerID, actor string) error

	// EventProcessed and RecordEvent make webhook handling idempotent by Stripe event ID.
	// RecordEvent on an already-recorded ID is not an error.
	EventProcessed(ctx context.Context, eventID string) (bool, error)
	RecordEvent(ctx context.Context, eventID, eventType string, at time.Time) error

	AppendAudit(ctx context.Context, e models.AuditEntry) error
	// ListAudit returns entries newest first.
	ListAudit(ctx context.Context, limit, skip int64) ([]models.AuditEntry, error)

	Ping(ctx context.Context) error
}
