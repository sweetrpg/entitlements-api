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

	// SERVICE_TOKENS registers the services allowed to call the check endpoints, as
	// comma-separated name=token pairs. It comes from the platform secret store.
	SERVICE_TOKENS = "SERVICE_TOKENS"
)

// Value constants
const (
	ServiceName = "entitlements-api"

	// ProfilingEnabledFlag is the feature-flag key gating continuous profiling.
	ProfilingEnabledFlag = "profiling-enabled"

	KeysCollection          = "entitlement_keys"
	TiersCollection         = "entitlement_tiers"
	GrantsCollection        = "entitlement_grants"
	CatalogConfigCollection = "catalog_config"
	AuditCollection         = "catalog_audit"

	// CatalogConfigID is the _id of the singleton catalog_config document.
	CatalogConfigID = "catalog"

	// MaxBatchKeys caps how many keys one batch check may name or expand to.
	MaxBatchKeys = 200

	// MaxTTLSeconds caps a key's cache TTL hint.
	MaxTTLSeconds = 86400
)
