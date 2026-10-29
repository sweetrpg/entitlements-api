## [0.4.0] - 2026-10-29

### 🚀 Features

- *(catalog)* Add add and at_least entitlement modes
- *(billing)* Add plan groups that switch a subscription
- *(seed)* Add a per-environment Stripe price overlay

### 🐛 Bug Fixes

- *(seed)* Treat Stripe price IDs as opaque and add Members 10
- *(data)* Use a partial index filter MongoDB accepts

### ⚙️ Miscellaneous Tasks

- *(release)* Merge master into develop after v0.3.0
## [0.3.0] - 2026-10-28

### 🚀 Features

- *(checks)* Add key-level grants to the tier value
- *(catalog)* Add a per-key maximum on limit keys
- *(catalog)* Add purchasable products
- *(seed)* Add the Initiative catalog seed and a loader

### ⚙️ Miscellaneous Tasks

- *(release)* Merge master into develop after v0.2.1
## [0.2.1] - 2026-10-28

### 🐛 Bug Fixes

- *(checks)* Make batch check a GET with query parameters

### ⚙️ Miscellaneous Tasks

- *(release)* Merge master into develop after v0.2.0
## [0.2.0] - 2026-10-28

### 🚀 Features

- *(billing)* Add Stripe checkout, billing portal, and signed webhook

### 🧪 Testing

- *(data)* Cover grant, customer, and event methods of the in-memory store

### ⚙️ Miscellaneous Tasks

- *(release)* Merge master into develop after v0.1.0
## [0.1.0] - 2026-10-28

### 🚀 Features

- *(entitlements)* Add catalog, effective-value checks, and service scaffold
- *(kubernetes)* Add entitlements-api base and dev/local overlays

### 🐛 Bug Fixes

- *(catalog)* Log failed audit appends instead of failing the write
- *(kubernetes)* Drop SERVICE_TOKENS secret from entitlements-api

### 🚜 Refactor

- *(checks)* Authenticate checks with the end user's token

### ⚙️ Miscellaneous Tasks

- Scaffold repository baseline (CI, release, Docker, docs)
- *(release)* Set prepare-release project board to Entitlements Service
# Changelog

## [Unreleased]

- Repository scaffolding: CI, PR validation, release workflows, Docker build, dependabot, and docs.
