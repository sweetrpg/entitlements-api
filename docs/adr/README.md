# Architecture Decision Records

An ADR records one architectural decision: the context, the options weighed, what was chosen,
and what that costs. It answers **why**, where a spec or the code answers **what**.

Accepted ADRs are binding constraints, not background reading. If a task requires contradicting
one, stop and say so - propose a superseding ADR instead of quietly working around it.

## Two tiers

| Tier | Prefix | Lives in | Scope |
| --- | --- | --- | --- |
| Platform | `PADR-NNNN` | `sweetrpg/platform`, `docs/adr/` | binding on more than one service |
| Service-local | `ADR-NNNN` | this repo, `docs/adr/` | this service only |

Number sequentially within each tier. A reference is unambiguous about its tree because the
prefix differs.

## Rules

- **One decision per file. Written once, never rewritten.** When a decision changes, write a new
  ADR and mark the old one `superseded`. Do not edit its Context, Options, Decision, or
  Consequences.
- **Immutability applies to accepted and superseded ADRs.** A `proposed` ADR may be revised
  until it flips to `accepted` or `rejected`.
- **Rejected options are mandatory.** Write what was not taken and the concrete reason.
- **Keep the count low.** The trigger is *cost to reverse*, not importance. One-way doors get an
  ADR; anything undoable in an afternoon does not.
- **`rejected` ADRs stay on disk** so a declined decision is not relitigated later.

## Statuses

- `proposed` - made at less than full confidence, not yet proven against the code.
- `accepted` - in force. Binding.
- `accepted (reconstructed)` - backfilled after the fact. Carries a `confirmed_by: <name>,
  <date>` frontmatter line, or states in Consequences that the original rationale was not
  recovered.
- `superseded` - replaced. `superseded_by` set in frontmatter; `Superseded by ADR-NNNN.` at the
  top of the body.
- `rejected` - considered and declined.

## Frontmatter

```yaml
id: ADR-0001
title: Use Redis for rate-limit counters
status: accepted
date: 2026-09-01
scope: entitlements-api
supersedes:               # omit if none
superseded_by:            # set when this ADR is retired
tags: [caching]
affected_repos: [entitlements-api]
confirmed_by:             # required for accepted (reconstructed)
```

## Writing one

- `/adr "<title>"` grabs the next number, copies the template, sets status `proposed`, and adds
  a row to the index below.
- A decision that starts life in an OpenSpec change's `design.md` and constrains future work gets
  promoted to an ADR before that change archives. See `docs/openspec.md` in `sweetrpg/platform`.

## Index

| ID | Title | Status | Decision (one line) | Date |
| --- | --- | --- | --- | --- |
| [0000](0000-template.md) | ADR template | template | - | 2026-10-27 |
