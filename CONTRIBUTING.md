# Contributing

## Before you start

Read [`AGENTS.md`](AGENTS.md): it holds the non-negotiable rules (SQL with parameters, external
processes only from `internal/execx`, nothing destructive without a backup, secrets out of the
logs) and the definition of done. They apply equally to people and to agents.

## Environment

- Current stable Go (see `go.mod`), `CGO_ENABLED=0`.
- For the integration tests, the PostgreSQL 18 binaries in the `PATH` or in
  `SQSOLVE_TEST_PG_BIN`.
- No Node, npm or dependencies beyond those approved in `docs/06-stack.md`.

## Cycle

```
make check              # formatting, vet, staticcheck, tests with -race, error codes, govulncheck
make test-integration   # tests against real PostgreSQL
make errors             # regenerates docs/manual/errors.md if you added or changed an SQS- code
make tidy-check         # go.mod and go.sum are what `go mod tidy` writes (CI runs it too)
make pg-image-check     # the PostgreSQL image is the same in Dockerfile and deploy/compose.yaml
```

An `SQS-` code means one thing: `make errors-check` (part of `make check`) fails if the same code
has two different messages. Grep for the code before you add one and take the next free number.

First the test that pins the behavior, then the code. Every expected error has a test of its code
and message. The affected documentation goes in the same change.

## Decisions

If a decision recorded in `docs/adr` does not hold up, do not change it silently: write a new ADR
with status "Proposed" and propose it.

## Style

Everything is in English, the only supported language: code identifiers and comments,
documentation, interface and commit messages, with straight quotes and no dashes. No invented
data: if a datum is missing, "To be confirmed". One-line commits, no trailers.

## Publishing a version

1. In `CHANGELOG.md`, move what is under "Unreleased" to a section `## [X.Y.Z] YYYY-MM-DD` with
   the upgrade and rollback steps. The GitHub release takes its notes from there; without that
   section its notes only say that `CHANGELOG.md` has none for that version.
   If the PostgreSQL pin changed since the previous release, say so and give the new `image:` line:
   installed copies of the compose file keep the old digest.
2. Tag `vX.Y.Z` and push: CI runs first and, only if it passes, publishes the signed image; then,
   and only then, the GitHub release with the compose file (pinned to `X.Y.Z`) is published.
3. Verify the signature as the user guide ("Updating") says (cosign 3 or newer) and update the
   test installation.

## How to propose a change

1. One branch per change, with a short name.
2. `make check` and `make test-integration` green.
3. Pull request with what changes, why and how it was tested.

License: by contributing you agree that the code is published under AGPL-3.0-only and the
documentation under CC BY-SA 4.0 (ADR 0001).
