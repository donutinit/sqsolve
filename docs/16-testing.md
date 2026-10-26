# 16. Test plan

## 1. Levels

What runs and where. "In CI" means it gates every pull request and every published image
(`docs/17` §4); "not in place" means there is no such test, whatever the other columns say.

| Level | What it covers | Tool | Where it runs | Status (1.2.2) |
|---|---|---|---|---|
| Unit | Pure functions and services with test doubles | `go test` | Every change, CI | In CI, with `-race`, on Go 1.26 and 1.27 |
| Properties and fuzzing | Identifier validators, cron parser, retention, encryption, CIDRs, display names | `testing.F` | CI (short fuzz), nightly (long fuzz) | In CI: the seeds with the tests and every target for 10 s (`make fuzz`); nightly 5 min each (`fuzz.yml`). Import paths have table tests, not a fuzz target |
| Integration | Services against real PostgreSQL 18, real SQLite, real `pg_dump`/`pg_restore` | `go test` with throwaway clusters (`internal/testpg`) | CI | In CI (PostgreSQL 18 from PGDG, `SQSOLVE_REQUIRE_PG=1`) |
| Contract | API responses against `api/openapi.yaml` | `kin-openapi` | CI | Not in place: there is no `api/openapi.yaml`. `internal/api` tests the request contract instead (authentication, scopes, CSRF, JSON framing, problem documents and their status) |
| End to end | UI flows in a browser | `chromedp` against the full Compose | CI | Not in place: no browser tests. `test/compose_e2e.py` drives the real image over HTTP, without a browser (CI `smoke`) |
| Faults and recovery | Outages, full disk, missing volumes, PostgreSQL down | `make scenarios`, failpoints and models in the Go tests | CI | Partly: `make scenarios` in CI (real PostgreSQL and server, processes killed mid-job, PostgreSQL down, folders without permission); recovery and pair writes stopped at every step and under a power-loss model; audit and filesystem write failures injected. No Docker-level kills (§4) |
| Security | Static analysis, vulnerabilities, web scanning, authorization | `govulncheck`, `gosec`, OWASP ZAP, matrix tests | CI | `govulncheck` in CI (source and the binary of the image). `gosec` and ZAP are not in place (ZAP was run by hand for 1.0, `08`, "Security review of 1.0"). Authorization tested per action and per job type, not as one matrix |
| Resolve compatibility | Real Resolve behaviour, including the export kit (`22` §6) | Guided manual script (`14-compatibility.md` §4) | Diego's workstations | Manual, on Linux only: done for 1.0, and replace and revert again for 1.2.2 |
| Acceptance | Criteria AC-01 to AC-11 | Script in §8, part automated and part manual | Before each release | Manual; recorded for 1.0 (`docs/acceptance/1.0.md`) and, for the update, recovery and restore checks of 1.2.2, `docs/acceptance/1.2.2.md` |
| Performance | Page and backup times | `go test -bench`, synthetic loads | Before each release | Not in place: there are no benchmarks |

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

The package tree is in `05-architecture.md` §4.

- `internal/pg` (`ident.go`): accept and reject slugs and role names; property: every accepted
  name fits in 63 bytes and does not contain `__` except for the generated auxiliary names.
- `internal/core` retention (`Retain`): table cases (60 days of hourly backups, protected kinds,
  days in local time, a clock moved back, `min_verified` by insertion order) and properties, in a
  seeded random test and in `FuzzRetain`: a pinned backup is never deleted, the minimum of
  verified backups is kept when there are enough, the result does not depend on the input order.
- `internal/scheduler`: next run with time zones and daylight saving time; missed runs.
- `internal/auth` policy: full matrix of role by action, and the narrowing by token scope.
- `internal/secrets`: encrypt and decrypt, rejection with changed additional data, with another
  key and with a tampered value.
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

Against a throwaway PostgreSQL 18 cluster (`internal/testpg`, with the local server binaries and
the configuration generated by sqsolve) and, in `make scenarios`, against the real server:

- Start from scratch: secrets, `sqsolve_admin`, wizard.
- Libraries: create with E1 simulating Resolve (a client that, with the library role and
  `CREATEDB`, creates the database and some sample tables belonging to the test project); the
  window detects it, removes `CREATEDB` and activates. Maintenance. Delete and untrash. There is
  no E2 (creation from a template): local templates are a direction in `20-roadmap.md`.
- Network security: a TCP client as `sqsolve_admin` and `postgres` is rejected; a library role
  cannot enter another library.
- Backups: create, verify L1 and L2, counts match; overwrite the file and check that L1 fails.
- Restore as new and by swap with open sessions (test clients connected); revert.
- Real retention over files.
- Import a `pg_dump` file without a manifest, copied to `_import` or uploaded. A file made with
  another owner has no test of its own (the restore runs with `--no-owner`).
- Catalog: rebuild from manifests after deleting the catalog rows.
- SQLite migrations: a copy before each one, a failed one rolled back, a catalog from a newer
  version refused (`SQS-X012`). There are no stored fixtures of each published version.

Test data never includes Resolve schemas: a synthetic schema of our own is used that mimics the
pattern (many tables, some large `bytea`, indexes). The synthetic schema of the experimental export
may repeat the table and column names of the `.drp` format (`22` §3), never the full Resolve
schema or real data.

## 4. Fault and recovery tests

The scenarios check the invariants that apply to each: the active library exists and opens; no
`ok` backup was lost; no orphaned `.partial`, `__restore_*` or `sqsolve_verify_*` remain after the
next start; the catalog matches the files. `make scenarios` had 66 checks at 1.2.2, all passing
(`docs/acceptance/1.2.2.md`).

| Scenario | How it is triggered |
|---|---|
| sqsolve dies during `pg_dump` | `test/scenarios.py` kills the server with `SIGKILL` during a backup; after the next start the job is marked interrupted or failed, no `pg_dump` survives and no `.partial` remains |
| Outage between the two renames of a swap | `internal/core` tests leave a replace or revert interrupted at the `swap` step (`TestSwapRecoveryAfterCrash` and the reconciliation tests); the next start settles it |
| PostgreSQL down during a backup | `test/scenarios.py` stops PostgreSQL in the middle of a backup: the job ends instead of hanging, `readyz` answers 503, the panel still opens, Health says PostgreSQL is not responding and sqsolve recovers when PostgreSQL returns |
| Not enough space | A backup whose restored size exceeds the free space: the restore and the test restore refuse with `SQS-B022` and the backup stays usable |
| Missing volume | A backups volume without its marker, in the init, startup and verification tests (`SQS-S014`, `SQS-S001`) |
| Backups folder without permission | `test/scenarios.py`: the backup fails with a clear message |
| PostgreSQL that never comes up | `internal/app` and `internal/health` tests: the start and the health monitor report it |
| Clock jump | Retention orders backups by insertion, tested with the clock moved back |
| Corrupt SQLite | `internal/store` refuses a file that is not a database (`SQS-X010`) and its integrity check reports corruption |
| Power cut during file writes and recovery | `internal/fsx` failpoints and a model of what a power cut keeps; control plane recovery stopped at every step (`internal/app`) |

Not in place: kills at the Docker level in CI (`docker kill` of sqsolve and PostgreSQL; the 1.2.2
acceptance restarted the container by hand during a backup), an outage during L2 verification, a
really full disk, a PostgreSQL broken by its own configuration, and a corrupt SQLite at startup
offering the `_system` copy.

The function that terminates processes in tests uses a simulated `os.Process` or only its own
children. No test sends signals to PIDs it did not launch.

## 5. Security tests

- `govulncheck` with no reachable vulnerability in order to merge (in CI, on the source and on the
  binary of the image). `gosec` is not in place.
- OWASP ZAP in "baseline" mode: not in CI. It was run by hand against 1.0 (`08`, "Security review
  of 1.0").
- Authorization: the role by action matrix and the token scopes in `internal/auth`, and the
  refusals per action and per job type in the web and API tests; not one table shared by the
  web, the API and the CLI.
- CSRF: `POST` without a token fails, in the panel and in the API with a session.
- Headers: CSP, `X-Content-Type-Options`, `Referrer-Policy`, `X-Frame-Options`, HSTS over HTTPS
  and never over plain HTTP.
- Injection: fuzzing of slugs, display names, CIDRs and cron expressions; import, manifest and
  catalog paths (`..`, symbolic links) have table tests.
- Secrets in output: the secret type prints nothing through `slog`, JSON or `fmt`, and SMTP and
  `pg.SetRolePassword` errors redact the password. There is no sweep of every log, response,
  diagnostic file and event.
- Manual security review before 1.0 (done, `08`, "Security review of 1.0") and on every major
  version, following `08-security.md`.

## 6. Compatibility

- Automatable, not in place: `pg_restore` of backups made by earlier versions of sqsolve, and a
  matrix of PostgreSQL 16, 17 and 18, which only matters for an external server mode (a direction
  in `20-roadmap.md`).
- Manual: the script in `14-compatibility.md` §4 with Resolve on Windows or macOS and Linux. It
  asks for its results in `docs/compat/`, which does not exist: the Linux runs so far are in
  `docs/poc/` and `docs/acceptance/`, and macOS and Windows have not been run.

## 7. Performance

Targets, with no measurement in place (there are no benchmarks):

- Libraries page and catalog with 50 libraries and 5000 backups: p95 under 300 ms (RNF-USA-05).
- Start with a large catalog: under 5 s.
- Backup and restore: times per size, recorded on each release to detect regressions, with
  reference values from PoC-10, which has not been run. No times are recorded.

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
