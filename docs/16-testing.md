# 16. Test plan

## 1. Levels

The plan, and what of it runs today. "In CI" means it gates every pull request and every
published image (`docs/17` §4); "planned" is not a gate yet, whatever the other columns say. The
pending items have no target release yet: To be confirmed by Diego.

| Level | What it covers | Tool | Where it runs | Status (1.2.1) |
|---|---|---|---|---|
| Unit | Pure functions and services with test doubles | `go test` | Every change, CI | In CI, with `-race`, on Go 1.26 and 1.27 |
| Properties and fuzzing | Identifier validators, cron parser, retention, encryption, CIDRs, display names | `testing.F` | CI (short fuzz), nightly (long fuzz) | In CI: the seeds with the tests and every target for 10 s (`make fuzz`); nightly 5 min each (`fuzz.yml`). Import paths: planned |
| Integration | Services against real PostgreSQL 18, real SQLite, real `pg_dump`/`pg_restore` | `go test` with throwaway clusters (`internal/testpg`) | CI | In CI (PostgreSQL 18 from PGDG, `SQSOLVE_REQUIRE_PG=1`) |
| Contract | API responses against `api/openapi.yaml` | `kin-openapi` | CI | Planned: there is no `api/openapi.yaml`. Meanwhile `internal/api` tests the request contract (authentication, scopes, CSRF, JSON framing, problem documents and their status) |
| End to end | UI flows in a browser | `chromedp` against the full Compose | CI | Planned. `test/compose_e2e.py` drives the real image over HTTP, without a browser (CI `smoke`) |
| Faults and recovery | Outages, full disk, missing volumes, PostgreSQL down | `make scenarios`, failpoints and models in the Go tests | CI | Partly: `make scenarios` in CI (real PostgreSQL and server, processes killed mid-job, PostgreSQL down, folders without permission); recovery and pair writes stopped at every step and under a power-loss model; audit and filesystem write failures injected. No Docker-level kills |
| Security | Static analysis, vulnerabilities, web scanning, authorization | `govulncheck`, `gosec`, OWASP ZAP, matrix tests | CI | `govulncheck` in CI (source and the binary of the image). `gosec` and ZAP planned (ZAP was run by hand for 1.0, `08` §10). Authorization tested per action and per job type, not as one matrix |
| Resolve compatibility | Real Resolve behaviour, including the export kit (`22` §6) | Guided manual script (`14-compatibility.md` §4) | Diego's workstations | Manual; done for 1.0 on Linux |
| Acceptance | Criteria AC-01 to AC-11 | Script in §8, part automated and part manual | Before each release | Manual; recorded for 1.0 (`docs/acceptance/1.0.md`) |
| Performance | Page and backup times | `go test -bench`, synthetic loads | Before each release | Planned: there are no benchmarks |

Coverage (RNF-MAN-01 asks for 80 % of the lines of `internal/`): 87.1 % of the statements of
`internal/` on 2026-10-25, measured with the integration tests (`go test -cover ./internal/...`
with PostgreSQL 18 installed; 70.5 % on 2026-10-22 and 66.7 % in the 2026-10-21 audit). The
requirement is met. Every package is above 80 % except `internal/secrets` (79.4 %); the API is
at 95.9 % and the panel at 88.0 %, with handler tests for every API route and most panel forms,
their permission refusals and their audit failures. Without PostgreSQL the integration tests
skip and the figure is lower.

Coverage is raised first where a failure costs data or access (recovery, audit failures,
concurrent authentication, request contracts), not with tests of getters.

Resolve cannot run in CI (it needs a GPU and a license). That is why the design avoids depending
on its behaviour on the automatable paths and concentrates what does depend on it in PoCs and
manual scripts.

## 2. Mandatory unit tests

Package names as they are today (`21-tasks.md` §1).

- `internal/pg/ident`: accept and reject slugs and role names; property: every accepted name fits
  in 63 bytes and does not contain `__` except for the generated auxiliary names.
- `internal/core` retention (`Retain`): table cases (no backups, only pinned ones, schedule changes, leap
  years, `min_verified` greater than the available ones) and properties: a pinned backup is never
  deleted, an active library is never left without a verified backup if one existed, the result
  does not depend on the input order.
- `internal/scheduler`: next run with time zones and daylight saving time; missed runs.
- `internal/auth` policy: full matrix of role by action by resource.
- `internal/secrets`: encrypt and decrypt, rejection with changed additional data, key rotation.
- `internal/auth` TOTP: RFC 6238 vectors; a code is accepted once, also by two sessions at the
  same time.
- `internal/audit`: valid chain, tampering detected at any position; an entry written in the
  transaction of its change, or before a change outside SQLite, and nothing done without it.
- `internal/pgconf`: `pg_hba.conf` generated for combinations of libraries, credentials and CIDRs;
  the reject lines for administrative roles always come first.
- `internal/fsx`: atomic write (simulating failures between steps); key and certificate pairs
  under a model of what a power cut keeps (`powerloss_test.go`).
- `internal/app`: control plane recovery stopped at every step and finished by the next start.
- Error codes: every code has one message (`make errors-check`) and is listed in
  `docs/manual/errors.md` (`make errors`).

## 3. Integration tests

Against PostgreSQL 18 in a container, with the configuration generated by sqsolve:

- Start from scratch: secrets, `sqsolve_admin`, wizard.
- Libraries: create with E1 simulating Resolve (a client that, with the library role and
  `CREATEDB`, creates the database and some sample tables belonging to the test project); the
  window detects it, removes `CREATEDB` and activates. E2 from a template. Maintenance. Delete and
  untrash.
- Network security: a TCP client as `sqsolve_admin` and `postgres` is rejected; a library role
  cannot enter another library.
- Backups: create, verify L1 and L2, counts match; alter one byte of the file and check that L1
  fails.
- Restore as new and by swap with open sessions (test clients connected); revert.
- Real retention over files.
- Import a `pg_dump` file made with another owner and without a manifest.
- Catalog: rebuild from manifests after deleting SQLite.
- SQLite migrations from every previously published version (fixtures stored per version).

Test data never includes Resolve schemas: a synthetic schema of our own is used that mimics the
pattern (many tables, some large `bytea`, indexes). The synthetic schema of the experimental export
may repeat the table and column names of the `.drp` format (`22` §3), never the full Resolve
schema or real data.

## 4. Fault and recovery tests

Every scenario ends by checking the invariants: the active library exists and opens; no `ok`
backup was lost; no orphaned `.partial`, `__restore_*` or `sqsolve_verify_*` remain after the next
start; the catalog matches the files.

| Scenario | How it is triggered |
|---|---|
| Outage during `pg_dump` | `docker kill -s KILL` of sqsolve and PostgreSQL at a random point of the `dump` step |
| Outage between the two renames | Fault injection point in the `swap_2` step (build with the `faultinject` tag) |
| Outage during L2 verification | `docker kill` during `pg_restore` |
| Full disk | `backups` volume on a small tmpfs |
| Missing volume | Start without mounting `backups` |
| PostgreSQL down during a scheduled backup | `docker stop postgres` |
| PostgreSQL that does not start | Deliberately corrupt configuration |
| Clock jump | Change the time of the test container (injectable clock library) |
| Corrupt SQLite | Truncate the file; sqsolve must refuse to start and offer to restore the `_system` copy |

The function that terminates processes in tests uses a simulated `os.Process` or only its own
children. No test sends signals to PIDs it did not launch.

## 5. Security tests

- `govulncheck` with no reachable vulnerability in order to merge (in CI, on the source and on the
  binary of the image). `gosec` with no high findings: planned, not a gate yet.
- OWASP ZAP in "baseline" mode against the authenticated test Compose; no high alerts. Planned,
  not a gate yet (run by hand for 1.0, `08` §10).
- Authorization matrix tested against the web, the API and the CLI (same table, three adapters).
- CSRF: every `POST` without a token fails.
- Headers: CSP, `X-Content-Type-Options`, `Referrer-Policy`, HSTS when there is a custom
  certificate.
- Injection: fuzzing of slug inputs, display names, CIDRs, cron expressions, import paths
  (including `..`, symbolic links and null characters).
- Secrets in output: search for marked secrets in logs, responses, diagnostic files and events.
- Manual security review before 1.0 and on every major version, following `08-security.md`.

## 6. Compatibility

- Automatable: matrix of PostgreSQL 16, 17 and 18 for the external mode (2.0); `pg_restore` of
  backups generated by earlier versions of sqsolve.
- Manual: the script in `14-compatibility.md` §4 with Resolve on Windows or macOS and Linux.
  Results in `docs/compat/`.

## 7. Performance

- Libraries page and catalog with 50 libraries and 5000 backups: p95 under 300 ms (RNF-USA-05).
- Start with a large catalog: under 5 s.
- Backup and restore: times per size are recorded on each release to detect regressions; the
  reference values come from PoC-10.

## 8. 1.0 acceptance script

Environment: a Linux server and a NAS with Docker; two workstations with Resolve 21.1.x (at least
one Studio); local network; then without Internet access.

| Step | Criterion | Verification |
|---|---|---|
| 1 | AC-01 | Install with the YAML in both environments; time it; note every edit of the YAML |
| 2 | AC-02 | Complete wizard; restart; the wizard does not reappear |
| 3 | AC-03 | Create `acceptance_a`; initialize from workstation 1; create a project |
| 4 | AC-04 | Someone with no prior experience connects workstation 2 following only the panel |
| 5 | AC-05 | Both see the project; the panel shows two workstations |
| 6 | AC-06 | Enable collaboration; bin locking and chat between workstations |
| 7 | AC-07 | Activity-based mode (ADR 0022) during a two-hour work session and a pause with no activity; check that backups are produced while there are changes and none when there are not, and review the catalog and retention with a short policy |
| 8 | AC-08 | Modify the project; restore as a copy and open it; replace and open; revert |
| 9 | AC-09 | Reboot the host; upgrade to a release candidate; cut the power during a backup |
| 10 | AC-10 | Repeat 3, 7 and 8 without Internet access |
| 11 | AC-11 | Delete everything except `backups` and the kit; reinstall on another machine and recover |

The result is recorded in `docs/acceptance/1.0.md` with versions, dates and evidence
(screenshots and `sqsolve doctor` output).
