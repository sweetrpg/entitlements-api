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
