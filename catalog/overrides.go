package catalog

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sweetrpg/common.go/logging"
	"github.com/sweetrpg/entitlements-api/constants"
	"github.com/sweetrpg/entitlements-api/models"
)

// userIDPattern is the shape of a canonical user ID; it never contains the "/" that separates
// the user from the key in an audit subject.
var userIDPattern = regexp.MustCompile(`^[A-Za-z0-9._|:@-]{1,128}$`)

// MaxReasonLength caps the free-text reason an administrator gives for a write.
const MaxReasonLength = 500

// OverrideInput is an administrator's request to pin a user's value for one key. Value is a JSON
// value: a boolean for a flag, a non-negative integer for a limit.
type OverrideInput struct {
	Value      any
	ValidUntil *time.Time
	Reason     string
	// AcknowledgePaidReduction and Reference are both required to lower the value below what the
	// user paid for: the administrator confirms the reduction affects a purchase and says why.
	AcknowledgePaidReduction bool
	Reference                string
}

// PaidReductionError rejects an override that would lower a value below its paid value without
// acknowledgment. Paid and Proposed are in stored units (a flag is 0 or 1).
type PaidReductionError struct {
	Key      models.Key
	Paid     int64
	Proposed int64
	Payments []models.Grant
}

func (e *PaidReductionError) Error() string {
	return fmt.Sprintf("override of %s would reduce the paid value %d to %d; acknowledge the paid reduction with a reference to apply it", e.Key.Key, e.Paid, e.Proposed)
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
	reduced, err := s.checkPaidReduction(ctx, userID, keys[0], value, in)
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
	entry := models.AuditEntry{Reason: reason, Before: before, After: after}
	if reduced != nil {
		entry.PaidReduction, entry.Paid, entry.Reference = true, models.FormatValue(keys[0].Type, *reduced), strings.TrimSpace(in.Reference)
	}
	return g, s.auditOverride(ctx, actor, "set", userID, key, entry)
}

// checkPaidReduction returns the paid value when value would lower the user's effective value
// below it (nil when it would not), or an error when that reduction is not allowed.
func (s *Service) checkPaidReduction(ctx context.Context, userID string, k models.Key, value int64, in OverrideInput) (*int64, error) {
	u, err := s.loadUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	paid, payments := u.paid(k)
	paid = clamp(k, paid)
	proposed := clamp(k, value)
	if len(payments) == 0 || proposed >= paid {
		return nil, nil
	}
	if !in.AcknowledgePaidReduction || strings.TrimSpace(in.Reference) == "" {
		return nil, &PaidReductionError{Key: k, Paid: paid, Proposed: proposed, Payments: payments}
	}
	if !s.paidReductions {
		return nil, ErrPaidReductionsDisabled
	}
	return &paid, nil
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
		return s.auditOverride(ctx, actor, "remove", userID, key, models.AuditEntry{Reason: reason, Before: before, After: after})
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

// UserAudit lists the audit entries about one user's overrides, newest first.
func (s *Service) UserAudit(ctx context.Context, userID string, limit, skip int64) ([]models.AuditEntry, error) {
	if !userIDPattern.MatchString(userID) {
		return nil, fmt.Errorf("%w: user id must be 1 to 128 letters, digits or . _ | : @ -", models.ErrInvalid)
	}
	if limit < 1 || limit > constants.MaxAuditPage {
		limit = constants.MaxAuditPage
	}
	return s.store.ListAuditBySubjectPrefix(ctx, userID+"/", limit, max(skip, 0))
}

// PaidReductions lists the acknowledged overrides that lowered a paid value, newest first.
func (s *Service) PaidReductions(ctx context.Context, limit, skip int64) ([]models.AuditEntry, error) {
	return s.store.ListPaidReductions(ctx, limit, skip)
}

// effective returns the clamped effective value of one key, rendered per key type.
func (s *Service) effective(ctx context.Context, userID string, k models.Key) (any, error) {
	r, err := s.resolveKeys(ctx, userID, []string{k.Key}, []models.Key{k})
	if err != nil {
		return nil, err
	}
	return models.FormatValue(k.Type, r[0].Value), nil
}

func (s *Service) auditOverride(ctx context.Context, actor, action, userID, key string, e models.AuditEntry) error {
	e.At, e.Actor, e.Action, e.Kind, e.Subject = s.now().UTC(), actor, action, "override", userID+"/"+key
	if err := s.store.AppendAudit(ctx, e); err != nil {
		logging.Logger.Error("catalog audit entry not recorded", "action", action, "kind", "override", "subject", e.Subject, "actor", actor, "error", err.Error())
	}
	return nil
}
