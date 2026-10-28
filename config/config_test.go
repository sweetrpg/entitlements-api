package config

import (
	"strings"
	"testing"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func valid() map[string]string {
	return map[string]string{
		"AUTH_API_URL": "http://auth", "USERS_API_URL": "http://users", "DB_URI": "mongodb://x/y",
		"STRIPE_SECRET_KEY": "sk_test_abc", "STRIPE_WEBHOOK_SECRET": "whsec_abc",
		"STRIPE_CHECKOUT_SUCCESS_URL": "https://example.test/ok",
		"STRIPE_CHECKOUT_CANCEL_URL":  "https://example.test/cancel",
		"STRIPE_PORTAL_RETURN_URL":    "https://example.test/account",
	}
}

func TestLoadValid(t *testing.T) {
	cfg, err := Load(env(valid()))
	if err != nil || cfg.AuthAPIURL != "http://auth" || cfg.StripeWebhookSecret != "whsec_abc" || cfg.PortalReturnURL != "https://example.test/account" {
		t.Fatalf("cfg = %+v err = %v", cfg, err)
	}
}

func TestLoadReportsEveryProblem(t *testing.T) {
	_, err := Load(env(map[string]string{}))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"AUTH_API_URL", "USERS_API_URL", "DB_URI", "STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "STRIPE_CHECKOUT_SUCCESS_URL", "STRIPE_CHECKOUT_CANCEL_URL", "STRIPE_PORTAL_RETURN_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestLoadRejectsMalformedStripeSettingsWithoutEchoingValues(t *testing.T) {
	m := valid()
	m["STRIPE_SECRET_KEY"] = "oops-secret-value"
	m["STRIPE_WEBHOOK_SECRET"] = "oops-webhook-value"
	m["STRIPE_PORTAL_RETURN_URL"] = "javascript:alert(1)"
	_, err := Load(env(m))
	if err == nil {
		t.Fatal("expected error")
	}
	for _, leaked := range []string{"oops-secret-value", "oops-webhook-value"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("error leaks %q", leaked)
		}
	}
	for _, want := range []string{"STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "STRIPE_PORTAL_RETURN_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}
