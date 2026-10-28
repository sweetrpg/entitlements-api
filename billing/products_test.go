package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

func i64(v int64) *int64 { return &v }

// seedProducts registers the keys, a free tier, and three products: a recurring single-key
// product with a quantity, a one-off bundle (two flags and a slot), and a repeatable one-off.
func seedProducts(t *testing.T, st *data.MemoryStore) *catalog.Service {
	t.Helper()
	cat := catalog.New(st)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []catalog.KeyInput{
		{Key: "initiative.saved_encounters", Type: models.TypeLimit, Max: i64(20)},
		{Key: "initiative.theme_slots", Type: models.TypeLimit},
		{Key: "initiative.packaged_themes", Type: models.TypeFlag},
		{Key: "initiative.customization", Type: models.TypeFlag},
	} {
		_, err := cat.CreateKey(ctx, "admin", k)
		must(err)
	}
	_, err := cat.CreateTier(ctx, "admin", catalog.TierInput{Slug: "free", Name: "Free", Values: map[string]any{
		"initiative.saved_encounters": 1.0, "initiative.theme_slots": 0.0,
		"initiative.packaged_themes": false, "initiative.customization": false,
	}})
	must(err)
	must(cat.SetDefaultTier(ctx, "admin", "free"))
	for _, p := range []catalog.ProductInput{
		{Slug: "encounters", Name: "Encounters", Billing: models.BillingRecurring, Repeatable: true, StripePriceID: "price_enc",
			Entitlements: map[string]any{"initiative.saved_encounters": 5.0}},
		{Slug: "themes", Name: "Themes", Billing: models.BillingOneOff, StripePriceID: "price_themes",
			Entitlements: map[string]any{"initiative.packaged_themes": true, "initiative.customization": true, "initiative.theme_slots": 1.0}},
		{Slug: "theme-slot", Name: "Theme slot", Billing: models.BillingOneOff, Repeatable: true, StripePriceID: "price_slot",
			Entitlements: map[string]any{"initiative.theme_slots": 1.0}},
		{Slug: "unpriced", Name: "Unpriced", Billing: models.BillingOneOff,
			Entitlements: map[string]any{"initiative.customization": true}},
	} {
		_, err := cat.CreateProduct(ctx, "admin", p)
		must(err)
	}
	return cat
}

func productSetup(t *testing.T) (*Service, *catalog.Service, *data.MemoryStore, *fakeGateway) {
	t.Helper()
	s, st, gw := setup(t)
	cat := seedProducts(t, st)
	s.limits = cat
	return s, cat, st, gw
}

func effective(t *testing.T, cat *catalog.Service, user, key string) any {
	t.Helper()
	r, err := cat.Check(context.Background(), user, []string{key})
	if err != nil {
		t.Fatal(err)
	}
	return models.FormatValue(r[0].Type, r[0].Value)
}

func grantsOf(t *testing.T, st *data.MemoryStore, source, ext string) map[string]models.Grant {
	t.Helper()
	gs, err := st.GrantsByExternal(context.Background(), source, ext)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]models.Grant{}
	for _, g := range gs {
		out[g.Key] = g
	}
	return out
}

func TestProductCheckoutPassesPriceProductAndQuantity(t *testing.T) {
	s, _, _, gw := productSetup(t)
	if _, err := s.Checkout(context.Background(), "user-3", "", CheckoutRequest{ProductID: "encounters", Quantity: 2}); err != nil {
		t.Fatal(err)
	}
	if gw.checkout.PriceID != "price_enc" || gw.checkout.ProductID != "encounters" || gw.checkout.Quantity != 2 || gw.checkout.TierID != "" {
		t.Errorf("params = %+v", gw.checkout)
	}
	if _, err := s.Checkout(context.Background(), "user-3", "", CheckoutRequest{ProductID: "theme-slot"}); err != nil || gw.checkout.Quantity != 1 {
		t.Errorf("default quantity = %d err = %v", gw.checkout.Quantity, err)
	}
}

func TestProductCheckoutRejectsWhatIsNotPurchasable(t *testing.T) {
	s, _, _, gw := productSetup(t)
	cases := map[string]CheckoutRequest{
		"neither":                  {},
		"both":                     {TierID: "free", ProductID: "encounters"},
		"unknown product":          {ProductID: "ghost"},
		"no stripe price":          {ProductID: "unpriced"},
		"negative quantity":        {ProductID: "encounters", Quantity: -1},
		"quantity over the cap":    {ProductID: "encounters", Quantity: 101},
		"quantity on a single-buy": {ProductID: "themes", Quantity: 2},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Checkout(context.Background(), "user-3", "", req); !errors.Is(err, models.ErrInvalid) {
				t.Errorf("err = %v, want invalid", err)
			}
		})
	}
	if gw.checkout.PriceID != "" {
		t.Error("a Stripe session was created for a rejected request")
	}
}

func TestCheckoutRefusesPurchaseThatPassesTheMaximum(t *testing.T) {
	s, _, _, gw := productSetup(t)
	ctx := context.Background()
	// free tier gives 1, maximum 20: three units of +5 fit (16), four do not (21).
	if _, err := s.Checkout(ctx, "user-3", "", CheckoutRequest{ProductID: "encounters", Quantity: 3}); err != nil {
		t.Fatalf("fitting purchase refused: %v", err)
	}
	gw.checkout = CheckoutParams{}
	_, err := s.Checkout(ctx, "user-3", "", CheckoutRequest{ProductID: "encounters", Quantity: 4})
	var exceeded catalog.MaxExceededError
	if !errors.As(err, &exceeded) || exceeded.Remaining != 19 || exceeded.Key != "initiative.saved_encounters" {
		t.Fatalf("err = %v, want exceeded with 19 remaining", err)
	}
	if gw.checkout.PriceID != "" {
		t.Error("a Stripe session was created for a refused purchase")
	}
}

func TestCheckoutCountsUnitsAlreadyHeld(t *testing.T) {
	s, _, st, _ := productSetup(t)
	apply(t, s, "product_subscription_checkout.json") // quantity 2 = +10, holds 11 of 20
	_, err := s.Checkout(context.Background(), "user-3", "", CheckoutRequest{ProductID: "encounters", Quantity: 2})
	var exceeded catalog.MaxExceededError
	if !errors.As(err, &exceeded) || exceeded.Remaining != 9 {
		t.Errorf("err = %v, want 9 remaining", err)
	}
	if got := grantsOf(t, st, models.SourceStripeSubscription, "sub_p1"); len(got) != 1 {
		t.Errorf("grants = %v", got)
	}
}

func TestSingleBuyProductCannotBeBoughtTwiceUntilRefunded(t *testing.T) {
	s, _, _, _ := productSetup(t)
	ctx := context.Background()
	if _, err := s.Checkout(ctx, "user-3", "", CheckoutRequest{ProductID: "themes"}); err != nil {
		t.Fatal(err)
	}
	apply(t, s, "product_bundle_checkout.json")
	if _, err := s.Checkout(ctx, "user-3", "", CheckoutRequest{ProductID: "themes"}); !errors.Is(err, catalog.ErrInUse) {
		t.Fatalf("err = %v, want in use", err)
	}
	apply(t, s, "product_bundle_refunded.json")
	if _, err := s.Checkout(ctx, "user-3", "", CheckoutRequest{ProductID: "themes"}); err != nil {
		t.Errorf("repurchase after refund refused: %v", err)
	}
}

func TestSubscriptionProductGrantsValueTimesQuantity(t *testing.T) {
	s, cat, st, _ := productSetup(t)
	apply(t, s, "product_subscription_checkout.json")
	g := grantsOf(t, st, models.SourceStripeSubscription, "sub_p1")["initiative.saved_encounters"]
	if g.Value != 10 || g.Quantity != 2 || g.ProductID != "encounters" || g.UserID != "user-3" {
		t.Errorf("grant = %+v", g)
	}
	if v := effective(t, cat, "user-3", "initiative.saved_encounters"); v != int64(11) {
		t.Errorf("effective = %v, want free 1 + 10", v)
	}
}

func TestBundleCreatesOneGrantPerEntitlementSharingTheExternalID(t *testing.T) {
	s, cat, st, _ := productSetup(t)
	apply(t, s, "product_bundle_checkout.json")
	got := grantsOf(t, st, models.SourceStripePurchase, "pi_b1")
	if len(got) != 3 {
		t.Fatalf("grants = %v, want 3", got)
	}
	for _, key := range []string{"initiative.packaged_themes", "initiative.customization", "initiative.theme_slots"} {
		if g, ok := got[key]; !ok || g.ProductID != "themes" || g.Status != models.GrantActive {
			t.Errorf("%s grant = %+v", key, g)
		}
	}
	if effective(t, cat, "user-3", "initiative.packaged_themes") != true || effective(t, cat, "user-3", "initiative.theme_slots") != int64(1) {
		t.Error("bundle did not take effect")
	}
}

func TestBundleRefundRevokesEveryGrantTogether(t *testing.T) {
	s, cat, st, _ := productSetup(t)
	apply(t, s, "product_bundle_checkout.json", "product_bundle_refunded.json")
	for key, g := range grantsOf(t, st, models.SourceStripePurchase, "pi_b1") {
		if g.Status != models.GrantRevoked {
			t.Errorf("%s status = %s", key, g.Status)
		}
	}
	if effective(t, cat, "user-3", "initiative.packaged_themes") != false || effective(t, cat, "user-3", "initiative.theme_slots") != int64(0) {
		t.Error("revoked bundle still counts")
	}
}

func TestRepeatableOneOffAddsQuantityUnits(t *testing.T) {
	s, cat, _, _ := productSetup(t)
	apply(t, s, "product_repeatable_checkout.json")
	if v := effective(t, cat, "user-3", "initiative.theme_slots"); v != int64(3) {
		t.Errorf("effective = %v, want 3", v)
	}
}

func TestCancelEndsEveryGrantOfTheSubscription(t *testing.T) {
	s, cat, st, _ := productSetup(t)
	apply(t, s, "product_subscription_checkout.json", "product_invoice_paid.json")
	if g := grantsOf(t, st, models.SourceStripeSubscription, "sub_p1")["initiative.saved_encounters"]; g.ValidUntil == nil || g.ValidUntil.Unix() != 1792592000 {
		t.Fatalf("valid_until = %v", g.ValidUntil)
	}
	apply(t, s, "product_subscription_deleted.json")
	if g := grantsOf(t, st, models.SourceStripeSubscription, "sub_p1")["initiative.saved_encounters"]; g.Status != models.GrantEnded {
		t.Errorf("status = %s", g.Status)
	}
	if v := effective(t, cat, "user-3", "initiative.saved_encounters"); v != int64(1) {
		t.Errorf("effective = %v, want the free value", v)
	}
}

func TestInvoiceBeforeCheckoutBuildsGrantsFromProductMetadata(t *testing.T) {
	s, _, st, _ := productSetup(t)
	apply(t, s, "product_invoice_paid.json")
	g := grantsOf(t, st, models.SourceStripeSubscription, "sub_p1")["initiative.saved_encounters"]
	if g.Value != 10 || g.Quantity != 2 || g.ValidUntil == nil {
		t.Errorf("grant = %+v", g)
	}
}

func TestSubscriptionQuantityChangeRecomputesTheGrantValue(t *testing.T) {
	s, cat, st, _ := productSetup(t)
	apply(t, s, "product_subscription_checkout.json", "product_subscription_quantity5.json")
	g := grantsOf(t, st, models.SourceStripeSubscription, "sub_p1")["initiative.saved_encounters"]
	if g.Value != 25 || g.Quantity != 5 {
		t.Fatalf("grant = %+v", g)
	}
	// 1 + 25 = 26 passes the maximum of 20: the grant keeps the paid value, checks clamp it.
	if v := effective(t, cat, "user-3", "initiative.saved_encounters"); v != int64(20) {
		t.Errorf("effective = %v, want the maximum 20", v)
	}
}

func TestQuantityChangeLeavesRevokedGrantsAlone(t *testing.T) {
	s, _, st, _ := productSetup(t)
	apply(t, s, "product_subscription_checkout.json")
	gs, _ := st.GrantsByExternal(context.Background(), models.SourceStripeSubscription, "sub_p1")
	gs[0].Status = models.GrantRevoked
	if err := st.UpdateGrant(context.Background(), &gs[0], "admin"); err != nil {
		t.Fatal(err)
	}
	apply(t, s, "product_subscription_quantity9.json")
	if g := grantsOf(t, st, models.SourceStripeSubscription, "sub_p1")["initiative.saved_encounters"]; g.Value != 10 || g.Status != models.GrantRevoked {
		t.Errorf("grant = %+v", g)
	}
}

func TestEventForARemovedProductCreatesNothing(t *testing.T) {
	s, cat, st, _ := productSetup(t)
	if err := cat.DeleteProduct(context.Background(), "admin", "themes"); err != nil {
		t.Fatal(err)
	}
	apply(t, s, "product_bundle_checkout.json")
	if got := grantsOf(t, st, models.SourceStripePurchase, "pi_b1"); len(got) != 0 {
		t.Errorf("grants = %v", got)
	}
}
