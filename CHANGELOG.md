## [0.5.0] - 2026-10-30

### 🚀 Features

- *(billing)* Charge plan upgrades immediately and fail cleanly
- *(billing)* Quote plan changes and require confirmation to switch

### ⚙️ Miscellaneous Tasks

- *(release)* Merge master into develop after v0.4.3
- *(docker)* Deploy new release tags to dev via ArgoCD update job
## [0.4.3] - 2026-10-29

### 🐛 Bug Fixes

- *(billing)* Stop sending a nil list to MongoDB on plan-group checkout

### ⚙️ Miscellaneous Tasks

- *(release)* Merge master into develop after v0.4.2
## [0.4.2] - 2026-10-29

### 🐛 Bug Fixes

- *(billing)* Return a typed 502 for payment provider failures

### ⚙️ Miscellaneous Tasks

- *(release)* Merge master into develop after v0.4.1
## [0.4.1] - 2026-10-29

### 🐛 Bug Fixes

- *(kubernetes)* Set explicit rate-limit values in dev and local overlays

### ⚙️ Miscellaneous Tasks

- *(release)* Merge master into develop after v0.4.0
- *(seed)* Drop the in-repo loader in favor of the CLI command
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
