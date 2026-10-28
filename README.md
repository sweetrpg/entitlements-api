# SweetRPG Entitlements API

[![CI](https://github.com/sweetrpg/entitlements-api/actions/workflows/ci.yaml/badge.svg)](https://github.com/sweetrpg/entitlements-api/actions/workflows/ci.yaml)
[![License](https://img.shields.io/github/license/sweetrpg/entitlements-api.svg)](https://img.shields.io/github/license/sweetrpg/entitlements-api.svg)
[![Issues](https://img.shields.io/github/issues/sweetrpg/entitlements-api.svg)](https://img.shields.io/github/issues/sweetrpg/entitlements-api.svg)
[![PRs](https://img.shields.io/github/issues-pr/sweetrpg/entitlements-api.svg)](https://img.shields.io/github/issues-pr/sweetrpg/entitlements-api.svg)

Go service that holds entitlement keys, tiers, and grants as data, and resolves the effective
value for a subject. Platform-wide design is in `sweetrpg/platform`'s PADR-0034
(`docs/adr/PADR-0034-platform-wide-entitlements-service.md`). See `AGENTS.md` for conventions.

## Routes

| Route | Auth | Purpose |
| --- | --- | --- |
| `GET /check?key=` | user token | The caller's effective value for one key |
| `GET /check/batch` | user token | Repeated `?key=a.b&key=a.c`, or `?prefix=initiative.` (exactly one of the two) |
| `POST /billing/checkout` | user token | `{tier_id}` returns `{url}`, a Stripe Checkout page for the caller |
| `POST /billing/portal` | user token | `{url}` for the Stripe billing portal; 404 if the caller never checked out |
| `POST /webhooks/stripe` | Stripe signature | Public. Verified against the raw body; no user token |
| `/admin/keys`, `/admin/tiers`, `/admin/default-tier`, `/admin/audit` | admin user token | Catalog writes (audited) and reads |
| `GET /status/ping`, `GET /status/health` | none | Liveness, dependency check |

Check responses: `{key, type, value, ttl_seconds}` plus `Cache-Control: private, max-age=<ttl>`.
`value` is a boolean for flags and an integer for limits. An unknown key returns 404 and fails a
whole batch. A batch with both or neither of `key`/`prefix` returns 400 `invalid_selector`; more than 200 keys returns `too_many_keys`; a prefix over 128 characters returns `prefix_too_long`. Usage against a limit is the caller's to count.

## Auth

Zero trust: every call carries the end user's own bearer token. auth-api verifies it against Auth0
and users-api resolves the canonical user ID (`authz-client.go`); the answer is always about that
verified user, and no user ID is accepted in the request. A consuming service forwards the token
it received. Admin routes additionally require the `admin` role, and the admin's canonical ID is
stamped into `created_by`/`updated_by` and the audit trail. There are no service credentials.

## Data

Collections: `entitlement_keys`, `entitlement_tiers`, `entitlement_grants`, `catalog_config`
(singleton naming the default tier), `catalog_audit` (append-only). Keys and tiers are soft-deleted;
their unique indexes apply to live documents only. Grants are written by the Stripe webhook handler
and read for checks. `stripe_customers` maps a user to their Stripe customer; `stripe_events`
holds processed event IDs.

## Billing

Checkout and the portal send the user to Stripe-hosted pages; the redirect URLs come from config,
never from the request. Checkout puts the canonical user ID and tier on the session and on the
subscription as metadata, and every grant is built from that metadata.

| Stripe event | Effect |
| --- | --- |
| `checkout.session.completed`, `checkout.session.async_payment_succeeded` | Create the grant (`stripe_subscription` keyed on the subscription ID, `stripe_purchase` on the payment intent) and remember the customer |
| `invoice.paid` | Create or extend the subscription grant; `valid_until` is the latest period end and only moves forward |
| `customer.subscription.deleted`, `customer.subscription.updated` with `unpaid`, `canceled`, or `incomplete_expired` | Grant becomes `ended` (`past_due` stays active while Stripe retries) |
| `charge.refunded` (full refund only), `charge.dispute.created` | Grant becomes `revoked` and stays revoked; only an administrator reinstates it |

Delivery is at-least-once. An event ID is recorded after its handler succeeds and skipped on
replay; a failed handler returns 500 so Stripe retries. Event types not listed return 200.

A refund or dispute finds its grant through the Stripe object IDs stored on the grant (`refs`:
checkout session, invoice, payment intent, charge). A refund on a subscription payment resolves
only if the charge event names the payment intent or invoice; it does not call back into Stripe.

## Configuration

| Variable | Source | Purpose |
| --- | --- | --- |
| `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` | `api-stripe` Secret | Required; startup fails naming the missing one |
| `STRIPE_CHECKOUT_SUCCESS_URL`, `STRIPE_CHECKOUT_CANCEL_URL`, `STRIPE_PORTAL_RETURN_URL` | ConfigMap | Required absolute http(s) URLs |

See `.env.example` for the rest.

## Development

```bash
cp .env.example .env
make lint test build
```

Go version in `.go-version`. Tests use an in-memory store and stubbed auth-api/users-api; no
external services.
