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

	SourceAdmin = "admin"
)

var (
	keyPattern  = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)
	tierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
)

// ErrInvalid marks a validation failure; its message is safe to show an administrator.
var ErrInvalid = errors.New("invalid")

// Key is a registered entitlement key. Type is immutable after creation.
type Key struct {
	ID              primitive.ObjectID `bson:"_id,omitempty" json:"-"`
	Key             string             `bson:"key" json:"key"`
	Type            KeyType            `bson:"type" json:"type"`
	Description     string             `bson:"description" json:"description"`
	TTLSeconds      int                `bson:"ttl_seconds" json:"ttl_seconds"`
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
	ID              primitive.ObjectID `bson:"_id,omitempty"`
	UserID          string             `bson:"user_id"`
	TierID          string             `bson:"tier_id,omitempty"`
	Key             string             `bson:"key,omitempty"`
	Value           int64              `bson:"value,omitempty"`
	Source          string             `bson:"source"`
	ExternalID      string             `bson:"external_id,omitempty"`
	Status          string             `bson:"status"`
	ValidUntil      *time.Time         `bson:"valid_until,omitempty"`
	mcore.Auditable `bson:",inline"`
}

// ActiveAt reports whether the grant counts at now. The Mongo query in data.MongoStore applies
// the same rule.
func (g Grant) ActiveAt(now time.Time) bool {
	return g.Status == GrantActive && (g.ValidUntil == nil || g.ValidUntil.After(now))
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
