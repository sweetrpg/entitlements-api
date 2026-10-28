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
	prods   []*models.Product
	grants  []models.Grant
	audit   []models.AuditEntry
	custs   map[string]string
	events  map[string]bool
	defTier string
	now     func() time.Time
}

// NewMemoryStore returns an empty store using the wall clock.
func NewMemoryStore() *MemoryStore { return &MemoryStore{now: time.Now} }

// AddGrant seeds a grant for tests.
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

func (m *MemoryStore) UpdateKey(_ context.Context, name, description string, ttl int, max *int64, actor string) (*models.Key, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.findKey(name)
	if k == nil {
		return nil, ErrNotFound
	}
	k.Description, k.TTLSeconds, k.Max = description, ttl, max
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

func (m *MemoryStore) findProduct(slug string) *models.Product {
	for _, p := range m.prods {
		if p.Slug == slug && live(p.Auditable) {
			return p
		}
	}
	return nil
}

func (m *MemoryStore) InsertProduct(_ context.Context, p *models.Product, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.findProduct(p.Slug) != nil {
		return ErrDuplicate
	}
	mcore.StampCreate(&p.Auditable, actor, m.now().UTC())
	c := *p
	m.prods = append(m.prods, &c)
	return nil
}

func (m *MemoryStore) GetProducts(_ context.Context, slugs []string) ([]models.Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Product
	for _, p := range m.prods {
		if live(p.Auditable) && slices.Contains(slugs, p.Slug) {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (m *MemoryStore) ListProducts(_ context.Context, limit, skip int64) ([]models.Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Product
	for _, p := range m.prods {
		if live(p.Auditable) {
			out = append(out, *p)
		}
	}
	slices.SortFunc(out, func(a, b models.Product) int { return strings.Compare(a.Slug, b.Slug) })
	return window(out, limit, skip), nil
}

func (m *MemoryStore) UpdateProduct(_ context.Context, in *models.Product, actor string) (*models.Product, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.findProduct(in.Slug)
	if p == nil {
		return nil, ErrNotFound
	}
	p.Name, p.Description, p.Billing, p.Repeatable = in.Name, in.Description, in.Billing, in.Repeatable
	p.StripePriceID, p.Entitlements = in.StripePriceID, in.Entitlements
	mcore.StampUpdate(&p.Auditable, actor, m.now().UTC())
	c := *p
	return &c, nil
}

func (m *MemoryStore) DeleteProduct(_ context.Context, slug, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.findProduct(slug)
	if p == nil {
		return ErrNotFound
	}
	now := m.now().UTC()
	p.DeletedAt, p.DeletedBy = &now, &actor
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

func (m *MemoryStore) GrantsByExternal(_ context.Context, source, externalID string) ([]models.Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Grant
	for _, g := range m.grants {
		if g.Source == source && g.ExternalID == externalID {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *MemoryStore) GrantsByRef(_ context.Context, ref string) ([]models.Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Grant
	for _, g := range m.grants {
		if g.ExternalID == ref || slices.Contains(g.Refs, ref) {
			out = append(out, g)
		}
	}
	return out, nil
}

func (m *MemoryStore) InsertGrant(_ context.Context, g *models.Grant, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range m.grants {
		if g.ExternalID != "" && e.Source == g.Source && e.ExternalID == g.ExternalID && e.Key == g.Key {
			return ErrDuplicate
		}
	}
	mcore.StampCreate(&g.Auditable, actor, m.now().UTC())
	m.grants = append(m.grants, *g)
	return nil
}

func (m *MemoryStore) UpdateGrant(_ context.Context, g *models.Grant, actor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, e := range m.grants {
		if e.Source == g.Source && e.ExternalID == g.ExternalID && e.Key == g.Key {
			mcore.StampUpdate(&g.Auditable, actor, m.now().UTC())
			m.grants[i] = *g
			return nil
		}
	}
	return ErrNotFound
}

func (m *MemoryStore) StripeCustomer(_ context.Context, userID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.custs[userID], nil
}

func (m *MemoryStore) SaveStripeCustomer(_ context.Context, userID, customerID, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.custs == nil {
		m.custs = map[string]string{}
	}
	m.custs[userID] = customerID
	return nil
}

func (m *MemoryStore) EventProcessed(_ context.Context, eventID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events[eventID], nil
}

func (m *MemoryStore) RecordEvent(_ context.Context, eventID, _ string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.events == nil {
		m.events = map[string]bool{}
	}
	m.events[eventID] = true
	return nil
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
