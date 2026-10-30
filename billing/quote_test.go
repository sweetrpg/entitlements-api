package billing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stripe/stripe-go/v87"

	"github.com/sweetrpg/entitlements-api/catalog"
	"github.com/sweetrpg/entitlements-api/models"
)

// upgradePreview is what the fake Stripe says a 10.00 -> 20.00 switch would invoice: the
// proration lines (+15.00 for the new plan's remaining time, -5.00 for the old plan's unused
// time) and the renewal that follows, which is not part of what is due now.
var upgradePreview = Preview{Currency: "usd", Lines: []PreviewLine{
	{Description: "Remaining time on Members 20", Amount: 1500, Proration: true},
	{Description: "Unused time on Members 10", Amount: -500, Proration: true},
	{Description: "Members 20 renewal", Amount: 2000},
}}

var downgradePreview = Preview{Currency: "usd", Lines: []PreviewLine{
	{Description: "Remaining time on Members 10", Amount: 750, Proration: true},
	{Description: "Unused time on Members 20", Amount: -1500, Proration: true},
	{Description: "Members 10 renewal", Amount: 1000},
}}

func assertNothingChanged(t *testing.T, gw *fakeGateway, grants map[string]models.Grant, wantProduct string, wantValue int64) {
	t.Helper()
	if gw.switched.SubscriptionID != "" {
		t.Errorf("Stripe was asked to change the subscription: %+v", gw.switched)
	}
	if g := grants[membersKey]; g.ProductID != wantProduct || g.Value != wantValue {
		t.Errorf("grant = %+v", g)
	}
}

func TestSwitchWithoutConfirmationReturnsAQuoteAndChangesNothing(t *testing.T) {
	s, gw, grants := switchSetup(t, "members-10")
	gw.preview = upgradePreview

	res, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20"})
	var need *ConfirmationRequiredError
	if !errors.As(err, &need) || res.Switched || res.URL != "" {
		t.Fatalf("result = %+v err = %v, want ConfirmationRequiredError", res, err)
	}
	q := need.Quote
	if q.Action != QuoteSwitch || q.CurrentProduct.ID != "members-10" || q.NewProduct.ID != "members-20" || q.AmountDueNow != 1000 {
		t.Errorf("quote = %+v", q)
	}
	assertNothingChanged(t, gw, grants("sub_m1"), "members-10", 10)
}

func TestConfirmedSwitchIsExactlyWhatTheQuotePreviewed(t *testing.T) {
	s, gw, _ := switchSetup(t, "members-10")
	gw.preview = upgradePreview
	_, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20"})
	var need *ConfirmationRequiredError
	if !errors.As(err, &need) {
		t.Fatal(err)
	}
	if _, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{
		ProductID: "members-20", Confirm: true, ProrationDate: need.Quote.ProrationDate,
	}); err != nil {
		t.Fatal(err)
	}
	if len(gw.previewed) != 1 || gw.previewed[0] != gw.switched {
		t.Errorf("previewed %+v\nswitched %+v\nthe preview must use the same change, behavior, and proration date", gw.previewed, gw.switched)
	}
	if gw.switched.Proration != ProrateInvoiceNow || !gw.switched.ChargeNow || gw.switched.ProrationDate != need.Quote.ProrationDate {
		t.Errorf("switch = %+v", gw.switched)
	}
}

func TestConfirmedSwitchRejectsAProrationDateFromTheFutureOrLongAgo(t *testing.T) {
	for name, date := range map[string]int64{
		"future": time.Now().Add(time.Hour).Unix(),
		"stale":  time.Now().Add(-3 * time.Hour).Unix(),
	} {
		t.Run(name, func(t *testing.T) {
			s, gw, grants := switchSetup(t, "members-10")
			_, err := s.Checkout(context.Background(), "user-6", "", CheckoutRequest{ProductID: "members-20", Confirm: true, ProrationDate: date})
			if !errors.Is(err, models.ErrInvalid) {
				t.Errorf("err = %v, want invalid", err)
			}
			assertNothingChanged(t, gw, grants("sub_m1"), "members-10", 10)
		})
	}
}

func TestQuoteForAnUpgradeShowsWhatIsChargedNow(t *testing.T) {
	s, gw, grants := switchSetup(t, "members-10")
	gw.preview = upgradePreview
	before := time.Now().Unix()

	q, err := s.Quote(context.Background(), "user-6", CheckoutRequest{ProductID: "members-20"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Action != QuoteSwitch || q.Currency != "usd" || q.Quantity != 1 {
		t.Errorf("quote = %+v", q)
	}
	if q.CurrentProduct.Name != "members-10" || q.NewProduct.Name != "members-20" {
		t.Errorf("products = %+v / %+v", q.CurrentProduct, q.NewProduct)
	}
	if q.AmountDueNow != 1000 || q.CreditOnNextInvoice != 0 {
		t.Errorf("due now %d, credit %d; want 1000 (the proration lines only) and no credit", q.AmountDueNow, q.CreditOnNextInvoice)
	}
	if len(q.ProrationLines) != 2 || q.ProrationLines[0].Amount != 1500 || q.ProrationLines[1].Amount != -500 {
		t.Errorf("lines = %+v (the renewal is not a proration line)", q.ProrationLines)
	}
	if q.NextBillingDate == nil || !q.NextBillingDate.Equal(time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("next billing = %v", q.NextBillingDate)
	}
	if q.ProrationDate < before {
		t.Errorf("proration date = %d", q.ProrationDate)
	}
	if len(gw.previewed) != 1 || gw.previewed[0].Proration != ProrateInvoiceNow || gw.previewed[0].ProrationDate != q.ProrationDate {
		t.Errorf("preview params = %+v", gw.previewed)
	}
	assertNothingChanged(t, gw, grants("sub_m1"), "members-10", 10)
}

func TestQuoteForADowngradeShowsACreditAndNothingDueNow(t *testing.T) {
	s, gw, _ := switchSetup(t, "members-20")
	gw.preview = downgradePreview
	q, err := s.Quote(context.Background(), "user-6", CheckoutRequest{ProductID: "members-10"})
	if err != nil {
		t.Fatal(err)
	}
	if q.Action != QuoteSwitch || q.AmountDueNow != 0 || q.CreditOnNextInvoice != 750 {
		t.Errorf("quote = %+v, want nothing due now and a 7.50 credit", q)
	}
	if gw.previewed[0].Proration != ProrateCredit || gw.previewed[0].ChargeNow {
		t.Errorf("preview used %+v; a downgrade is previewed with the credit behavior", gw.previewed[0])
	}
}

func TestQuoteForANewSubscriptionIsPriceTimesQuantity(t *testing.T) {
	s, _, _, gw := productSetup(t)
	gw.prices = map[string]PriceInfo{"price_enc": {ID: "price_enc", UnitAmount: 500, Currency: "usd", Interval: "month", IntervalCount: 1}}
	q, err := s.Quote(context.Background(), "user-3", CheckoutRequest{ProductID: "encounters", Quantity: 3})
	if err != nil {
		t.Fatal(err)
	}
	if q.Action != QuoteCheckout || q.AmountDueNow != 1500 || q.Quantity != 3 || q.Currency != "usd" || q.CurrentProduct != nil {
		t.Errorf("quote = %+v", q)
	}
	if q.NextBillingDate == nil || q.NextBillingDate.Before(time.Now().AddDate(0, 0, 27)) || q.NextBillingDate.After(time.Now().AddDate(0, 1, 2)) {
		t.Errorf("next billing = %v, want about a month away", q.NextBillingDate)
	}
}

func TestQuoteForAOneOffPurchaseHasNoBillingDate(t *testing.T) {
	s, _, _, gw := productSetup(t)
	gw.prices = map[string]PriceInfo{"price_themes": {ID: "price_themes", UnitAmount: 1999, Currency: "usd"}}
	q, err := s.Quote(context.Background(), "user-3", CheckoutRequest{ProductID: "themes"})
	if err != nil || q.Action != QuoteCheckout || q.AmountDueNow != 1999 || q.NextBillingDate != nil {
		t.Errorf("quote = %+v err = %v", q, err)
	}
}

func TestQuoteForAProductAlreadyHeldIsNone(t *testing.T) {
	s, gw, _ := switchSetup(t, "members-10")
	q, err := s.Quote(context.Background(), "user-6", CheckoutRequest{ProductID: "members-10"})
	if err != nil || q.Action != QuoteNone || q.CurrentProduct.ID != "members-10" || q.NewProduct.ID != "members-10" || q.AmountDueNow != 0 {
		t.Errorf("quote = %+v err = %v", q, err)
	}
	if len(gw.previewed) != 0 {
		t.Error("Stripe was previewed for a product the user already has")
	}
}

func TestQuoteRefusesWhatCheckoutRefuses(t *testing.T) {
	s, cat, _, _ := productSetup(t)
	seedMembersProducts(t, cat)
	ctx := context.Background()
	if _, err := s.Quote(ctx, "u", CheckoutRequest{ProductID: "ghost"}); !errors.Is(err, models.ErrInvalid) {
		t.Errorf("unknown product: %v", err)
	}
	if _, err := s.Quote(ctx, "u", CheckoutRequest{TierID: "free"}); !errors.Is(err, models.ErrInvalid) {
		t.Errorf("tier quote: %v", err)
	}
	if _, err := s.Quote(ctx, "u", CheckoutRequest{}); !errors.Is(err, models.ErrInvalid) {
		t.Errorf("empty quote: %v", err)
	}
	var exceeded catalog.MaxExceededError
	if _, err := s.Quote(ctx, "u", CheckoutRequest{ProductID: "encounters", Quantity: 4}); !errors.As(err, &exceeded) {
		t.Errorf("over the maximum: %v", err)
	}
}

func TestQuoteSurfacesProviderFailures(t *testing.T) {
	s, gw, _ := switchSetup(t, "members-10")
	gw.previewErr = &stripe.Error{Code: stripe.ErrorCodeResourceMissing, Msg: "No such subscription"}
	var pe *ProviderError
	if _, err := s.Quote(context.Background(), "user-6", CheckoutRequest{ProductID: "members-20"}); !errors.As(err, &pe) || pe.Op != "quote" {
		t.Errorf("err = %v", err)
	}
}

func TestNextPeriod(t *testing.T) {
	from := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		p    PriceInfo
		want time.Time
		ok   bool
	}{
		"day":     {PriceInfo{Interval: "day", IntervalCount: 3}, from.AddDate(0, 0, 3), true},
		"week":    {PriceInfo{Interval: "week", IntervalCount: 2}, from.AddDate(0, 0, 14), true},
		"month":   {PriceInfo{Interval: "month"}, from.AddDate(0, 1, 0), true},
		"year":    {PriceInfo{Interval: "year", IntervalCount: 1}, from.AddDate(1, 0, 0), true},
		"one-off": {PriceInfo{}, time.Time{}, false},
	}
	for name, c := range cases {
		got, ok := nextPeriod(from, c.p)
		if ok != c.ok || !got.Equal(c.want) {
			t.Errorf("%s: got %v %v, want %v %v", name, got, ok, c.want, c.ok)
		}
	}
}
