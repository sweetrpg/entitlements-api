# Contributing

Thanks for considering a contribution to `entitlements-api`.

## Branching

This repo follows the sweetrpg platform's git-flow convention (`docs/git-flow.md` in
`sweetrpg/platform`):

* `develop` is the integration branch. All feature and fix branches merge here.
* `master` reflects the latest released state. Nothing is committed here directly.
* Branch names: `feature/<description>` for new functionality, `fix/<description>` for bug
  fixes, `hotfix/<description>` for urgent fixes to a released version.

```bash
git checkout develop
git pull
git checkout -b feature/my-change
# ... work, commit ...
git push -u origin feature/my-change
# open a PR: feature/my-change -> develop
```

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <description>
```

Commits must be signed. Squash merging is disabled; PRs merge with a merge commit or rebase.

## Running checks locally

```bash
test -z "$(gofmt -l .)"
go vet ./...
go test -race ./...
```

## Pull requests

CI runs on PRs targeting `master` and `develop`. Once checks pass and the PR is reviewed, it
can be merged (auto-merge is enabled once required checks pass).

## Releases

Versions are tagged from `master` via the "Prepare Release" workflow (`workflow_dispatch`),
which opens a `release/<version>` PR with an updated `CHANGELOG.md` for review before tagging.
