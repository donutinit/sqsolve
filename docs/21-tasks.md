# 21. Task breakdown

Each task can be executed by a programming agent with this document, the ones it references and
`AGENTS.md`. A task is done when it meets its criteria **and** the global definition of done in
`AGENTS.md`.

## 1. Package structure

```
cmd/sqsolve/main.go          subcommands: serve, init, doctor, healthcheck, admin, library, backup, restore, catalog, audit, version
internal/config              SQSOLVE_* variables, validation
internal/errs                catalog of SQS- codes, error types
internal/logx                slog, redaction, circular buffer
internal/secret              secret.String type
internal/secrets             secret files, AEAD encryption, master key
internal/fsx                 atomic writes, markers, statfs
internal/execx               execution of allowed binaries, PGPASSFILE, cancellation of own children
internal/store               SQLite, migrations, per-table repositories
internal/pg                  socket connection, ident, safe DDL, catalog queries
internal/pgconf              postgresql.conf, pg_hba.conf, certificates, reload
internal/jobs                queue, workers, locks, steps, logs
internal/scheduler           cron, presets, missed runs
internal/events              in-memory bus, event persistence
internal/audit               chained log, verification, export
internal/auth                users, sessions, CSRF, TOTP, tokens, policy
internal/library             lifecycle, credentials, sessions, fingerprint
internal/backup              backup, manifest, verification, catalog, retention, restore
internal/notify              webhook, SMTP, ntfy, rules, grouping
internal/health              status, indicators, doctor, diagnostics
internal/compat              known fingerprints with what is verified per version
internal/drp                 experimental export of .drp and .drx from the library (ADR 0019)
internal/api                 JSON v1 handlers
internal/web                 HTML handlers, templates, embedded assets
internal/reconcile           startup reconciliation
internal/clock               clock injectable for tests
```

## 2. Conventions for each task

- Branch `sqs-NNN-short-name`. One PR per task unless the task says otherwise.
- First the tests that pin the described behaviour; then the implementation.
- When finished, tick the criteria with `[x]` and update the status table (§12) with the date in
  the same PR.

## 3. Phase 0 (0.0): research

### SQS-020 Lab Compose
- **Depends on:** nothing.
- **Deliverables:** `deploy/lab/compose.yaml` with `postgres:18.6-trixie` pinned by digest,
  `log_statement=all`, `log_connections=on`, `log_disconnections=on`, `log_lock_waits=on`,
  `pg_hba` with `scram-sha-256` open to the LAN; `deploy/lab/README.md` with how to bring it up,
  read the logs and the queries of `07` §7 and §9 to copy into `psql`.
- **Criteria:** [ ] it comes up with `docker compose up -d`; [ ] a `psql` client from another
  machine connects with SCRAM; [ ] the logs show connections and statements; [ ] the per-version
  variants (16, 17) are selected with a variable.

### SQS-001 to SQS-010 Run PoC-01 to PoC-10
- **Cut of 2026-10-19:** for 1.0, PoC-01, 03, 04, 06, 08 and 09 are run, and PoC-02 only with
  Resolve 21.1.x and PostgreSQL 18. PoC-05, 07 and 10 and the rest of the matrix move to 1.1 and
  are documented as REPORTED. The lab is mininas (sqsolve, PostgreSQL 18) and SQS-020 stays as a
  minimal compose only if some PoC requires it. What does not need clicks in Resolve (roles,
  databases, backups, queries, Resolve's scripting API) is run by the agent.
- **Depends on:** SQS-020.
- **Deliverables:** one report per PoC in `docs/poc/` according to `02-proofs-of-concept.md`;
  state changes in `01-research.md` §14 and in `14-compatibility.md`; new or updated ADRs when a
  decision changes.
- **Criteria:** [ ] each report has exact versions, steps, literal results and a decision; [ ] no
  report contains Resolve schemas or dumps; [ ] the "Blocks" table of 02 reflects what got
  unblocked.
- **Note:** Diego runs them on his workstations, or an agent guides him step by step.

## 4. Version 0.1: skeleton

### SQS-040 Repository skeleton
- **Depends on:** nothing (can start in parallel with phase 0).
- **Deliverables:** `go.mod` (`go 1.26`), `cmd/sqsolve` with `version`, `Makefile` (`check`,
  `test-integration`, `image`, `vendor-check`), `LICENSE` (official AGPL-3.0 text),
  `CONTRIBUTING.md`, `SECURITY.md`, `CHANGELOG.md`, SPDX headers, `check` CI workflow.
- **Criteria:** [ ] `make check` passes from a clean tree; [ ] `sqsolve version` prints version,
  commit and Go version; [ ] CI runs on push and PR.

### SQS-041 Dockerfile and multi-architecture image
- **Depends on:** SQS-040.
- **References:** ADR 0016, `17` §5.
- **Criteria:** [ ] image based on `postgres:18.x-trixie@digest` with `sqsolve` in
  `/usr/local/bin`; [ ] `pg_dump --version` inside the image matches the server's major version;
  [ ] unprivileged default user; [ ] OCI labels; [ ] builds for amd64 and arm64; [ ] two builds
  of the same commit give the same binary.

### SQS-100 Startup configuration
- **Package:** `internal/config`. **References:** `12` §4.
- **Criteria:** [ ] all the variables of `12` §4 with a default value and validation; [ ] errors
  with an `SQS-C...` code and the variable name; [ ] tests for each validation.

### SQS-101 Error catalog
- **Package:** `internal/errs`. **References:** `05` §8.
- **Criteria:** [ ] `Error` type with code, human-readable message, cause and HTTP status; [ ]
  generator of `docs/manual/errors.md` from the table; [ ] test that fails if a code has no
  message or is repeated.

### SQS-102 Logs and redaction
- **Packages:** `internal/logx`, `internal/secret`. **References:** `13` §1.
- **Criteria:** [ ] `secret.String` prints `[redacted]` in `fmt`, `slog` and JSON; [ ] the
  handler redacts sensitive keys; [ ] circular buffer of 2000 lines for diagnostics.

### SQS-103 File utilities
- **Package:** `internal/fsx`.
- **Criteria:** [ ] `WriteFileAtomic` (`.partial`, `fsync`, `rename`, directory `fsync`); [ ]
  `CheckMarker(dir)` with `instance_id`; [ ] `Statfs(dir)`; [ ] tests with failures injected at
  each step.

### SQS-104 Running external processes
- **Package:** `internal/execx`. **References:** `08` T-07.
- **Criteria:** [ ] allowlist of binaries by absolute path; [ ] temporary 0600 `PGPASSFILE`
  created and deleted; [ ] process in its own group; cancelling sends SIGTERM and after 10 s
  SIGKILL only to that group; [ ] output capture with a limit; [ ] tests with a test binary of
  our own, no signals to other processes.

### SQS-105 Secrets
- **Package:** `internal/secrets`. **References:** `08` §6, ADR 0014.
- **Criteria:** [ ] read secrets from files and reject permissions more open than 0400 or 0600;
  [ ] `Seal`/`Open` with XChaCha20-Poly1305 and additional data (table, column, key id); [ ]
  master key rotation re-encrypting in one transaction; [ ] tamper tests.

### SQS-106 SQLite store and migrations
- **Package:** `internal/store`. **References:** `07` §11.
- **Criteria:** [ ] WAL, `foreign_keys`, one serialized writer; [ ] initial migration with all
  the tables of `07` §11; [ ] `VACUUM INTO` before migrating; [ ] checksum of each migration
  recorded and verified; [ ] `PRAGMA integrity_check` exposed for health.

### SQS-107 `sqsolve init`
- **Depends on:** SQS-103, SQS-105, SQS-109. **References:** `12` §5.
- **Criteria:** [ ] idempotent (running it twice changes nothing); [ ] generates only what is
  missing; [ ] refuses to adopt a backups volume with content and no marker; [ ] permissions and
  owners according to `12` §5, including PostgreSQL's TLS key as root:999 0640; [ ] prints the
  installation code if there is no administrator; [ ] no network.

### SQS-108 PostgreSQL control client
- **Package:** `internal/pg`. **References:** `07` §2, §3, §7.
- **Criteria:** [ ] socket connection with `sqsolve_admin` and, separately, with the superuser
  only from marked and audited functions; [ ] identifier validators with fuzzing; [ ] DDL builder
  that only accepts validated identifiers; [ ] catalog queries: databases, roles, sizes,
  sessions, collation.

### SQS-109 PostgreSQL configuration generation
- **Package:** `internal/pgconf`. **References:** `07` §10.
- **Criteria:** [ ] `postgresql.conf` with the parameters of the table; [ ] `pg_hba.conf` from the
  catalog, with administrative rejects first and roles by name; [ ] validation with
  `pg_hba_file_rules` before reloading and a `.prev` copy; [ ] self-signed certificate; [ ]
  golden file tests.

### SQS-110 PostgreSQL startup and control role
- **Depends on:** SQS-108, SQS-109.
- **Criteria:** [ ] creates `sqsolve_admin` with the attributes and memberships of `07` §3 only
  once; [ ] tries `GRANT EXECUTE ON FUNCTION pg_reload_conf()` and records whether it was
  available; [ ] integration test: `sqsolve_admin` and `postgres` rejected over TCP.

### SQS-111 Job queue
- **Package:** `internal/jobs`. **References:** `11` §11, `05` §5.
- **Criteria:** [ ] states and transitions of `11` §11; [ ] priorities; [ ] per-library read and
  write locks; [ ] semaphore for heavy jobs; [ ] steps with a journal in `job_steps`; [ ]
  cancellation; [ ] concurrency tests with `-race`.

### SQS-112 Scheduler
- **Package:** `internal/scheduler`. **References:** `11` §10.
- **Criteria:** [ ] 5-field cron and presets; [ ] time zone and daylight saving time; [ ]
  per-library offset; [ ] missed runs with a window; [ ] injectable clock.

### SQS-113 Events
- **Package:** `internal/events`. **References:** `10` §4.
- **Criteria:** [ ] types of `10` §4 as constants; [ ] persistence with retention; [ ] bus for
  SSE and notifications.

### SQS-114 Audit log
- **Package:** `internal/audit`. **References:** `08` §9.
- **Criteria:** [ ] hash chain with canonical JSON; [ ] full and incremental verification; [ ]
  JSON Lines export; [ ] tamper test at any position.

### SQS-115 Health, `doctor` and `healthcheck`
- **Package:** `internal/health`. **References:** `13` §3, §4, §7.
- **Criteria:** [ ] `/healthz`, `/readyz`; [ ] `sqsolve healthcheck` for Docker; [ ] `doctor`
  with the checks available in 0.1 (the rest are added with each area).

### SQS-116 Startup reconciliation
- **Package:** `internal/reconcile`. **References:** `05` §6.4, `11` §7.3, §8.
- **Criteria:** [ ] reconciliation framework with one registrable step per area; [ ] running jobs
  set to `interrupted`; [ ] each area registers its cleanup (those for backups and restore are
  added in their tasks).

### SQS-117 Base HTTP server
- **Package:** `internal/web` (base). **References:** `08` §3 T-05, RNF-SEG-03.
- **Criteria:** [ ] TLS with its own renewable or loaded self-signed certificate; [ ] read,
  write and idle timeouts; [ ] security headers and CSP; [ ] per-request identifier; [ ] optional
  redirect from HTTP.

### SQS-030 Reference Compose
- **Depends on:** SQS-041, SQS-107, SQS-110. **References:** `12` §3.
- **Criteria:** [ ] working `deploy/compose.yaml`; [ ] CI test that brings it up and waits for
  `readyz`; [ ] restart of both containers without loss; [ ] pasting it as is into TrueNAS
  "Install via YAML" works (manual test recorded).

### SQS-031 PostgreSQL container hardening
- **Depends on:** SQS-030.
- **Criteria:** [ ] minimal list of capabilities that allows starting and initializing the
  cluster; [ ] `no-new-privileges`; [ ] documented in the YAML.

## 5. Version 0.2: access

### SQS-150 Panel users
- **Package:** `internal/auth`. **References:** `08` §4.
- **Criteria:** [ ] create, edit, disable; [ ] Argon2id with the parameters in the hash; [ ]
  local list of common passwords; [ ] tests.

### SQS-151 Sessions, CSRF and limits
- **Criteria:** [ ] 256-bit tokens stored as a hash; [ ] idle and absolute expiry; [ ]
  reauthentication with the `reauth_at` mark; [ ] CSRF by synchronizer token; [ ] attempt limit
  per IP and account with exponential back-off; [ ] `auth.login_failed_burst` event.

### SQS-152 TOTP and recovery codes
- **Criteria:** [ ] RFC 6238 with vectors; [ ] QR generated on the server (own PNG or SVG, no
  external services); [ ] 10 single-use codes; [ ] global option of mandatory TOTP for
  administrators.

### SQS-153 API tokens
- **Criteria:** [ ] `sqs_` prefix, shown once, SHA-256 hash; [ ] scopes and expiry; [ ]
  `last_used_at`; [ ] revocation.

### SQS-154 Authorization policy
- **References:** `08` §5.
- **Criteria:** [ ] a single `Authorize(actor, action, resource)` table; [ ] test of the full
  matrix; [ ] used by web, API and CLI.

### SQS-155 Setup wizard (service)
- **References:** `09` §3.1, RF-INS-03, RF-INS-04.
- **Criteria:** [ ] single-use installation code; [ ] persisted steps (the page can be reloaded);
  [ ] closes when the administrator is created; [ ] "recover existing installation" option
  reserved for SQS-600.

### SQS-156 Administration CLI
- **References:** `10` §7.
- **Criteria:** [ ] `admin create`, `reset-password`, `disable-totp`; [ ] audit log with actor
  `cli`.

## 6. Version 0.3: libraries

**Blocked by PoC-01, PoC-06 and PoC-08.**

### SQS-200 Library model and states
- **Package:** `internal/library`. **References:** `07` §1, §2, §4.
- **Criteria:** [ ] state machine with the allowed transitions and a test of the forbidden ones;
  [ ] validation of slug and display name; [ ] SQLite repository.

### SQS-201 Creation with initialization window (E1)
- **Depends on:** SQS-200, SQS-210, SQS-211, SQS-111.
- **References:** `07` §5 E1, `05` §6.1.
- **Criteria:** [ ] `library.create` and `library.init_watch` jobs; [ ] detection of an
  initialized database; [ ] removal of `CREATEDB` on detection, on expiry and in reconciliation;
  [ ] ownership adjustments according to the PoC-01 report; [ ] report of extra databases created
  by the role; [ ] integration test with a synthetic "simulated Resolve".

### SQS-202 Creation with a precreated database (E0)
- **Conditional:** only if PoC-01 validates it.

### SQS-210 Owner credential
- **References:** `07` §6.
- **Criteria:** [ ] generation with the defined alphabet; [ ] stored encrypted; [ ] reveal with
  reauthentication and audit; [ ] rotate with a warning about connected workstations.

### SQS-211 `pg_hba.conf` rules per library
- **Depends on:** SQS-109.
- **Criteria:** [ ] every change of libraries or credentials regenerates and reloads; [ ] a
  library cannot connect to another (integration test); [ ] a validation failure leaves the
  previous version active.

### SQS-220 Active sessions
- **References:** `07` §7.
- **Criteria:** [ ] listing per library; [ ] terminate only sessions of that library's roles; [ ]
  audit log.

### SQS-221 Maintenance
- **Criteria:** [ ] lock and unlock with `ALLOW_CONNECTIONS`; [ ] option to terminate sessions;
  [ ] backups keep working in maintenance.

### SQS-230 Deletion, trash and purge
- **Depends on:** SQS-301 (final backup).
- **References:** RF-BIB-07.
- **Criteria:** [ ] verified `pre_delete` backup before any change; [ ] rename to trash and
  `NOLOGIN` on the role; [ ] untrash; [ ] scheduled purge with a second backup check; [ ] typed
  confirmation and reauthentication required by the service, not only by the interface.

### SQS-240 Schema fingerprint
- **References:** `07` §9. **Blocked by PoC-03 for the "known versions" part.**
- **Criteria:** [ ] deterministic computation with a test on a synthetic schema; [ ] watch every
  5 min; [ ] event on change; [ ] registry of known fingerprints in `internal/compat`.

## 7. Versions 0.4 and 0.5: backups and restore

**Blocked by PoC-04, PoC-09 and PoC-10.**

### SQS-300 Backups volume
- **References:** `11` §3, RNF-FIA-04.
- **Criteria:** [ ] marker check before every write; [ ] computation of required space; [ ] errors
  `SQS-S001` and `SQS-B012`.

### SQS-301 Backup of a library
- **Depends on:** SQS-104, SQS-111, SQS-300.
- **References:** `11` §4.
- **Criteria:** [ ] steps 1 to 8 of `11` §4 as job steps; [ ] exported snapshot and counts in the
  same transaction; [ ] `--lock-wait-timeout`; [ ] no final file without a manifest; [ ]
  cancellable; [ ] integration test that compares the manifest counts with the database.

### SQS-302 Manifest
- **References:** `10` §8.1.
- **Criteria:** [ ] `api/schemas/manifest.v1.json`; [ ] atomic write; [ ] reading tolerant of
  unknown fields.

### SQS-303 L1 verification
- **Criteria:** [ ] SHA-256 read from disk; [ ] `pg_restore --list`; [ ] failure cleans up and
  notifies.

### SQS-304 Catalog and rebuild
- **Criteria:** [ ] catalog in SQLite; [ ] `catalog rebuild` from manifests; [ ] detection of
  orphans in both directions; [ ] daily consistency job.

### SQS-310 Policies and schedule per library
- **Depends on:** SQS-112.
- **Criteria:** [ ] default policy when creating a library; [ ] presets and cron; [ ] preview of
  upcoming runs.

### SQS-311 Retention
- **References:** `11` §6.
- **Criteria:** [ ] pure function with table and property tests; [ ] preview; [ ] two-phase
  deletion; [ ] pinned backups and protected kinds; [ ] `min_verified`.

### SQS-312 Download and import
- **Criteria:** [ ] download with authorization and audit; [ ] import from `_import/` and by
  upload with a configurable size limit; [ ] validation with `pg_restore --list`; [ ] manifest
  generated for imported backups; [ ] paths validated against `_import/` (no `..` or links
  outside).

### SQS-313 Control plane backup
- **Criteria:** [ ] daily `VACUUM INTO` to `_system/`; [ ] monthly audit log export; [ ]
  retention of 30 copies.

### SQS-320 L2 verification
- **References:** `11` §5.
- **Criteria:** [ ] restore into `sqsolve_verify_*`; [ ] comparison of counts and fingerprint; [ ]
  always cleans up, including in reconciliation; [ ] skipped with a warning if there is no space.

### SQS-330 Restore as a new library
- **Criteria:** [ ] creates a new role and database; [ ] `pg_restore --no-owner --no-privileges
  --role=<slug> --single-transaction --exit-on-error`; [ ] verification against the manifest; [ ]
  active library with its own policy.

### SQS-331 Restore by swap
- **References:** `11` §7.2.
- **Criteria:** [ ] every state of the diagram as a step with a journal; [ ] `pre_restore`
  backup; [ ] wait for sessions with a maximum time; [ ] the service requires confirmation and
  reauthentication; [ ] integration test with connected clients.

### SQS-332 Swap recovery
- **References:** `11` §7.3.
- **Criteria:** [ ] the four cases of the table with a fault-injection test at each point; [ ] no
  path deletes `slug` or `__prev`.

### SQS-333 Revert and purge of previous states
- **References:** `11` §7.4.
- **Criteria:** [ ] revert within the deadline; [ ] purge only with a verified backup of the
  previous state.

### SQS-334 Backup compatibility before restoring
- **References:** `11` §7.5.
- **Criteria:** [ ] rejection by `pg_dump` version; [ ] warnings for a different fingerprint with
  the text of the table.

### SQS-340 Default maintenance jobs
- **References:** `11` §12.
- **Criteria:** [ ] all the jobs of the table created on first start and editable; [ ]
  `VACUUM (ANALYZE)` with `vacuumdb`.

## 8. Version 0.6: API and interface

Cut of 2026-10-19: SQS-400, SQS-410 and SQS-520 move to 1.1. Version 1.0 keeps the JSON API as it
is, the end-to-end test over HTTP and the template tests.

### SQS-400 OpenAPI contract (1.1)
- **References:** `10` §1, §2, §3.
- **Criteria:** [ ] complete `api/openapi.yaml` 3.1 for 1.0 before the handlers; [ ] RFC 9457
  errors; [ ] reusable schemas.

### SQS-401 to SQS-404 API handlers
- **SQS-401** libraries, connection, credentials, sessions; **SQS-402** backups, restore, import;
  **SQS-403** jobs, logs and SSE; **SQS-404** users, tokens, settings, events, audit log,
  maintenance.
- **Common criteria:** [ ] authorization through the single policy; [ ] idempotency and `ETag`
  where applicable; [ ] 202 with a job for long operations; [ ] tests per endpoint.

### SQS-410 Contract tests (1.1)
- **Criteria:** [ ] every response of the integration suite is validated against OpenAPI; [ ] an
  unspecified endpoint fails CI.

### SQS-420 Webhooks and notification base
- **Package:** `internal/notify`. **References:** `10` §5, `11` §13.
- **Criteria:** [ ] HMAC signature with timestamp; [ ] retries; [ ] rules and grouping; [ ] test
  button; [ ] does not follow redirects; [ ] warning for private destinations.

### SQS-500 UI base
- **References:** `09` §1, §5, §6, §7, §8.
- **Criteria:** [ ] `ui-consistency` phase run and `docs/ui/checklist.md` written; [ ] light and
  dark design tokens; [ ] layout, navigation and components of `09` §8; [ ] vendored htmx with
  its SHA-256 recorded; [ ] English-only text with a test that finds leftovers; [ ] CSP with no
  exceptions.

### SQS-501 to SQS-511 Screens
- **SQS-501** wizard; **SQS-502** login, TOTP, reauthentication; **SQS-503** dashboard;
  **SQS-504** library list and new library; **SQS-505** library detail; **SQS-506** backups and
  restore; **SQS-507** jobs with SSE; **SQS-508** events and audit log; **SQS-509** health;
  **SQS-510** settings; **SQS-511** Resolve upgrade window (service and screen).
- **Common criteria:** [ ] meets `docs/ui/checklist.md` (verified with
  `ui-consistency:verifying`); [ ] empty, error and loading states; [ ] long operations as jobs
  with progress; [ ] dangerous ones with the pattern of `09` §5; [ ] usable at 360 px; [ ]
  keyboard and screen reader in the main flows.

### SQS-520 End-to-end tests with a browser (1.1)
- **Criteria:** [ ] `chromedp` covers the wizard, creating a library (with simulated Resolve),
  backing up, restoring as new, replacing and reverting, deleting and untrashing.

## 9. Version 0.9 and 1.0

Scope cut on 2026-10-19 (Diego's decision: the original plan was oversized for a one-person
project). What stays is what validates what the code already assumes and what makes the release
publishable; the rest moves to 1.1 and is noted in each task.

### SQS-600 Recover an existing installation
- **References:** `12` §9.
- **Criteria:** [ ] from the wizard with the master key; [ ] from `_system/` or by rebuilding the
  catalog; [ ] "restore all"; [ ] full recovery test in CI (AC-11 automated without Resolve).

### SQS-601 Recovery kit
- **Criteria:** [ ] mandatory download in the wizard; [ ] re-download from Settings (without
  reauthentication, ADR 0021); [ ] contents of `08` §6.

### SQS-602 Diagnostic bundle
- **References:** `13` §6.
- **Criteria:** [ ] exact contents of the list; [ ] test that searches for secrets inside the
  zip.

### SQS-603 Fault and recovery suite
- **References:** `16` §4.
- **Criteria (cut):** [ ] the scenarios that already run in CI (`test/scenarios.py`: processes
  that die, PostgreSQL down, restore with open sessions) pass on the release candidate; [ ]
  invariants checked. The nightly job with the rest of the scenarios moves to 1.1.

### SQS-604 Security review
- **Criteria (cut):** [ ] one local ZAP baseline run against the panel, with no high alerts; [ ]
  manual review against `08` with ADR 0021 already applied; [ ] findings fixed or recorded with
  an ADR. Nightly ZAP in CI moves to 1.1.

### SQS-605 User documentation and runbooks
- **References:** `18` §1.
- **Criteria (cut):** [ ] the README is the 1.0 manual and is up to date; [ ] `SECURITY.md`,
  `CHANGELOG.md` and `CONTRIBUTING.md` exist; [ ] three runbooks: restore and revert, recover an
  installation, upgrade sqsolve; [ ] `docs/manual/errors.md` generated from the `SQS-` codes. The
  other manuals and runbooks move to 1.1.

### SQS-606 1.0 acceptance
- **References:** `16` §8.
- **Criteria (cut):** [ ] AC-01 to AC-11 in a single session with Diego's Linux workstation and
  a macOS one with Resolve Studio, with the activity-based mode instead of the 72 hours of
  hourly backups; [ ] recorded in `docs/acceptance/1.0.md` with versions, dates and `sqsolve
  doctor` output.

### SQS-607 1.0 release
- **References:** `17` §6, §8.
- **Criteria (cut):** [ ] checklist of `17` §8 complete; [ ] image signed with keyless cosign
  (GitHub workflow identity) and verified with `cosign verify`; [ ] buildx SBOM attached; [ ]
  GitHub release with `deploy/compose.yaml` and the notes. Registry mirror, standalone binaries
  and verification by a third party move to 1.1.

## 10. Version 1.1

| ID | Task | Depends on | Main criterion |
|---|---|---|---|
| SQS-700 | Per-person credentials | PoC-01 step 5, SQS-210 | Create, immediate revocation, new objects owned by the owner |
| SQS-701 | Network rules in the UI and API | PoC-08, SQS-211 | An IP outside the rule does not connect; the rule is visible in `pg_hba.conf` |
| SQS-702 | Collaborator portal | SQS-700 | A collaborator only sees their libraries and their password with reauthentication |
| SQS-703 | SMTP and ntfy | SQS-420 | Test send and by rules; encrypted passwords |
| SQS-704 | External copies with restic | SQS-301 | Scheduled `backup`, `forget --prune`, `check`; restore from remote |
| SQS-705 | Hooks in a separate container | SQS-420 | Contract of `10` §6; no secrets; blocking `pre` |
| SQS-706 | Migration from another server | SQS-330 | Lists libraries of an external PostgreSQL and brings them in with L2 verification |
| SQS-707 | Local templates (E2) | PoC-04 step 3 | Create from a template without going through Resolve |
| SQS-708 | Adoption of existing databases | SQS-200 | Audited `REASSIGN OWNED` and registration of the database |
| SQS-610 | `.resolvedbkey` keys | PoC-05, review J-02 | Key accepted by Resolve on Windows, macOS and Linux; disabled by default |
| SQS-720 | Experimental export: model and `.drp`/`.drx` files | ADR 0019 | 5 of 5 `apolo` projects with no canonical differences against Resolve; documented kit (`22` §6) |
| SQS-721 | Experimental export: UI, API, permissions and audit log | SQS-720 | Export tab; setting off by default; audited downloads; `SQS-E` codes with tests |
| SQS-722 | Registry of verified fingerprints | SQS-720 | `internal/compat` with the 21.1.1 fingerprint and procedure in `14` §4 |

## 11. Versions 1.2 and 2.0

| ID | Task | Version |
|---|---|---|
| SQS-800 | Prometheus metrics | 1.2 |
| SQS-801 | Capacity of media volumes | 1.2 |
| SQS-802 | `sqsolve-check` for workstations (connection, authentication, TLS, collaboration port between two workstations) | 1.2 |
| SQS-803 | Schedulable maintenance in the UI | 1.2 |
| SQS-900 | PITR with pgBackRest | 2.0 |
| SQS-901 | OIDC | 2.0 |
| SQS-902 | WebAuthn | 2.0 |
| SQS-903 | PostgreSQL major version wizard (ADR 0017) | 2.0 |
| SQS-904 | External server mode | 2.0 |

## 12. Status

| Phase | Tasks | Status |
|---|---|---|
| 0.0 | SQS-020, SQS-001 to SQS-010 | Done on 2026-10-19 against mininas with Resolve's scripting API: PoC-06, 08 and 09, and PoC-03 in its "stable with use" half (`docs/poc/`). PoC-01 and 04 partly done the same day (E1 confirmed, E0 discarded, Resolve opens restored libraries); pending with clicks in Resolve: `sqsolve_admin` owner, member role, Resolve's own Backup and Restore, and the half of 03 (upgrade). Cut: 05, 07, 10 and the full matrix of 02 move to 1.1 |
| 0.1 | SQS-040, 041, 100 to 117, 030, 031 | Implemented (2026-10-18) |
| 0.2 | SQS-150 to 156 | Implemented (2026-10-18) |
| 0.3 | SQS-200 to 240 | Implemented (2026-10-18) according to the plan's hypothesis; not validated with Resolve (PoC-01 to 05). E0 stays experimental |
| 0.4 and 0.5 | SQS-300 to 340 | Implemented (2026-10-18); restore as seen from Resolve not validated (PoC-07, 08) |
| 0.6 | SQS-400 to 420, 500 to 520 | API, notifications and interface implemented (2026-10-18). SQS-400, 410 and 520 move to 1.1 (cut of 2026-10-19); there is an end-to-end test over HTTP and template tests |
| 0.9 and 1.0 | SQS-600 to 607 | SQS-600, 601 and 602 implemented (2026-10-18); SQS-603 meets the cut criterion with `test/scenarios.py`; SQS-604 done on 2026-10-19 (`docs/08` §10); SQS-605 done except `docs/acceptance`; SQS-607 ready in CI (signing and release), pending the first `v1.0.0` tag; SQS-606 partly done on 2026-10-19 (`docs/acceptance/1.0.md`): AC-01, 02, 03, 07, 08, 11 and the AC-09 restart pass against mininas; AC-04, 05, 06, 10 not done by Diego's decision |
| 1.1 (partial) | SQS-720 to 722 | Implemented (2026-10-19), ADR 0019 Accepted as experimental in 1.0; validated against Resolve 21.1.1 |
| Identity | ADR 0020 | Implemented (2026-10-19): brand in `docs/brand`, panel with the logo and icons, README |
| Proportionate security | ADR 0021 | Implemented (2026-10-19): no reauthentication, 8-character passwords, long sessions, lock with `admin unlock` |
| Activity-based backup | ADR 0022 | Implemented (2026-10-19): `activity` mode, gate in the scheduler, mark per backup, protection timeline |
| Export from backup | docs/22 §7 | Implemented (2026-10-19): `backup.export` job, temporary database, page and API, audit log |
| Stability audit | ADR 0023, 0024, 0025 | Implemented (2026-10-20): every finding of the 2026-10-20 audit (data safety of restores, recreate and import; verification outcomes; jobs runtime; volume identity and startup; health and alerts; login lockout and web hardening; PostgreSQL client; build, CI and deploy). Not validated with real Resolve: maintenance through `NOLOGIN` (what Resolve shows is To be confirmed) and the reserved connections |

The `[ ]` criteria of each task were not ticked one by one in this pass; the table summarizes.
