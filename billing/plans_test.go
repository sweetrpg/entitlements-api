package billing

import (
	"context"
	"errors"
	"testing"

	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/data"
	"github.com/sweetrpg/entitlements-api/models"
)

const membersKey = "initiative.members_per_session"

// planSetup seeds two Members plans in group "members" (at_least 10 and 20, free tier 4) and two
// flag plans in group "perks" that grant different keys.
func planSetup(t *testing.T) (*Service, *catalog.Service, *data.MemoryStore, *fakeGateway) {
	t.Helper()
	s, cat, st, gw := productSetup(t)
	seedMembersProducts(t, cat)
	ctx := context.Background()
	_, err := cat.UpdateTier(ctx, "admin", catalog.TierInput{Slug: "free", Name: "Free", Values: map[string]any{
		"initiative.saved_encounters": 1.0, "initiative.theme_slots": 0.0, "initiative.packaged_themes": false,
		"initiative.customization": false, membersKey: 4.0,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"initiative.perk_a", "initiative.perk_b"} {
		if _, err := cat.CreateKey(ctx, "admin", catalog.KeyInput{Key: k, Type: models.TypeFlag}); err != nil {
			t.Fatal(err)
		}
	}
	for slug, key := range map[string]string{"plan-a": "initiative.perk_a", "plan-b": "initiative.perk_b"} {
		_, err := cat.CreateProduct(ctx, "admin", catalog.ProductInput{Slug: slug, Name: slug, Billing: models.BillingRecurring, PlanGroup: "perks",
			StripePriceID: "price_" + slug, Entitlements: map[string]any{key: true}})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, slug := range []string{"members-10", "members-20"} {
		p, _ := st.GetProducts(ctx, []string{slug})
		in := catalog.ProductInput{Slug: slug, Name: slug, Billing: models.BillingRecurring, PlanGroup: "members", StripePriceID: p[0].StripePriceID,
			Entitlements: map[string]any{membersKey: map[string]any{"value": float64(p[0].Entitlements[0].Value), "mode": "at_least"}}}
		if _, err := cat.UpdateProduct(ctx, "admin", in); err != nil {
			t.Fatal(err)
		}
	}
	return s, cat, st, gw
}

// subscribe makes the user hold a subscription to a product, the way a completed checkout does.
func subscribe(t *testing.T, s *Service, user, sub, product string) {
	t.Helper()
	ev := Event{ID: "evt_sub_" + sub, Type: "checkout.session.completed", Object: []byte(
		`{"id":"cs_` + sub + `","mode":"subscription","payment_status":"paid","customer":"cus_` + user + `","subscription":"` + sub +
			`","client_reference_id":"` + user + `","metadata":{"user_id":"` + user + `","product_id":"` + product + `","quantity":"1"}}`)}
	if err := s.HandleEvent(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
}

func TestUpgradeSwitchesTheExistingSubscription(t *testing.T) {
	s, cat, st, gw := planSetup(t)
	subscribe(t, s, "user-6", "sub_m1", "members-10")
	if v := effective(t, cat, "user-6", membersKey); v != int64(10) {
		t.Fatalf("before = %v, want 10", v)
	}
	res, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true})
	if err != nil || !res.Switched || res.URL != "" {
		t.Fatalf("result = %+v err = %v", res, err)
	}
	if gw.switched.SubscriptionID != "sub_m1" || gw.switched.PriceID != "price_members-20" || gw.switched.ProductID != "members-20" {
		t.Errorf("switch = %+v", gw.switched)
	}
	if gw.checkout.PriceID != "" {
		t.Error("a second subscription was started")
	}
	got := grantsOf(t, st, models.SourceStripeSubscription, "sub_m1")
	if len(got) != 1 || got[membersKey].Value != 20 || got[membersKey].ProductID != "members-20" {
		t.Errorf("grants = %+v", got)
	}
	if v := effective(t, cat, "user-6", membersKey); v != int64(20) {
		t.Errorf("after = %v, want 20", v)
	}
}

func TestDowngradeIsJudgedWithoutTheOldPlan(t *testing.T) {
	s, cat, st, gw := planSetup(t)
	subscribe(t, s, "user-6", "sub_m1", "members-20")
	res, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-10", Confirm: true})
	if err != nil || !res.Switched {
		t.Fatalf("downgrade refused: %+v %v", res, err)
	}
	if gw.switched.PriceID != "price_members-10" {
		t.Errorf("switch = %+v", gw.switched)
	}
	if g := grantsOf(t, st, models.SourceStripeSubscription, "sub_m1")[membersKey]; g.Value != 10 || g.ProductID != "members-10" {
		t.Errorf("grant = %+v", g)
	}
	if v := effective(t, cat, "user-6", membersKey); v != int64(10) {
		t.Errorf("effective = %v, want 10", v)
	}
}

func TestSameProductAgainIsRefused(t *testing.T) {
	s, _, _, gw := planSetup(t)
	subscribe(t, s, "user-6", "sub_m1", "members-10")
	if _, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-10"}); !errors.Is(err, catalog.ErrInUse) {
		t.Errorf("err = %v, want in use", err)
	}
	if gw.switched.SubscriptionID != "" || gw.checkout.PriceID != "" {
		t.Error("Stripe was called for a refused request")
	}
}

func TestProductOutsideTheGroupStillStartsItsOwnCheckout(t *testing.T) {
	s, _, _, gw := planSetup(t)
	subscribe(t, s, "user-6", "sub_m1", "members-10")
	res, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "encounters"})
	if err != nil || res.Switched || res.URL == "" || gw.switched.SubscriptionID != "" {
		t.Errorf("result = %+v err = %v switched = %+v", res, err, gw.switched)
	}
}

func TestFirstPlanInAGroupStartsACheckout(t *testing.T) {
	s, _, _, gw := planSetup(t)
	res, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20"})
	if err != nil || res.Switched || res.URL == "" || gw.checkout.ProductID != "members-20" {
		t.Errorf("result = %+v err = %v", res, err)
	}
}

func TestSwitchRefusedWhenTheNewPlanRaisesNothing(t *testing.T) {
	s, cat, _, gw := planSetup(t)
	ctx := context.Background()
	// A plan whose floor (2) is below the free value (4): even without the old plan it raises nothing.
	_, err := cat.CreateProduct(ctx, "admin", catalog.ProductInput{Slug: "members-2", Name: "M2", Billing: models.BillingRecurring, PlanGroup: "members",
		StripePriceID: "price_m2", Entitlements: map[string]any{membersKey: map[string]any{"value": 2.0, "mode": "at_least"}}})
	if err != nil {
		t.Fatal(err)
	}
	subscribe(t, s, "user-6", "sub_m1", "members-10")
	var no catalog.NoIncreaseError
	if _, err := s.Checkout(ctx, "user-6", "", CheckoutRequest{ProductID: "members-2"}); !errors.As(err, &no) {
		t.Errorf("err = %v, want no increase", err)
	}
	if gw.switched.SubscriptionID != "" {
		t.Error("the subscription was switched")
	}
}

func TestSwitchReplacesTheKeysOfTheGrant(t *testing.T) {
	s, cat, st, _ := planSetup(t)
	subscribe(t, s, "user-6", "sub_g1", "plan-a")
	if effective(t, cat, "user-6", "initiative.perk_a") != true {
		t.Fatal("plan A not active")
	}
	if _, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "plan-b", Confirm: true}); err != nil {
		t.Fatal(err)
	}
	got := grantsOf(t, st, models.SourceStripeSubscription, "sub_g1")
	if got["initiative.perk_a"].Status != models.GrantEnded || got["initiative.perk_b"].Status != models.GrantActive || got["initiative.perk_b"].ProductID != "plan-b" {
		t.Errorf("grants = %+v", got)
	}
	if effective(t, cat, "user-6", "initiative.perk_a") != false || effective(t, cat, "user-6", "initiative.perk_b") != true {
		t.Error("entitlements were not replaced")
	}
}

func TestPortalPlanChangeReplacesTheGrant(t *testing.T) {
	s, cat, st, _ := planSetup(t)
	subscribe(t, s, "user-6", "sub_m1", "members-10")
	apply(t, s, "plan_portal_to_members20.json")
	got := grantsOf(t, st, models.SourceStripeSubscription, "sub_m1")
	if len(got) != 1 || got[membersKey].Value != 20 || got[membersKey].ProductID != "members-20" {
		t.Fatalf("upgrade grants = %+v", got)
	}
	if v := effective(t, cat, "user-6", membersKey); v != int64(20) {
		t.Errorf("effective = %v, want 20", v)
	}
	apply(t, s, "plan_portal_to_members10.json")
	if g := grantsOf(t, st, models.SourceStripeSubscription, "sub_m1")[membersKey]; g.Value != 10 || g.ProductID != "members-10" {
		t.Errorf("downgrade grant = %+v", g)
	}
}

func TestPortalChangeToDifferentKeysKeepsOneSetOfGrants(t *testing.T) {
	s, _, st, _ := planSetup(t)
	subscribe(t, s, "user-6", "sub_g1", "plan-a")
	apply(t, s, "plan_portal_to_plan_b.json")
	got := grantsOf(t, st, models.SourceStripeSubscription, "sub_g1")
	if len(got) != 2 || got["initiative.perk_a"].Status != models.GrantEnded || got["initiative.perk_b"].Status != models.GrantActive {
		t.Errorf("grants = %+v", got)
	}
}

func TestPortalChangeToAPriceNotInTheCatalogFallsBackToMetadata(t *testing.T) {
	s, _, st, _ := planSetup(t)
	subscribe(t, s, "user-6", "sub_m1", "members-10")
	apply(t, s, "plan_portal_unknown_price.json")
	if g := grantsOf(t, st, models.SourceStripeSubscription, "sub_m1")[membersKey]; g.Value != 10 || g.ProductID != "members-10" {
		t.Errorf("grant = %+v", g)
	}
}

func TestPortalChangeLeavesARevokedSubscriptionAlone(t *testing.T) {
	s, _, st, _ := planSetup(t)
	subscribe(t, s, "user-6", "sub_m1", "members-10")
	gs, _ := st.GrantsByExternal(context.Background(), models.SourceStripeSubscription, "sub_m1")
	gs[0].Status = models.GrantRevoked
	if err := st.UpdateGrant(context.Background(), &gs[0], "admin"); err != nil {
		t.Fatal(err)
	}
	apply(t, s, "plan_portal_to_members20.json")
	if g := grantsOf(t, st, models.SourceStripeSubscription, "sub_m1")[membersKey]; g.Status != models.GrantRevoked || g.Value != 10 {
		t.Errorf("grant = %+v", g)
	}
}

// strictStore rejects a nil name list the way MongoDB rejects {$in: null} ("$in needs an array").
// The in-memory store accepts it, which hid a 500 on every plan-group checkout.
type strictStore struct{ data.Store }

var errInNeedsArray = errors.New("(BadValue) $in needs an array")

func (s strictStore) GetKeys(ctx context.Context, names []string) ([]models.Key, error) {
	if names == nil {
		return nil, errInNeedsArray
	}
	return s.Store.GetKeys(ctx, names)
}

func (s strictStore) GetTiers(ctx context.Context, slugs []string) ([]models.Tier, error) {
	if slugs == nil {
		return nil, errInNeedsArray
	}
	return s.Store.GetTiers(ctx, slugs)
}

func (s strictStore) GetProducts(ctx context.Context, slugs []string) ([]models.Product, error) {
	if slugs == nil {
		return nil, errInNeedsArray
	}
	return s.Store.GetProducts(ctx, slugs)
}

func strictPlanSetup(t *testing.T) (*Service, *data.MemoryStore, *fakeGateway) {
	t.Helper()
	s, _, st, gw := planSetup(t)
	strict := strictStore{st}
	s.store = strict
	s.limits = catalog.New(strict)
	return s, st, gw
}

func TestFirstPlanGroupCheckoutDoesNotSendANilListToTheStore(t *testing.T) {
	for _, slug := range []string{"members-10", "members-20"} {
		t.Run(slug, func(t *testing.T) {
			s, _, gw := strictPlanSetup(t)
			res, err := s.Checkout(context.Background(), "user-7", "", CheckoutRequest{ProductID: slug})
			if err != nil || res.URL == "" || res.Switched {
				t.Fatalf("result = %+v err = %v", res, err)
			}
			if gw.checkout.PriceID != "price_"+slug || gw.checkout.ProductID != slug || gw.checkout.Quantity != 1 {
				t.Errorf("params = %+v", gw.checkout)
			}
		})
	}
}

func TestPlanSwitchAlsoWorksAgainstTheStrictStore(t *testing.T) {
	s, _, gw := strictPlanSetup(t)
	subscribe(t, s, "user-6", "sub_m1", "members-10")
	res, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true})
	if err != nil || !res.Switched || gw.switched.PriceID != "price_members-20" {
		t.Errorf("result = %+v err = %v switch = %+v", res, err, gw.switched)
	}
}

func TestRecurringProductsCheckOutAgainstTheStrictStore(t *testing.T) {
	s, _, gw := strictPlanSetup(t)
	for _, slug := range []string{"encounters", "plan-a"} {
		if res, err := s.Checkout(context.Background(), "user-8", "", CheckoutRequest{ProductID: slug}); err != nil || res.URL == "" {
			t.Errorf("%s: result = %+v err = %v", slug, res, err)
		}
	}
	if gw.checkout.PriceID != "price_plan-a" {
		t.Errorf("params = %+v", gw.checkout)
	}
}
