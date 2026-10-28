// Package catalog holds the entitlements business rules: admin-only catalog writes with an audit
// trail, and effective-value resolution for checks.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/sweetrpg/common.go/logging"
	"github.com/sweetrpg/entitlements-api/constants"
	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

// Sentinel errors handlers map to HTTP statuses. Validation failures wrap models.ErrInvalid.
var (
	ErrNotFound  = data.ErrNotFound
	ErrDuplicate = data.ErrDuplicate
	// ErrInUse means the change would orphan something that still depends on the target.
	ErrInUse = errors.New("in use")
)

// Service owns catalog writes and checks.
type Service struct {
	store data.Store
	now   func() time.Time
}

// New returns a Service over store.
func New(store data.Store) *Service { return &Service{store: store, now: time.Now} }

// KeyInput is the writable part of a key.
type KeyInput struct {
	Key         string
	Type        models.KeyType
	Description string
	TTLSeconds  int
}

// TierInput is the writable part of a tier. Values maps key name to a JSON bool or number.
type TierInput struct {
	Slug          string
	Name          string
	Description   string
	Values        map[string]any
	StripePriceID string
}

func validateTTL(ttl int) error {
	if ttl < 0 || ttl > constants.MaxTTLSeconds {
		return fmt.Errorf("%w: ttl_seconds must be between 0 and %d", models.ErrInvalid, constants.MaxTTLSeconds)
	}
	return nil
}

// audit appends the trail entry after a successful write. The write and the entry are not one
// transaction: if the append fails the change is already persisted, so the failure is logged at
// ERROR and the write still reports success. Upgrade path: wrap both in a Mongo transaction if
// lost entries ever matter.
func (s *Service) audit(ctx context.Context, actor, action, kind, subject string, before, after any) error {
	err := s.store.AppendAudit(ctx, models.AuditEntry{
		At: s.now().UTC(), Actor: actor, Action: action, Kind: kind, Subject: subject, Before: before, After: after,
	})
	if err != nil {
		logging.Logger.Error("catalog audit entry not recorded", "action", action, "kind", kind, "subject", subject, "actor", actor, "error", err.Error())
	}
	return nil
}

// CreateKey registers a key. A live key with the same name is rejected.
func (s *Service) CreateKey(ctx context.Context, actor string, in KeyInput) (*models.Key, error) {
	if err := models.ValidateKeyName(in.Key); err != nil {
		return nil, err
	}
	if in.Type != models.TypeFlag && in.Type != models.TypeLimit {
		return nil, fmt.Errorf("%w: type must be flag or limit", models.ErrInvalid)
	}
	if err := validateTTL(in.TTLSeconds); err != nil {
		return nil, err
	}
	k := &models.Key{Key: in.Key, Type: in.Type, Description: in.Description, TTLSeconds: in.TTLSeconds}
	if err := s.store.InsertKey(ctx, k, actor); err != nil {
		return nil, err
	}
	return k, s.audit(ctx, actor, "create", "key", k.Key, nil, k)
}

// UpdateKey changes a key's description and TTL. Its type cannot change.
func (s *Service) UpdateKey(ctx context.Context, actor, name, description string, ttl int) (*models.Key, error) {
	if err := validateTTL(ttl); err != nil {
		return nil, err
	}
	before, err := s.store.GetKeys(ctx, []string{name})
	if err != nil {
		return nil, err
	}
	if len(before) == 0 {
		return nil, ErrNotFound
	}
	k, err := s.store.UpdateKey(ctx, name, description, ttl, actor)
	if err != nil {
		return nil, err
	}
	return k, s.audit(ctx, actor, "update", "key", name, before[0], k)
}

// DeleteKey removes a key from the catalog. Tier values that still name it stay stored but are
// never returned, because a check resolves only live keys.
func (s *Service) DeleteKey(ctx context.Context, actor, name string) error {
	if err := s.store.DeleteKey(ctx, name, actor); err != nil {
		return err
	}
	return s.audit(ctx, actor, "delete", "key", name, nil, nil)
}

// ListKeys returns live keys under prefix, ordered by name.
func (s *Service) ListKeys(ctx context.Context, prefix string, limit, skip int64) ([]models.Key, error) {
	return s.store.ListKeys(ctx, prefix, limit, skip)
}

// buildValues validates every named key exists and the value matches its type.
func (s *Service) buildValues(ctx context.Context, raw map[string]any) ([]models.TierValue, error) {
	names := make([]string, 0, len(raw))
	for n := range raw {
		names = append(names, n)
	}
	slices.Sort(names)
	keys, err := s.store.GetKeys(ctx, names)
	if err != nil {
		return nil, err
	}
	byName := map[string]models.Key{}
	for _, k := range keys {
		byName[k.Key] = k
	}
	out := make([]models.TierValue, 0, len(names))
	for _, n := range names {
		k, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("%w: unknown key %q", models.ErrInvalid, n)
		}
		v, err := models.ParseValue(k.Type, raw[n])
		if err != nil {
			return nil, fmt.Errorf("%w (key %q)", err, n)
		}
		out = append(out, models.TierValue{Key: n, Type: k.Type, Value: v})
	}
	return out, nil
}

func (s *Service) tierFromInput(ctx context.Context, in TierInput) (*models.Tier, error) {
	if err := models.ValidateTierSlug(in.Slug); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name is required", models.ErrInvalid)
	}
	values, err := s.buildValues(ctx, in.Values)
	if err != nil {
		return nil, err
	}
	return &models.Tier{Slug: in.Slug, Name: in.Name, Description: in.Description, Values: values, StripePriceID: in.StripePriceID}, nil
}

// CreateTier adds a tier.
func (s *Service) CreateTier(ctx context.Context, actor string, in TierInput) (*models.Tier, error) {
	t, err := s.tierFromInput(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := s.store.InsertTier(ctx, t, actor); err != nil {
		return nil, err
	}
	return t, s.audit(ctx, actor, "create", "tier", t.Slug, nil, t)
}

// UpdateTier replaces a tier's name, description, values, and Stripe price ID.
func (s *Service) UpdateTier(ctx context.Context, actor string, in TierInput) (*models.Tier, error) {
	t, err := s.tierFromInput(ctx, in)
	if err != nil {
		return nil, err
	}
	before, err := s.store.GetTiers(ctx, []string{in.Slug})
	if err != nil {
		return nil, err
	}
	if len(before) == 0 {
		return nil, ErrNotFound
	}
	updated, err := s.store.UpdateTier(ctx, t, actor)
	if err != nil {
		return nil, err
	}
	return updated, s.audit(ctx, actor, "update", "tier", in.Slug, before[0], updated)
}

// DeleteTier removes a tier. The default tier cannot be deleted; pick another default first.
func (s *Service) DeleteTier(ctx context.Context, actor, slug string) error {
	def, err := s.store.DefaultTierID(ctx)
	if err != nil {
		return err
	}
	if def == slug {
		return fmt.Errorf("%w: tier is the default tier", ErrInUse)
	}
	if err := s.store.DeleteTier(ctx, slug, actor); err != nil {
		return err
	}
	return s.audit(ctx, actor, "delete", "tier", slug, nil, nil)
}

// ListTiers returns live tiers ordered by id.
func (s *Service) ListTiers(ctx context.Context, limit, skip int64) ([]models.Tier, error) {
	return s.store.ListTiers(ctx, limit, skip)
}

// SetDefaultTier makes an existing tier the one every user implicitly holds.
func (s *Service) SetDefaultTier(ctx context.Context, actor, slug string) error {
	tiers, err := s.store.GetTiers(ctx, []string{slug})
	if err != nil {
		return err
	}
	if len(tiers) == 0 {
		return ErrNotFound
	}
	before, err := s.store.DefaultTierID(ctx)
	if err != nil {
		return err
	}
	if err := s.store.SetDefaultTierID(ctx, slug, actor); err != nil {
		return err
	}
	return s.audit(ctx, actor, "set_default", "default_tier", slug, before, slug)
}

// DefaultTier returns the default tier id, or "" when none is configured.
func (s *Service) DefaultTier(ctx context.Context) (string, error) {
	return s.store.DefaultTierID(ctx)
}

// ListAudit returns audit entries newest first.
func (s *Service) ListAudit(ctx context.Context, limit, skip int64) ([]models.AuditEntry, error) {
	return s.store.ListAudit(ctx, limit, skip)
}
