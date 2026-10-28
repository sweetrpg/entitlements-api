// Package config validates the settings the service cannot run without, so a bad deployment
// fails at startup with a message naming the problem instead of at the first request.
package config

import (
	"errors"
	"net/url"
	"strings"

	"github.com/sweetrpg/entitlements-api/constants"
)

// Config holds validated settings.
type Config struct {
	AuthAPIURL  string
	UsersAPIURL string

	StripeSecretKey     string
	StripeWebhookSecret string
	CheckoutSuccessURL  string
	CheckoutCancelURL   string
	PortalReturnURL     string
}

// Load reads and validates settings through getenv (os.Getenv in production). Every problem is
// reported at once, naming the setting and never its value.
func Load(getenv func(string) string) (*Config, error) {
	var problems []string

	cfg := &Config{
		AuthAPIURL:          getenv(constants.AUTH_API_URL),
		UsersAPIURL:         getenv(constants.USERS_API_URL),
		StripeSecretKey:     getenv(constants.STRIPE_SECRET_KEY),
		StripeWebhookSecret: getenv(constants.STRIPE_WEBHOOK_SECRET),
		CheckoutSuccessURL:  getenv(constants.STRIPE_CHECKOUT_SUCCESS_URL),
		CheckoutCancelURL:   getenv(constants.STRIPE_CHECKOUT_CANCEL_URL),
		PortalReturnURL:     getenv(constants.STRIPE_PORTAL_RETURN_URL),
	}
	for _, name := range []string{constants.AUTH_API_URL, constants.USERS_API_URL, constants.STRIPE_SECRET_KEY, constants.STRIPE_WEBHOOK_SECRET} {
		if getenv(name) == "" {
			problems = append(problems, name+" is required")
		}
	}

	if cfg.StripeSecretKey != "" && !hasPrefix(cfg.StripeSecretKey, "sk_", "rk_") {
		problems = append(problems, constants.STRIPE_SECRET_KEY+" must be a Stripe secret or restricted key")
	}
	if cfg.StripeWebhookSecret != "" && !hasPrefix(cfg.StripeWebhookSecret, "whsec_") {
		problems = append(problems, constants.STRIPE_WEBHOOK_SECRET+" must be a Stripe webhook signing secret")
	}
	for name, v := range map[string]string{
		constants.STRIPE_CHECKOUT_SUCCESS_URL: cfg.CheckoutSuccessURL,
		constants.STRIPE_CHECKOUT_CANCEL_URL:  cfg.CheckoutCancelURL,
		constants.STRIPE_PORTAL_RETURN_URL:    cfg.PortalReturnURL,
	} {
		if !absoluteURL(v) {
			problems = append(problems, name+" must be an absolute http(s) URL")
		}
	}

	if getenv("DB_URI") == "" && getenv("DB_HOST") == "" {
		problems = append(problems, "DB_URI (or DB_HOST and DB_NAME) is required")
	}

	if len(problems) > 0 {
		return nil, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return cfg, nil
}

func hasPrefix(v string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	return false
}

func absoluteURL(v string) bool {
	u, err := url.Parse(v)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}
