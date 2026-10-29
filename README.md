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
| `POST /billing/checkout` | user token | `{tier_id}` or `{product_id, quantity}` returns `{url}`, a Stripe Checkout page for the caller; 409 `exceeds_maximum` (with `units_remaining`) or `in_use` when refused |
| `POST /billing/portal` | user token | `{url}` for the Stripe billing portal; 404 if the caller never checked out |
| `POST /webhooks/stripe` | Stripe signature | Public. Verified against the raw body; no user token |
| `/admin/keys`, `/admin/tiers`, `/admin/products`, `/admin/default-tier`, `/admin/audit` | admin user token | Catalog writes (audited) and reads |
| `GET /status/ping`, `GET /status/health` | none | Liveness, dependency check |

Check responses: `{key, type, value, ttl_seconds}` plus `Cache-Control: private, max-age=<ttl>`.
`value` is a boolean for flags and an integer for limits. An unknown key returns 404 and fails a
whole batch. A batch with both or neither of `key`/`prefix` returns 400 `invalid_selector`; more than 200 keys returns `too_many_keys`; a prefix over 128 characters returns `prefix_too_long`. Usage against a limit is the caller's to count.

## Resolution

`effective = max(default tier, tier grants, at_least grants) + sum(add grants)` for a limit key,
then clamped to the key's maximum. A flag is on when its tier value is on or any key-level grant is
active. Tiers and `at_least` grants are alternatives (the highest wins); purchased `add` units
stack, and each ends independently.

A limit key can carry a `max` (set through `/admin/keys`, absent means none). Every check clamps
the effective value to it, so a user never sees more than the maximum. `catalog.CheckPurchase`
refuses units that would pass it and returns how many remain; the HTTP error is 409
`exceeds_maximum` with `units_remaining`.

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

## Products

A product is one Stripe price that grants one or more entitlements (a key and a value per unit);
more than one makes it a bundle. `billing` is `one_off` or `recurring`; `repeatable` allows a
quantity (recurring) or repeat purchases (one-off), otherwise a user holds it once. Products are
catalog data (`/admin/products`); checkout refuses a product with no Stripe price ID, an unknown
key, or a quantity that would pass a key's maximum.

An entitlement is `add` (a bare value, stacks per unit) or `at_least`
(`{"value": 10, "mode": "at_least"}`, a floor; two products at 10 and 20 give 20, not 30, and the
value ignores quantity). Checkout rejects with 409 `no_increase` a product whose `at_least`
entitlements would not raise the user's current value; a bundle passes if any one raises it.

A recurring product can name a `plan_group`. A user holds at most one active subscription per
group: checking out another product in the group switches the existing Stripe subscription to the
new price (Stripe prorates; `POST /billing/checkout` answers `{"switched": true}` and creates no
session) and replaces that subscription's grants with the new product's. The check ignores the old
plan, so a downgrade (20 to 10) is allowed and a plan that raises nothing is not. A price change
made in the billing portal (`customer.subscription.updated`) does the same, matched by the price
ID. This is enforced at checkout, not at the webhook, so two subscriptions created outside this
flow are not merged.

A purchase creates one key-level grant per entitlement, valued per-unit value x quantity and
sharing the purchase's Stripe ID (payment intent, or subscription), so a refund, dispute, or
cancel ends them together. A subscription quantity change (`customer.subscription.updated`)
recomputes the grants; if that passes a key's maximum the grant keeps the paid value, a WARN is
logged, and checks clamp it. Soft-deleting a product does not touch grants already issued.

Upgrade note: grants are unique on `(source, external_id, key)`. `EnsureIndexes` drops the earlier
`(source, external_id)` index so a bundle's grants can share an ID.

## Seed data

`seed/initiative.json` holds the Initiative keys (types and maxima), the `free` default tier, and
the products. It is data, reviewed by product, not code. Load it through the admin API with an
administrator's token:

```bash
ENTITLEMENTS_API_URL=https://dev.sweetrpg.com/api/1/entitlements \
ENTITLEMENTS_ADMIN_TOKEN=... \
go run ./cmd/seed-catalog -dry-run   # drop -dry-run to write
```

The loader only creates what is missing; an existing row, or an already-set default tier, is left
alone, so re-running cannot undo an edit or clear a Stripe price ID. Products ship with no
`stripe_price_id` (checkout rejects them) until test-mode prices exist; set each with
`PUT /admin/products/:id`. `-file` loads a different seed. Members-per-session tiers above 4 are
not seeded: their products are not defined yet.

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
