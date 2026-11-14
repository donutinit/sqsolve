# 16. Test plan

## 1. Levels

What runs and where. "In CI" means it gates every pull request and every published image
(`docs/17` §4); "not in place" means there is no such test, whatever the other columns say.

| Level | What it covers | Tool | Where it runs | Status (1.2.4) |
|---|---|---|---|---|
| Unit | Pure functions and services with test doubles | `go test` | Every change, CI | In CI, with `-race`, on Go 1.26 and 1.27 |
| Properties and fuzzing | Identifier validators, cron parser, retention, encryption, CIDRs, display names | `testing.F` | CI (short fuzz), nightly (long fuzz) | In CI: the seeds with the tests and every target for 10 s (`make fuzz`); nightly 5 min each (`fuzz.yml`). Import paths have table tests, not a fuzz target |
| Integration | Services against real PostgreSQL 18, real SQLite, real `pg_dump`/`pg_restore` | `go test` with throwaway clusters (`internal/testpg`) | CI | In CI (PostgreSQL 18 from PGDG, `SQSOLVE_REQUIRE_PG=1`) |
| Contract | API responses against `api/openapi.yaml` | `kin-openapi` | CI | Not in place: there is no `api/openapi.yaml`. `internal/api` tests the request contract instead (authentication, scopes, CSRF, JSON framing, problem documents and their status) |
| End to end | UI flows in a browser | `chromedp` against the full Compose | CI | Not in place: no browser tests. `test/compose_e2e.py` drives the real image over HTTP, without a browser (CI `smoke`) |
| Faults and recovery | Outages, full disk, missing volumes, PostgreSQL down | `make scenarios`, failpoints and models in the Go tests | CI | Partly: `make scenarios` in CI (real PostgreSQL and server, processes killed mid-job, PostgreSQL down, folders without permission); recovery and pair writes stopped at every step and under a power-loss model; audit and filesystem write failures injected; failures of PostgreSQL, of `pg_dump` and of a page template simulated in the Go tests (§4). No Docker-level kills (§4) |
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
  days in local time, a clock moved back, `min_verified` by insertion order, `pre_restore`
  backups that take no `keep_last` place, inside and past their 30 days) and properties, in a
  seeded random test and in `FuzzRetain`: a pinned backup is never deleted, the minimum of
  verified backups is kept when there are enough, the result does not depend on the input order,
  the `keep_last` most recent backups that are not `pre_restore` are kept whatever `pre_restore`
  backups sit among them.
- `internal/scheduler`: next run with time zones and daylight saving time; missed runs.
- `internal/auth` policy: full matrix of role by action, and the narrowing by token scope.
- `internal/secrets`: encrypt and decrypt, rejection with changed additional data, with another
  key and with a tampered value.
- `internal/auth` TOTP: RFC 6238 vectors; a code is accepted once, also by two sessions at the
  same time.
- `internal/audit`: valid chain, tampering detected at any position; an entry written in the
  transaction of its change, or before a change outside SQLite, and nothing done without it.
- `internal/pgconf`: `pg_hba.conf` generated for combinations of libraries, credentials and CIDRs;
  the reject lines for administrative roles always come first; the names of a library called
  like a keyword of the file (`all`, `replication`, `sameuser`, `samerole`, `samegroup`) are
  quoted.
- `internal/fsx`: atomic write (simulating failures between steps); key and certificate pairs
  under a model of what a power cut keeps (`powerloss_test.go`).
- `internal/app`: control plane recovery stopped at every step and finished by the next start.
- Error codes: every code has one message (`make errors-check`), is listed in
  `docs/manual/errors.md` (`make errors`) and is asserted by a test with its code and wording of
  its message. No tool checks that a code has a test. The codes of
  `test/errors-multi-message.txt` carry several wordings, and not every wording is asserted.

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
- Real retention over files, also after a replace and a revert (their `pre_restore` backups take
  no `keep_last` place).
- Import a `pg_dump` file without a manifest, copied to `_import` or uploaded. A file made with
  another owner has no test of its own (the restore runs with `--no-owner`).
- Catalog: rebuild from manifests after deleting the catalog rows.
- SQLite migrations: a copy before each one, a failed one rolled back, a catalog from a newer
  version refused (`SQS-X012`). There are no stored fixtures of each published version.

Each cluster of `internal/testpg` has its folder in `~/.cache/sqsolve-test/pg-<pid>-<digits>`
(the pid of the test process), its socket in a `/tmp/sqs*` folder and a port of 127.0.0.1 that was
free a moment before. When another process takes that port before PostgreSQL binds it ("Address
already in use"), the fixture starts PostgreSQL again on another port, five ports at most. On Linux
the kernel sends PostgreSQL SIGQUIT when the test process dies, so a killed test leaves no cluster
running, and the next start of a cluster removes the folders whose test process and postmaster
are both gone. It does not remove the folders of earlier versions of the fixture (`pg-<digits>`)
or the `/tmp/sqs*` folders of killed tests.

Test data never includes Resolve schemas: a synthetic schema of our own is used that mimics the
pattern (many tables, some large `bytea`, indexes). The synthetic schema of the experimental export
may repeat the table and column names of the `.drp` format (`22` §3), never the full Resolve
schema or real data.

## 4. Fault and recovery tests

The scenarios check the invariants that apply to each: the active library exists and opens; no
`ok` backup was lost; no orphaned `.partial`, `__restore_*` or `sqsolve_verify_*` remain after the
next start; the catalog matches the files. `make scenarios` had 66 checks at 1.2.2, all passing
(`docs/acceptance/1.2.2.md`), and has 68 since 1.2.3: the creation and the activation of the
second library are checked too.

| Scenario | How it is triggered |
|---|---|
| sqsolve dies during `pg_dump` | `test/scenarios.py` kills the server with `SIGKILL` during a backup; after the next start the job is marked interrupted or failed, no `pg_dump` survives and no `.partial` remains |
| Outage between the two renames of a swap | `internal/core` tests leave a replace or revert interrupted at the `swap` step (`TestSwapRecoveryAfterCrash` and the reconciliation tests); the next start settles it |
| PostgreSQL down during a backup | `test/scenarios.py` stops PostgreSQL in the middle of a backup: the job ends instead of hanging, `readyz` answers 503, the panel still opens, Health says PostgreSQL is not responding and sqsolve recovers when PostgreSQL returns |
| Not enough space | A backup whose restored size exceeds the free space: the restore and the test restore refuse with `SQS-B022` and the backup stays usable; a backup whose estimate does not fit in the backups volume fails in `prepare` with `SQS-B012` |
| Missing volume | A backups volume without its marker, in the init, startup, verification, retention, manual delete, purge, previous-state and panel upload tests (`SQS-S014`, `SQS-S001`): the purge stops at `check_backup` before its journal, and a previous state whose own backup still checks is kept; retention also with another installation's marker (`SQS-S003`) and swapped volumes (`SQS-S019`) |
| Backups folder without permission | `test/scenarios.py`: the backup fails with a clear message |
| A backup path that cannot be checked (disk error) | `internal/core` tests put a regular file where a folder of the path was, so the check fails with an error that is not "does not exist", or make the backup file a symlink to a folder sqsolve may not enter (not as root); the backup is blocked (`SQS-B045`), counts for no safety rule, is reported, and counts again when the folder is back; against PostgreSQL, a previous state whose only backup is such a backup is kept (`SQS-R055`), and such a backup does not hold the activity gate |
| PostgreSQL that never comes up | `internal/app` and `internal/health` tests: the start and the health monitor report it |
| Clock jump | Retention orders backups by insertion, tested with the clock moved back |
| Corrupt SQLite | `internal/store` refuses a file that is not a database (`SQS-X010`) and its integrity check reports corruption |
| Power cut during file writes and recovery | `internal/fsx` failpoints and a model of what a power cut keeps; control plane recovery stopped at every step (`internal/app`) |
| `pg_dump` fails, the row count fails or the database is gone | `internal/core`: a program that exits with an error takes the place of `pg_dump` (`SQS-B010`); the count is made to fail at the fault point `backup.count` while `pg_dump` succeeds (`SQS-B011`); the database of an active library is dropped (`SQS-B002`). The backup is recorded as failed, no file, `.partial` or manifest remains and the newest verified backup is the same |
| An archive cut short after its index | `internal/core`: the file of a backup is cut and its checksum recorded again, so level 1 passes; the test restore fails with `SQS-R010`, the backup is marked failed and no `sqsolve_verify_*` database remains |
| A manifest with another structure | `internal/core`: restore as new fails with `SQS-R011` and leaves no database or role; the test restore marks the backup failed |
| Sessions that never close before a replace | `internal/core`: a session stays open and the clock of the service passes the 10 minutes; the job fails with `SQS-R020`, the `__restore_*` copy is dropped and the library is not touched |
| Sessions that cannot be listed or closed | `internal/core`: the listing or the termination of a session fails at the fault points `sessions.list` and `sessions.terminate` while a session is open. A revocation stays recorded and reports `SQS-L081` (followed by `SQS-P041` when the rewrite of `pg_hba.conf` fails too); maintenance stays on and reports `SQS-L082`; both are audited as failed and the session is still open. The session left by a revocation is then closed by "Close" in the Activity tab and by maintenance with "close sessions", and the role stays `NOLOGIN` |
| A lookup or a role change that fails during a delete or a recovery from the trash | `internal/core`: the lookup of the database of an initialization window, the lookup of a role in the lock, the removal of `CREATEDB` and the lookup of the library's name before a recovery fail at the fault points `trash.window_lookup`, `trash.role_lookup`, `trash.createdb` and `untrash.name_lookup`. The job fails with `SQS-P001`, is not pending reconciliation, and the library keeps its state, its database and its access |
| A previous state that is no longer on the server | `internal/core`: Revert fails with `SQS-R031` and changes nothing |
| A `pg_hba.conf` that PostgreSQL rejects, a reload that fails | `internal/core`: "TLS only" on the test server, which runs without TLS (`SQS-P042`: the previous file is put back and nothing is reloaded), also without `pg_hba.conf.prev` (the error says it could not be put back); `sqsolve_admin` without the right to reload (`SQS-P043`: the next sync applies the file); stored settings that cannot be read (`SQS-P044`: nothing is written). `internal/pg`: a reload that answers false (`SQS-P030`) |
| `sqsolve_admin` cannot be prepared, the membership of a new role fails | `internal/pg`: another transaction holds `sqsolve_admin` until the lock timeout (`SQS-P050` inside `SQS-P002`), and a fake server refuses the first read of `pg.Bootstrap` the same way; `sqsolve_admin` renamed under an open session (`SQS-P021`: the new role is not left behind) |
| A program is missing, a page template fails, a handler panics | `internal/execx`: a program that is absent, not executable or a folder (`SQS-X002`). `internal/web`: the error page with data it cannot read answers 500 with `SQS-X998`, and a handler that panics behind the panel's middleware answers 500 with `SQS-X999` |

Not in place: kills at the Docker level in CI (`docker kill` of sqsolve and PostgreSQL; the 1.2.2
acceptance restarted the container by hand during a backup), an outage during L2 verification, a
really full disk, a PostgreSQL broken by its own configuration, and a corrupt SQLite at startup
offering the `_system` copy.

The function that terminates processes in tests uses a simulated `os.Process` or only its own
children. No test sends signals to PIDs it did not launch.

The rule that external programs run only through `internal/execx` (`AGENTS.md`) is for the code
that ships in the binary. `execx` runs an allowed program to its end and keeps its output in
memory, and its allowlist (`pg_dump`, `pg_restore`, `vacuumdb`, `restic`) is a security control
(`08` T-07) that does not grow for the tests. Test code starts what it needs directly, with an
argument array and no password in an argument: `internal/testpg` runs `initdb` and keeps a
`postgres` server until the end of the test (`os/exec`), the tests of `internal/execx` and
`internal/testpg` start their own test binary as a helper process, the `internal/execx` tests
also start a `sleep` and a `true`, and `test/scenarios.py` and `test/compose_e2e.py` use
`subprocess` without `shell=True`. The fake programs that the `internal/execx` tests run through
`Runner` are `/bin/sh` scripts that the tests write. `TestOnlyExecxStartsProgramsInShippedCode`
(`internal/execx`) fails when a file that is not a test, outside `internal/execx` and
`internal/testpg`, imports `os/exec` or uses `os.StartProcess`, `syscall.ForkExec`,
`syscall.StartProcess`, `syscall.Exec` or `unix.Exec`, and when a file that is not a test
imports `internal/testpg`.

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
- Secrets in output: the secret type prints nothing through `slog`, JSON or `fmt`, with any verb
  and also from an unexported field, and SMTP errors redact the password. The errors of the
  statements that carry a SCRAM verifier (`pg.SetRolePassword`, `pg.CreateLibraryRole`,
  `pg.Bootstrap`) redact it in their text, with any `fmt` verb, in `slog` and in the PostgreSQL
  error they unwrap to, and still match with `errors.Is` and `errors.As`. There is no sweep of
  every log, response, diagnostic file and event.
- Internal names in errors: a failed PostgreSQL connection shows the driver's text (socket path,
  `sqsolve_admin`, database) only to whoever has the `diagnostics` permission, in the
  experimental export of the panel and the API, on the Health page with its doctor checks
  (`TestHealthPageHidesTheDriverText`, `TestChecksForHidesTheDriverText`,
  `TestDoctorReportsServerProblems`), in `GET /api/v1/system/status` and any API problem
  (`TestAPIHidesTheDriverTextWhenPostgreSQLStops`, `TestAPIProblemHidesTheDriverText`) and in
  the notice of a failed panel action (`TestFailHidesTheDriverText`); `postgres.down`
  (`TestPostgresDownHidesTheDriverText`, `TestSnapshotHidesTheDriverText`) and the stored error
  of a failed export preparation (`TestExportPreparationStoresThePublicError`) keep only the
  public text. `pg.Public` is also checked against a real server (a refused connection, a
  session that loses its server, a stopped server, the ping of a stopped server). The error of
  a failed job and the output of `pg_dump` and `pg_restore` are not covered.
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
