package catalog

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sweetrpg/entitlements-api/models"
)

// MaxReasonLength caps the free-text reason an administrator gives for a write.
const MaxReasonLength = 500

// OverrideInput is an administrator's request to pin a user's value for one key. Value is a JSON
// value: a boolean for a flag, a non-negative integer for a limit.
type OverrideInput struct {
	Value      any
	ValidUntil *time.Time
	Reason     string
}

// overrideExternalID makes (source, external_id, key) unique per user and key, so setting an
// override again replaces the earlier one instead of adding a second.
func overrideExternalID(userID string) string { return "override:" + userID }

func validateReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "", fmt.Errorf("%w: reason is required", models.ErrInvalid)
	}
	if len(reason) > MaxReasonLength {
		return "", fmt.Errorf("%w: reason is at most %d characters", models.ErrInvalid, MaxReasonLength)
	}
	return reason, nil
}

func validateUser(userID string) error {
	if strings.TrimSpace(userID) == "" {
		return fmt.Errorf("%w: user id is required", models.ErrInvalid)
	}
	return nil
}

// SetOverride makes value the user's effective value for key (still clamped to the key maximum)
// until validUntil, replacing any earlier override for the same user and key. It never changes
// or ends any other grant.
func (s *Service) SetOverride(ctx context.Context, actor, userID, key string, in OverrideInput) (*models.Grant, error) {
	reason, err := validateReason(in.Reason)
	if err != nil {
		return nil, err
	}
	if err := validateUser(userID); err != nil {
		return nil, err
	}
	if in.ValidUntil != nil && !in.ValidUntil.After(s.now()) {
		return nil, fmt.Errorf("%w: valid_until must be in the future", models.ErrInvalid)
	}
	keys, err := s.store.GetKeys(ctx, []string{key})
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, UnknownKeysError{Keys: []string{key}}
	}
	value, err := models.ParseValue(keys[0].Type, in.Value)
	if err != nil {
		return nil, err
	}
	before, err := s.effective(ctx, userID, keys[0])
	if err != nil {
		return nil, err
	}

	g, err := s.upsertOverride(ctx, actor, userID, key, value, in.ValidUntil, reason)
	if err != nil {
		return nil, err
	}
	after, err := s.effective(ctx, userID, keys[0])
	if err != nil {
		return nil, err
	}
	return g, s.auditOverride(ctx, actor, "set", userID, key, reason, before, after)
}

func (s *Service) upsertOverride(ctx context.Context, actor, userID, key string, value int64, until *time.Time, reason string) (*models.Grant, error) {
	ext := overrideExternalID(userID)
	existing, err := s.store.GrantsByExternal(ctx, models.SourceAdmin, ext)
	if err != nil {
		return nil, err
	}
	g := &models.Grant{
		UserID: userID, Key: key, Value: value, Mode: models.ModeOverride, Source: models.SourceAdmin,
		ExternalID: ext, Status: models.GrantActive, ValidUntil: until, Reason: reason,
	}
	for _, e := range existing {
		if e.Key == key {
			g.ID, g.Auditable = e.ID, e.Auditable
			return g, s.store.UpdateGrant(ctx, g, actor)
		}
	}
	return g, s.store.InsertGrant(ctx, g, actor)
}

// RemoveOverride ends the user's active override for key, restoring the computed value. It ends
// the override only; no other grant is touched. ErrNotFound when the user has none.
func (s *Service) RemoveOverride(ctx context.Context, actor, userID, key, reason string) error {
	reason, err := validateReason(reason)
	if err != nil {
		return err
	}
	if err := validateUser(userID); err != nil {
		return err
	}
	keys, err := s.store.GetKeys(ctx, []string{key})
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return UnknownKeysError{Keys: []string{key}}
	}
	grants, err := s.store.GrantsByExternal(ctx, models.SourceAdmin, overrideExternalID(userID))
	if err != nil {
		return err
	}
	now := s.now().UTC()
	for _, g := range grants {
		if g.Key != key || !g.ActiveAt(now) {
			continue
		}
		before, err := s.effective(ctx, userID, keys[0])
		if err != nil {
			return err
		}
		g.Status = models.GrantEnded
		if err := s.store.UpdateGrant(ctx, &g, actor); err != nil {
			return err
		}
		after, err := s.effective(ctx, userID, keys[0])
		if err != nil {
			return err
		}
		return s.auditOverride(ctx, actor, "remove", userID, key, reason, before, after)
	}
	return ErrNotFound
}

// GrantView is a grant with the type of its key, so a flag value can be shown as a boolean.
type GrantView struct {
	models.Grant
	Type models.KeyType
}

// UserGrants returns the user's active grants, overrides included.
func (s *Service) UserGrants(ctx context.Context, userID string) ([]GrantView, error) {
	if err := validateUser(userID); err != nil {
		return nil, err
	}
	grants, err := s.store.ActiveGrants(ctx, userID, s.now().UTC())
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, g := range grants {
		if g.Key != "" {
			names = append(names, g.Key)
		}
	}
	keys, err := s.store.GetKeys(ctx, dedupe(names))
	if err != nil {
		return nil, err
	}
	types := make(map[string]models.KeyType, len(keys))
	for _, k := range keys {
		types[k.Key] = k.Type
	}
	out := make([]GrantView, len(grants))
	for i, g := range grants {
		out[i] = GrantView{Grant: g, Type: types[g.Key]}
	}
	return out, nil
}

// effective returns the clamped effective value of one key, rendered per key type.
func (s *Service) effective(ctx context.Context, userID string, k models.Key) (any, error) {
	r, err := s.resolveKeys(ctx, userID, []string{k.Key}, []models.Key{k})
	if err != nil {
		return nil, err
	}
	return models.FormatValue(k.Type, r[0].Value), nil
}

func (s *Service) auditOverride(ctx context.Context, actor, action, userID, key, reason string, before, after any) error {
	return s.auditWithReason(ctx, actor, action, "override", userID+"/"+key, reason, before, after)
}
