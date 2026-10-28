package catalog

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/sweetrpg/entitlements-api/models"
)

// ProductInput is the writable part of a product. Entitlements maps key name to a JSON bool or
// number: the value granted per unit purchased.
type ProductInput struct {
	Slug          string
	Name          string
	Description   string
	Billing       string
	Repeatable    bool
	StripePriceID string
	Entitlements  map[string]any
}

// buildEntitlements validates every key exists and the value matches its type. A limit
// entitlement must add at least one unit; a flag entitlement must be true.
func (s *Service) buildEntitlements(ctx context.Context, raw map[string]any) ([]models.ProductEntitlement, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: a product needs at least one entitlement", models.ErrInvalid)
	}
	values, err := s.buildValues(ctx, raw)
	if err != nil {
		return nil, err
	}
	out := make([]models.ProductEntitlement, len(values))
	for i, v := range values {
		if v.Value < 1 {
			return nil, fmt.Errorf("%w: entitlement %q must grant at least 1", models.ErrInvalid, v.Key)
		}
		out[i] = models.ProductEntitlement(v)
	}
	return out, nil
}

func (s *Service) productFromInput(ctx context.Context, in ProductInput) (*models.Product, error) {
	if err := models.ValidateProductSlug(in.Slug); err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name is required", models.ErrInvalid)
	}
	if !slices.Contains([]string{models.BillingOneOff, models.BillingRecurring}, in.Billing) {
		return nil, fmt.Errorf("%w: billing must be one_off or recurring", models.ErrInvalid)
	}
	ents, err := s.buildEntitlements(ctx, in.Entitlements)
	if err != nil {
		return nil, err
	}
	return &models.Product{
		Slug: in.Slug, Name: in.Name, Description: in.Description, Billing: in.Billing,
		Repeatable: in.Repeatable, StripePriceID: in.StripePriceID, Entitlements: ents,
	}, nil
}

// CreateProduct adds a product. It can be purchased as soon as it has a Stripe price ID.
func (s *Service) CreateProduct(ctx context.Context, actor string, in ProductInput) (*models.Product, error) {
	p, err := s.productFromInput(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := s.store.InsertProduct(ctx, p, actor); err != nil {
		return nil, err
	}
	return p, s.audit(ctx, actor, "create", "product", p.Slug, nil, p)
}

// UpdateProduct replaces a product's fields. Grants already issued keep the values they were
// created with, except subscription quantity changes, which recompute from the current product.
func (s *Service) UpdateProduct(ctx context.Context, actor string, in ProductInput) (*models.Product, error) {
	p, err := s.productFromInput(ctx, in)
	if err != nil {
		return nil, err
	}
	before, err := s.store.GetProducts(ctx, []string{in.Slug})
	if err != nil {
		return nil, err
	}
	if len(before) == 0 {
		return nil, ErrNotFound
	}
	updated, err := s.store.UpdateProduct(ctx, p, actor)
	if err != nil {
		return nil, err
	}
	return updated, s.audit(ctx, actor, "update", "product", in.Slug, before[0], updated)
}

// DeleteProduct removes a product from the catalog. Existing grants are unaffected.
func (s *Service) DeleteProduct(ctx context.Context, actor, slug string) error {
	if err := s.store.DeleteProduct(ctx, slug, actor); err != nil {
		return err
	}
	return s.audit(ctx, actor, "delete", "product", slug, nil, nil)
}

// ListProducts returns live products ordered by id.
func (s *Service) ListProducts(ctx context.Context, limit, skip int64) ([]models.Product, error) {
	return s.store.ListProducts(ctx, limit, skip)
}
