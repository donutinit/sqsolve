# Instructions for agents working on sqsolve

Read this in full before touching the repo. The global rules in `~/.claude/CLAUDE.md` and
`~/dev/README.md` still apply.

## Project status

Version 1.2.2 (2026-10-26); the history is in `CHANGELOG.md` and the architecture in `docs/`.
The known issues of the current release are in `docs/19-risks.md` §5 and what comes next in
`docs/20-roadmap.md`. Resolve was tested with 1.0 on Linux (`docs/acceptance/1.0.md`,
`docs/poc/`), and replace and revert again with 1.2.2 (`docs/acceptance/1.2.2.md`). Whatever
depends on a PoC (the "Blocks" column of `docs/02-proofs-of-concept.md`) or on Resolve behavior
that those records do not cover was written according to the plan's hypothesis and is **not
validated with real Resolve**: do not extend it or take it for granted until a PoC or an
acceptance test confirms it.

## How to take on a change

1. Work comes from Diego: a known issue (`docs/19-risks.md` §5), an item of the roadmap
   (`docs/20-roadmap.md`) or a request of his. There is no task list to pick from.
2. Read the documents that describe the affected area and the related ADRs.
3. Implement only what was asked. If you find that a decision does not hold up, do not change it
   silently: write a new ADR with status "Proposed" and tell Diego.
4. The change is done when it does what was asked and meets the "definition of done" below. When
   it fixes a known issue, remove the issue from `docs/19-risks.md` §5; when it completes a
   roadmap item, remove the item from `docs/20-roadmap.md`.

## Definition of done (every change)

- `make check` passes: formatting, `go vet`, `staticcheck`, unit tests and `govulncheck`.
- The integration tests of the touched packages pass (`make test-integration`).
- Every new behavior has a test. Every expected error has a test of its message and code.
- No destructive operation without a prior backup, confirmation and an audit log entry.
- The affected documentation is updated in the same change.
- No new dependencies beyond those approved in `docs/06-stack.md`. Adding one requires an ADR and
  Diego's permission.

## Non-negotiable technical rules

- Resolve databases are a black box (ADR 0007): sqsolve neither reads nor writes Resolve tables
  except for the catalog queries and counts described in `docs/07-libraries-and-postgresql.md`.
  The only exception is the bounded read in `internal/drp` to export .drp/.drx (ADR 0019), which
  is not extended without another ADR.
- SQL always with parameters. Identifiers (database and role names) go through the regular
  expression validation and through `pgx.Identifier.Sanitize`. Never concatenate user text into
  SQL.
- External processes only with `exec.CommandContext` and an argument array, from
  `internal/execx`. No `sh -c`. Passwords through a temporary `PGPASSFILE`, never in arguments.
- Cancelling a job only sends signals to child processes that same job launched. Never `pkill`,
  never signals to foreign PIDs (in tests, simulate `os.Process`).
- Secrets: never in logs, never in long-lived environment variables; in the panel and the API
  only to whoever has the permission, with an audit log entry (no reauthentication, ADR 0021).
  The `slog` handler redacts fields marked as secrets.
- Writes of important files: `.partial` file, `fsync`, `rename`, `fsync` of the directory
  (`internal/fsx`).

## Code

- Current stable Go (see `go.mod`), `CGO_ENABLED=0`.
- Everything is in English, and English is the only supported language: code identifiers and
  comments, documentation, user interface, error messages, logs and commit messages. Do not add
  other languages or translation layers.
- Packages under `internal/`. No web frameworks: `net/http`, `html/template`, `log/slog`.
- Interface: server-side templates, standard forms and our own `app.js` (ADR 0004). No htmx, no
  third-party JavaScript, no Node or npm in the build.
- Changes to the interface follow `docs/ui/checklist.md`, the checklist left by the patterns
  phase of `ui-consistency` (finding-patterns); update its counts in the same change.

## Text style

No em or en dashes, straight quotes, no invented data: if a datum is missing, "To be confirmed".

## Commits

One-line, short message, in English. No trailers of any kind. Do not commit or push unless Diego
asks.
