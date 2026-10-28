# AGENTS.md

This file provides guidance to Claude Code, Codex, GitHub Copilot, and other coding agents
working in this repository.

## About This Project

`entitlements-api` (Go) is the platform's entitlements service. It holds entitlement keys, tiers,
and grants as data; the effective value for a subject is the max across the default tier and
that subject's active grants. Stripe is the only billing provider. Consumers enforce entitlements
and fail closed on creates. Design: `sweetrpg/platform`'s PADR-0034 (proposed).

No service code exists yet. The repo was scaffolded with CI, release, and Docker workflows
before implementation started.

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
