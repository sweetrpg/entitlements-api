# AGENTS.md

This file provides guidance to Claude Code, Codex, GitHub Copilot, and other coding agents
working in this repository.

## About This Project

`entitlements-api` (Go) is the platform's entitlements service. It holds entitlement keys, tiers,
and grants as data; the effective value for a subject is the max across the default tier and
that subject's active grants. Stripe is the only billing provider. Consumers enforce entitlements
and fail closed on creates. Design: `sweetrpg/platform`'s PADR-0034 (proposed).

## Platform Conventions and Decisions

This repo is a submodule of `sweetrpg/platform`. Platform-wide conventions and Architecture
Decision Records live there:

- checked out inside the platform tree: `../../docs/README.md` (convention index) and
  `../../docs/adr/README.md` (platform ADRs, `PADR-*`)
- standalone or module-only checkout:
  <https://github.com/sweetrpg/platform/tree/master/docs> and
  <https://github.com/sweetrpg/platform/tree/master/docs/adr>

Accepted `PADR-*` records are binding constraints. Read the ADR index before proposing a
structural change; if a task needs to contradict an accepted record, stop and say so -
propose a superseding ADR rather than working around it. This repo's own service-local
decisions are `ADR-*` in `docs/adr/` here. `/adr "<title>"` scaffolds one.

## Committing Code

[Conventional Commits](https://www.conventionalcommits.org/): `<type>(<scope>): <description>`.
Commits must be signed.

## Branches and Workflow

Git-flow (see `docs/git-flow.md` in `sweetrpg/platform`): `develop` is the integration branch,
`master` reflects the latest release. Feature/fix branches off `develop`, PR back into `develop`.

## Running Checks Locally

```bash
test -z "$(gofmt -l .)"
go build ./...
go vet ./...
go test -race ./...
```

## Service Notes

- **Auth:** zero trust. Checks require the end user's bearer token, verified through auth-api
  (Auth0) with the canonical user ID resolved via users-api. No service tokens, and no user ID
  taken from the request.
- **Checks:** `GET /check?key=` and `GET /check/batch` (repeated `key`, or `prefix`; exactly one).
  Both are GETs so consumers can cache them; an unknown key fails the whole batch with 404. Caps
  live in `constants` (`MaxBatchKeys`, `MaxPrefixLength`).
- **Audit:** catalog writes and their audit entry are not one transaction. A failed audit append
  is logged at ERROR and the write still succeeds.
- **Delete strategy:** catalog keys and tiers are soft-deleted; the audit collection is
  append-only. Grants are created only by the Stripe webhook handler (and admins).
- **Webhook:** `POST /webhooks/stripe` is the one public route. The Stripe signature over the raw
  body is its only authentication; nothing is read or written before it verifies. Event objects
  are decoded with local structs (`billing/events.go`), not SDK types, so a Stripe API-version
  change cannot break fields we do not read.
- **Revoked is sticky:** refund and dispute set `revoked`; later invoice or subscription events
  never change a revoked grant.
- **Fixtures:** `billing/testdata` events are hand-written to Stripe's documented shapes, not
  captured from an account. No test calls Stripe.
