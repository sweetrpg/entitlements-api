package catalog

import (
	"errors"
	"testing"

	"github.com/sweetrpg/entitlements-api/models"
)

func productInput() ProductInput {
	return ProductInput{
		Slug: "themes", Name: "Themes", Billing: models.BillingOneOff, StripePriceID: "price_x",
		Entitlements: map[string]any{"initiative.deluxe": true, "initiative.theme_slots": 1.0},
	}
}

func TestCreateBundleStoresTypedEntitlements(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	p, err := s.CreateProduct(ctx, "admin", productInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entitlements) != 2 {
		t.Fatalf("entitlements = %+v", p.Entitlements)
	}
	for _, e := range p.Entitlements {
		if (e.Key == "initiative.deluxe") != (e.Type == models.TypeFlag) || e.Value != 1 {
			t.Errorf("entitlement = %+v", e)
		}
	}
}

func TestProductValidation(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	mutate := map[string]func(*ProductInput){
		"unknown key":       func(p *ProductInput) { p.Entitlements = map[string]any{"no.such": 1.0} },
		"no entitlements":   func(p *ProductInput) { p.Entitlements = nil },
		"flag set false":    func(p *ProductInput) { p.Entitlements = map[string]any{"initiative.deluxe": false} },
		"limit of zero":     func(p *ProductInput) { p.Entitlements = map[string]any{"initiative.theme_slots": 0.0} },
		"flag given number": func(p *ProductInput) { p.Entitlements = map[string]any{"initiative.deluxe": 2.0} },
		"bad billing":       func(p *ProductInput) { p.Billing = "weekly" },
		"bad slug":          func(p *ProductInput) { p.Slug = "Not A Slug" },
		"blank name":        func(p *ProductInput) { p.Name = " " },
	}
	for name, f := range mutate {
		t.Run(name, func(t *testing.T) {
			in := productInput()
			f(&in)
			if _, err := s.CreateProduct(ctx, "admin", in); !errors.Is(err, models.ErrInvalid) {
				t.Errorf("err = %v, want invalid", err)
			}
		})
	}
}

func TestProductDuplicateAndLifecycleAreAudited(t *testing.T) {
	s, _ := newSvc(t)
	seed(t, s)
	if _, err := s.CreateProduct(ctx, "admin", productInput()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProduct(ctx, "admin", productInput()); !errors.Is(err, ErrDuplicate) {
		t.Errorf("err = %v, want duplicate", err)
	}
	in := productInput()
	in.Name = "Themes v2"
	in.Repeatable = true
	updated, err := s.UpdateProduct(ctx, "admin2", in)
	if err != nil || updated.Name != "Themes v2" || !updated.Repeatable || updated.UpdatedBy != "admin2" {
		t.Fatalf("updated = %+v err = %v", updated, err)
	}
	if _, err := s.UpdateProduct(ctx, "admin", ProductInput{Slug: "ghost", Name: "x", Billing: models.BillingOneOff, Entitlements: in.Entitlements}); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want not found", err)
	}
	if err := s.DeleteProduct(ctx, "admin", "themes"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListProducts(ctx, 50, 0); len(list) != 0 {
		t.Errorf("list = %v", list)
	}
	if err := s.DeleteProduct(ctx, "admin", "themes"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want not found", err)
	}
	audit, _ := s.ListAudit(ctx, 50, 0)
	var kinds []string
	for _, e := range audit {
		if e.Kind == "product" {
			kinds = append(kinds, e.Action)
		}
	}
	if len(kinds) != 3 {
		t.Errorf("product audit actions = %v, want create, update, delete", kinds)
	}
}
