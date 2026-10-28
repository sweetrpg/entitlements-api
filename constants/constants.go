package constants

// Environment variable names
const (
	ALLOWED_ORIGINS          = "ALLOWED_ORIGINS"
	PYROSCOPE_SERVER_ADDRESS = "PYROSCOPE_SERVER_ADDRESS"
	PYROSCOPE_TENANT_ID      = "PYROSCOPE_TENANT_ID"

	// AUTH_API_URL is auth-api's base URL, used to verify forwarded admin bearer tokens.
	AUTH_API_URL = "AUTH_API_URL"

	// USERS_API_URL is users-api's base URL, used to resolve an admin's canonical user ID for
	// audit stamping.
	USERS_API_URL = "USERS_API_URL"

	// STRIPE_SECRET_KEY and STRIPE_WEBHOOK_SECRET come from the api-stripe Secret
	// (kubernetes/overlays/dev/secrets/stripe.yaml); the service refuses to start without them.
	STRIPE_SECRET_KEY     = "STRIPE_SECRET_KEY"
	STRIPE_WEBHOOK_SECRET = "STRIPE_WEBHOOK_SECRET"

	// Where Stripe sends the customer after Checkout and after the billing portal. Fixed in
	// config, never taken from a request, so the endpoints cannot be used as an open redirect.
	STRIPE_CHECKOUT_SUCCESS_URL = "STRIPE_CHECKOUT_SUCCESS_URL"
	STRIPE_CHECKOUT_CANCEL_URL  = "STRIPE_CHECKOUT_CANCEL_URL"
	STRIPE_PORTAL_RETURN_URL    = "STRIPE_PORTAL_RETURN_URL"
)

// Value constants
const (
	ServiceName = "entitlements-api"

	// ProfilingEnabledFlag is the feature-flag key gating continuous profiling.
	ProfilingEnabledFlag = "profiling-enabled"

	KeysCollection            = "entitlement_keys"
	TiersCollection           = "entitlement_tiers"
	ProductsCollection        = "entitlement_products"
	GrantsCollection          = "entitlement_grants"
	CatalogConfigCollection   = "catalog_config"
	AuditCollection           = "catalog_audit"
	StripeCustomersCollection = "stripe_customers"
	StripeEventsCollection    = "stripe_events"

	// CatalogConfigID is the _id of the singleton catalog_config document.
	CatalogConfigID = "catalog"

	// MaxBatchKeys caps how many keys one batch check may name or expand to.
	MaxBatchKeys = 200

	// MaxPrefixLength caps a batch check's prefix; a key name is at most 128 characters.
	MaxPrefixLength = 128

	// MaxTTLSeconds caps a key's cache TTL hint.
	MaxTTLSeconds = 86400

	// MaxPurchaseQuantity caps the quantity of one product in a checkout.
	MaxPurchaseQuantity = 100

	// MaxWebhookBodyBytes caps a webhook payload read into memory.
	MaxWebhookBodyBytes = 1 << 20
)
