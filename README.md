# SweetRPG Entitlements API

[![CI](https://github.com/sweetrpg/entitlements-api/actions/workflows/ci.yaml/badge.svg)](https://github.com/sweetrpg/entitlements-api/actions/workflows/ci.yaml)
[![License](https://img.shields.io/github/license/sweetrpg/entitlements-api.svg)](https://img.shields.io/github/license/sweetrpg/entitlements-api.svg)
[![Issues](https://img.shields.io/github/issues/sweetrpg/entitlements-api.svg)](https://img.shields.io/github/issues/sweetrpg/entitlements-api.svg)
[![PRs](https://img.shields.io/github/issues-pr/sweetrpg/entitlements-api.svg)](https://img.shields.io/github/issues-pr/sweetrpg/entitlements-api.svg)

Go service that holds entitlement keys, tiers, and grants as data, and resolves the effective
value for a subject. Platform-wide design is in `sweetrpg/platform`'s PADR-0034
(`docs/adr/PADR-0034-platform-wide-entitlements-service.md`). See `AGENTS.md` for conventions.

Stripe checkout, billing portal, and webhooks are not implemented yet.

## Routes

| Route | Auth | Purpose |
| --- | --- | --- |
| `GET /check?user_id=&key=` | service token | One key's effective value |
| `POST /check/batch` | service token | `{user_id, keys[]}` or `{user_id, prefix}` |
| `/admin/keys`, `/admin/tiers`, `/admin/default-tier`, `/admin/audit` | admin user token | Catalog writes (audited) and reads |
| `GET /status/ping`, `GET /status/health` | none | Liveness, dependency check |

Check responses: `{key, type, value, ttl_seconds}` plus `Cache-Control: private, max-age=<ttl>`.
`value` is a boolean for flags and an integer for limits. An unknown key returns 404 and fails a
whole batch. Usage against a limit is the caller's to count.

## Auth

- Checks: `Authorization: Bearer <token>` where the token is registered in `SERVICE_TOKENS`
  (`name=token,name=token`). Anything else is 401.
- Admin routes: a user bearer token verified by auth-api with the `admin` role; the canonical user
  ID from users-api is stamped into `created_by`/`updated_by` and the audit trail.

## Data

Collections: `entitlement_keys`, `entitlement_tiers`, `entitlement_grants`, `catalog_config`
(singleton naming the default tier), `catalog_audit` (append-only). Keys and tiers are soft-deleted;
their unique indexes apply to live documents only. Grants are read here and written by the billing
flow (not built yet).

## Development

```bash
cp .env.example .env
make lint test build
```

Go version in `.go-version`. Tests use an in-memory store and stubbed auth-api/users-api; no
external services.
