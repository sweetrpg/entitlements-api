// Package models holds the entitlements catalog and grant documents.
package models

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	mcore "github.com/sweetrpg/model-core.go/models"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// KeyType says how a key's value is interpreted.
type KeyType string

const (
	// TypeFlag is a boolean; stored as 0 or 1.
	TypeFlag KeyType = "flag"
	// TypeLimit is a non-negative integer.
	TypeLimit KeyType = "limit"
)

// Grant statuses and sources.
const (
	GrantActive = "active"
	GrantEnded  = "ended"
	// GrantRevoked is sticky: a refund or dispute ends the grant and later billing events do not
	// bring it back. Only an administrator reinstates it.
	GrantRevoked = "revoked"

	SourceAdmin              = "admin"
	SourceStripeSubscription = "stripe_subscription"
	SourceStripePurchase     = "stripe_purchase"
)

var (
	keyPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	tierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// ErrInvalid marks a validation failure; its message is safe to show an administrator.
var ErrInvalid = errors.New("invalid")

// Key is a registered entitlement key. Type is immutable after creation.
type Key struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	Key         string             `bson:"key" json:"key"`
	Type        KeyType            `bson:"type" json:"type"`
	Description string             `bson:"description" json:"description"`
	TTLSeconds  int                `bson:"ttl_seconds" json:"ttl_seconds"`
	// Max caps every effective value of a limit key; nil means no maximum.
	Max *int64 `bson:"max,omitempty" json:"max,omitempty"`
	// Live is true while the key is not soft-deleted. It exists only so a unique partial index can
	// cover live documents; read paths filter on deleted_at.
	Live            bool `bson:"live,omitempty" json:"-"`
	mcore.Auditable `bson:",inline"`
}

// TierValue is one key's value inside a tier. Type is copied from the key at write time (key
// types never change), so a tier can be rendered without loading the keys.
type TierValue struct {
	Key   string  `bson:"key"`
	Type  KeyType `bson:"type"`
	Value int64   `bson:"value"`
}

// Tier is a named bundle of key values.
type Tier struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	Slug            string             `bson:"slug" json:"id"`
	Name            string             `bson:"name" json:"name"`
	Description     string             `bson:"description" json:"description"`
	Values          []TierValue        `bson:"values" json:"-"`
	StripePriceID   string             `bson:"stripe_price_id,omitempty" json:"stripe_price_id,omitempty"`
	Live            bool               `bson:"live,omitempty" json:"-"` // see Key.Live
	mcore.Auditable `bson:",inline"`
}

// CatalogConfig is the singleton document naming the default tier.
type CatalogConfig struct {
	ID              string `bson:"_id"`
	DefaultTierID   string `bson:"default_tier_id"`
	mcore.Auditable `bson:",inline"`
}

// Grant gives a user a tier or a single key value. Exactly one of TierID or Key is set. Grants
// are written by the billing flow and by admins; this service only reads them for checks.
type Grant struct {
	ID         primitive.ObjectID `bson:"_id,omitempty"`
	UserID     string             `bson:"user_id"`
	TierID     string             `bson:"tier_id,omitempty"`
	Key        string             `bson:"key,omitempty"`
	Value      int64              `bson:"value,omitempty"`
	Source     string             `bson:"source"`
	ExternalID string             `bson:"external_id,omitempty"`
	// ProductID and Quantity record which product purchase a key-level grant came from. All the
	// grants of one purchase share ExternalID and are ended or revoked together.
	ProductID string `bson:"product_id,omitempty"`
	// Mode is ModeAdd or ModeAtLeast for a key-level limit grant; empty means add.
	Mode     string `bson:"mode,omitempty"`
	Quantity int64  `bson:"quantity,omitempty"`
	// Refs are other Stripe object IDs (invoice, payment intent, charge) a refund or dispute
	// event can name instead of ExternalID.
	Refs            []string   `bson:"refs,omitempty"`
	Status          string     `bson:"status"`
	ValidUntil      *time.Time `bson:"valid_until,omitempty"`
	mcore.Auditable `bson:",inline"`
}

// ActiveAt reports whether the grant counts at now. The Mongo query in data.MongoStore applies
// the same rule.
func (g Grant) ActiveAt(now time.Time) bool {
	return g.Status == GrantActive && (g.ValidUntil == nil || g.ValidUntil.After(now))
}

// Entitlement modes. An add entitlement stacks on top of the user's value; an at_least entitlement
// is one of several alternatives (a 10-member and a 20-member plan), and the highest wins.
const (
	ModeAdd     = "add"
	ModeAtLeast = "at_least"
)

// Billing kinds for a product.
const (
	BillingOneOff    = "one_off"
	BillingRecurring = "recurring"
)

// ProductEntitlement is one key and value a product grants per unit purchased. Type is copied
// from the key at write time (key types never change).
type ProductEntitlement struct {
	Key   string  `bson:"key"`
	Type  KeyType `bson:"type"`
	Value int64   `bson:"value"`
	// Mode is ModeAdd or ModeAtLeast; empty means add.
	Mode string `bson:"mode,omitempty"`
}

// IsAtLeast reports whether the entitlement is an at_least alternative.
func (e ProductEntitlement) IsAtLeast() bool { return e.Mode == ModeAtLeast }

// Product is what a user buys: one Stripe price that grants one or more entitlements. A product
// with more than one entitlement is a bundle. Repeatable means it can be bought with a quantity
// (recurring) or more than once (one-off); otherwise a user holds at most one.
type Product struct {
	ID          primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	Slug        string             `bson:"slug" json:"id"`
	Name        string             `bson:"name" json:"name"`
	Description string             `bson:"description" json:"description"`
	Billing     string             `bson:"billing" json:"billing"`
	Repeatable  bool               `bson:"repeatable" json:"repeatable"`
	// PlanGroup names a set of recurring products that replace each other: a user holds at most
	// one active subscription per group. Empty means the product is in no group.
	PlanGroup       string               `bson:"plan_group,omitempty" json:"plan_group,omitempty"`
	StripePriceID   string               `bson:"stripe_price_id,omitempty" json:"stripe_price_id,omitempty"`
	Entitlements    []ProductEntitlement `bson:"entitlements" json:"-"`
	Live            bool                 `bson:"live,omitempty" json:"-"` // see Key.Live
	mcore.Auditable `bson:",inline"`
}

// StripeCustomer maps a user to their Stripe customer so the billing portal can be opened.
type StripeCustomer struct {
	UserID          string `bson:"user_id"`
	CustomerID      string `bson:"customer_id"`
	mcore.Auditable `bson:",inline"`
}

// AuditEntry records one catalog write. The collection is append-only.
type AuditEntry struct {
	ID      primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	At      time.Time          `bson:"at" json:"at"`
	Actor   string             `bson:"actor" json:"actor"`
	Action  string             `bson:"action" json:"action"`
	Kind    string             `bson:"kind" json:"kind"`
	Subject string             `bson:"subject" json:"subject"`
	Before  any                `bson:"before,omitempty" json:"before,omitempty"`
	After   any                `bson:"after,omitempty" json:"after,omitempty"`
}

// ValidateKeyName checks the namespaced key format, e.g. initiative.theme_slots.
func ValidateKeyName(name string) error {
	if len(name) > 128 || !keyPattern.MatchString(name) {
		return fmt.Errorf("%w: key must be namespaced lowercase, like service.feature_name", ErrInvalid)
	}
	return nil
}

// ValidateTierSlug checks a tier identifier.
func ValidateTierSlug(slug string) error {
	if !tierPattern.MatchString(slug) {
		return fmt.Errorf("%w: tier id must be lowercase letters, digits, - or _ (max 64)", ErrInvalid)
	}
	return nil
}

// ValidateProductSlug checks a product identifier; products share the tier id format.
func ValidateProductSlug(slug string) error { return ValidateTierSlug(slug) }

// ValidateMax checks a key's maximum: only limit keys carry one, and it must be a positive
// integer that fits a limit value.
func ValidateMax(t KeyType, max *int64) error {
	if max == nil {
		return nil
	}
	if t != TypeLimit {
		return fmt.Errorf("%w: only limit keys can have a maximum", ErrInvalid)
	}
	if *max < 1 || *max > math.MaxInt32 {
		return fmt.Errorf("%w: maximum must be between 1 and %d", ErrInvalid, math.MaxInt32)
	}
	return nil
}

// ParseValue converts a JSON value to its stored form: flags take true/false, limits take a
// non-negative integer.
func ParseValue(t KeyType, raw any) (int64, error) {
	switch t {
	case TypeFlag:
		b, ok := raw.(bool)
		if !ok {
			return 0, fmt.Errorf("%w: flag values must be true or false", ErrInvalid)
		}
		if b {
			return 1, nil
		}
		return 0, nil
	case TypeLimit:
		f, ok := raw.(float64)
		if !ok || f < 0 || f != math.Trunc(f) || f > math.MaxInt32 {
			return 0, fmt.Errorf("%w: limit values must be non-negative integers", ErrInvalid)
		}
		return int64(f), nil
	}
	return 0, fmt.Errorf("%w: unknown key type %q", ErrInvalid, t)
}

// FormatValue is the inverse of ParseValue.
func FormatValue(t KeyType, v int64) any {
	if t == TypeFlag {
		return v > 0
	}
	return v
}
