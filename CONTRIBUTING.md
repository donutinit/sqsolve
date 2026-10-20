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
make check              # formatting, vet, staticcheck, tests with -race, govulncheck
make test-integration   # tests against real PostgreSQL
make errors             # regenerates docs/manual/errors.md if you added or changed an SQS- code
```

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
   section it comes out empty.
2. Tag `vX.Y.Z` and push: CI publishes the signed image and the release with the compose file.
3. Verify the signature as `docs/17-ci-cd-and-releases.md` §6 says (cosign 3 or newer) and update
   the test installation.

## How to propose a change

1. One branch per task (`sqs-NNN-short-name` if it comes from `docs/21-tasks.md`).
2. `make check` and `make test-integration` green.
3. Pull request with what changes, why and how it was tested.

License: by contributing you agree that the code is published under AGPL-3.0-only and the
documentation under CC BY-SA 4.0 (ADR 0001).
