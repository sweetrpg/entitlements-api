package data

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sweetrpg/entitlements-api/models"
	mcore "github.com/sweetrpg/model-core.go/models"
)

// MemoryStore is an in-process Store used by tests so they need no database. It mirrors the
// Mongo store's semantics: soft deletes, live-only uniqueness, and the same grant activity rule.
type MemoryStore struct {
	mu      sync.Mutex
	keys    []*models.Key
	tiers   []*models.Tier
	grants  []models.Grant
	audit   []models.AuditEntry
	defTier string
	now     func() time.Time
}

// NewMemoryStore returns an empty store using the wall clock.
func NewMemoryStore() *MemoryStore { return &MemoryStore{now: time.Now} }

// AddGrant seeds a grant; grants are written by the billing flow, not by this service's API.
func (m *MemoryStore) AddGrant(g models.Grant) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.grants = append(m.grants, g)
}

func live(a mcore.Auditable) bool { return a.DeletedAt == nil }

func (m *MemoryStore) InsertKey(_ context.Context, k *models.Key, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findKey(k.Key) != nil {
		return ErrDuplicate
	}
	mcore.StampCreate(&k.Auditable, actor, m.now().UTC())
	c := *k
	m.keys = append(m.keys, &c)
	return nil
}

func (m *MemoryStore) findKey(name string) *models.Key {
	for _, k := range m.keys {
		if k.Key == name && live(k.Auditable) {
			return k
		}
	}
	return nil
}

func (m *MemoryStore) GetKeys(_ context.Context, names []string) ([]models.Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Key
	for _, k := range m.keys {
		if live(k.Auditable) && slices.Contains(names, k.Key) {
			out = append(out, *k)
		}
	}
	return out, nil
}

func (m *MemoryStore) ListKeys(_ context.Context, prefix string, limit, skip int64) ([]models.Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Key
	for _, k := range m.keys {
		if live(k.Auditable) && strings.HasPrefix(k.Key, prefix) {
			out = append(out, *k)
		}
	}
	slices.SortFunc(out, func(a, b models.Key) int { return strings.Compare(a.Key, b.Key) })
	return window(out, limit, skip), nil
}

func (m *MemoryStore) UpdateKey(_ context.Context, name, description string, ttl int, actor string) (*models.Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.findKey(name)
	if k == nil {
		return nil, ErrNotFound
	}
	k.Description, k.TTLSeconds = description, ttl
	mcore.StampUpdate(&k.Auditable, actor, m.now().UTC())
	c := *k
	return &c, nil
}

func (m *MemoryStore) DeleteKey(_ context.Context, name, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.findKey(name)
	if k == nil {
		return ErrNotFound
	}
	now := m.now().UTC()
	k.DeletedAt, k.DeletedBy = &now, &actor
	return nil
}

func (m *MemoryStore) findTier(slug string) *models.Tier {
	for _, t := range m.tiers {
		if t.Slug == slug && live(t.Auditable) {
			return t
		}
	}
	return nil
}

func (m *MemoryStore) InsertTier(_ context.Context, t *models.Tier, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findTier(t.Slug) != nil {
		return ErrDuplicate
	}
	mcore.StampCreate(&t.Auditable, actor, m.now().UTC())
	c := *t
	m.tiers = append(m.tiers, &c)
	return nil
}

func (m *MemoryStore) GetTiers(_ context.Context, slugs []string) ([]models.Tier, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Tier
	for _, t := range m.tiers {
		if live(t.Auditable) && slices.Contains(slugs, t.Slug) {
			out = append(out, *t)
		}
	}
	return out, nil
}

func (m *MemoryStore) ListTiers(_ context.Context, limit, skip int64) ([]models.Tier, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Tier
	for _, t := range m.tiers {
		if live(t.Auditable) {
			out = append(out, *t)
		}
	}
	slices.SortFunc(out, func(a, b models.Tier) int { return strings.Compare(a.Slug, b.Slug) })
	return window(out, limit, skip), nil
}

func (m *MemoryStore) UpdateTier(_ context.Context, in *models.Tier, actor string) (*models.Tier, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.findTier(in.Slug)
	if t == nil {
		return nil, ErrNotFound
	}
	t.Name, t.Description, t.Values, t.StripePriceID = in.Name, in.Description, in.Values, in.StripePriceID
	mcore.StampUpdate(&t.Auditable, actor, m.now().UTC())
	c := *t
	return &c, nil
}

func (m *MemoryStore) DeleteTier(_ context.Context, slug, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.findTier(slug)
	if t == nil {
		return ErrNotFound
	}
	now := m.now().UTC()
	t.DeletedAt, t.DeletedBy = &now, &actor
	return nil
}

func (m *MemoryStore) DefaultTierID(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.defTier, nil
}

func (m *MemoryStore) SetDefaultTierID(_ context.Context, slug, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defTier = slug
	return nil
}

func (m *MemoryStore) ActiveGrants(_ context.Context, userID string, now time.Time) ([]models.Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Grant
	for _, g := range m.grants {
		if g.UserID == userID && g.ActiveAt(now) {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *MemoryStore) AppendAudit(_ context.Context, e models.AuditEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, e)
	return nil
}

func (m *MemoryStore) ListAudit(_ context.Context, limit, skip int64) ([]models.AuditEntry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := slices.Clone(m.audit)
	slices.Reverse(out)
	return window(out, limit, skip), nil
}

func (m *MemoryStore) Ping(context.Context) error { return nil }

// window applies skip then limit; limit <= 0 means no limit.
func window[T any](s []T, limit, skip int64) []T {
	if skip >= int64(len(s)) {
		return nil
	}
	s = s[skip:]
	if limit > 0 && limit < int64(len(s)) {
		s = s[:limit]
	}
	return s
}
