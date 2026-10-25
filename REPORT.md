# Repository Audit Report

Date: 2026-10-21

Baseline: `main` at `766daac`, tag `v1.2.0`

Scope: architecture, data safety, recovery, authentication, authorization, secrets, audit logging,
SQL and process boundaries, API behavior, deployment, CI, tests, documentation, and the
server-rendered interface.

## Executive summary

The normal operating path is unusually well tested for a project of this size. All required Go
checks, the PostgreSQL integration suite, and all 66 operational and failure-recovery scenarios
passed. The database boundary, identifier handling, external process wrapper, container hardening,
and backup and restore workflows are generally disciplined.

The release should still receive a corrective patch before its recovery and crash-safety guarantees
are relied on. Two confirmed implementation gaps can leave the control plane or a certificate pair
in an inconsistent state after an error or power loss:

1. Applying a staged control-plane recovery replaces the SQLite database and master key as separate,
   non-resumable operations and ignores several critical errors.
2. `fsx.WritePair` does not make its recovery markers durable before the first rename, so its
   in-process failpoint test proves logical ordering but not power-loss safety.

There are also important security and consistency issues around secret file permissions, audit log
fail-open behavior, and concurrent authentication or administrator changes. None of these failures
appeared in the happy-path suites because they require injected filesystem failures, database write
failures, or concurrent requests.

### Release recommendation

Treat AUD-001 and AUD-002 as release blockers for the documented disaster-recovery guarantees.
Fix AUD-003 and AUD-004 in the same patch if possible. The authentication races can follow
immediately, but should not be deferred beyond the next release.

## Findings at a glance

| ID | Severity | Area | Finding |
|---|---|---|---|
| AUD-001 | Critical | Recovery | Control-plane recovery can replace the live database and key non-atomically and cannot resume every interrupted state |
| AUD-002 | High | Filesystem durability | `WritePair` can lose the only recovery marker across a power loss |
| AUD-003 | High | Secrets | Private boot secrets accept group-read access and initialization ignores permission-fix failures |
| AUD-004 | High | Audit | Destructive actions and secret disclosures can proceed without a durable audit entry |
| AUD-005 | Medium | MFA | A TOTP can be accepted twice under concurrency, and counter update errors are ignored |
| AUD-006 | Medium | Authorization | Concurrent changes can remove the last active administrator |
| AUD-007 | Medium | Setup | The one-time installation code can be consumed concurrently to create multiple administrators |
| AUD-008 | Low | API | Empty and concatenated JSON bodies are accepted as valid requests |
| AUD-009 | Low | Accessibility | Responsive tables remove their headers from the accessibility tree |
| AUD-010 | Medium | Assurance | Measured coverage and implemented release gates do not meet the documented test plan |
| AUD-011 | Low | Documentation | Version and package-structure documentation is stale at the audited tag |

## Detailed findings

### AUD-001: Control-plane recovery is not transactional or resumable

Severity: Critical

Status: Confirmed by code inspection. The current tests do not exercise interruption between the
database and key replacements.

Evidence:

- `internal/app/recovery.go:113-132` writes `sqsolve.db.recover`, but ignores `Sync` and `Close`
  errors and never syncs the directory that contains the staged database.
- `internal/app/recovery.go:183-190` ignores failure to rename the existing database to its safety
  copy, removes its WAL and SHM files, and then renames the staged database over the live path.
- `internal/app/recovery.go:192-196` repeats the same pattern for the master key.
- `internal/app/recovery.go:198-199` ignores both directory-sync errors.
- `internal/app/recovery.go:168-175` returns immediately unless both staged files exist. After a
  crash that already moved the staged database but not the staged key, the next start does not
  resume the key transition.
- `internal/app/startup_test.go:300-323` covers a successful application. Lines 640-661 cover a bad
  staged database before any move. There is no failpoint coverage for the transitions in between.

Failure modes:

- If the safety-copy rename fails, the following Unix rename can replace the live database anyway.
- A crash after the recovered database is installed but before the recovered key is installed leaves
  a database containing secrets encrypted by the new key next to the old live key. On restart, the
  missing database staging file causes recovery to be skipped.
- A crash after the old key is moved but before the new key is installed leaves no live master key.
- Ignored `fsync` failures let the command report success without proving the directory entries are
  durable.

This violates the repository rule that destructive operations require a prior backup and the stated
important-file protocol of partial write, file sync, rename, and directory sync.

Recommendation:

- Represent recovery as an explicit journaled state machine with the old and new database and key
  paths recorded before the first destructive transition.
- Check every `Sync`, `Close`, `Rename`, `Remove`, and directory-sync result.
- Make both staged files and their parent-directory entries durable before touching the live pair.
- Make every intermediate phase idempotently resumable. Startup must inspect the journal and all
  old, live, and staged paths, not require both original staging files to still exist.
- Preserve the previous database and key as a matched pair until the recovered pair has been opened
  and its key check has succeeded.

Required tests:

- Inject an error before and after each rename and each directory sync.
- Simulate restart from every resulting on-disk state.
- Assert that startup always produces either the complete old pair or complete recovered pair.
- Assert exact error codes and messages for failures that require operator action.

### AUD-002: `WritePair` is not power-loss safe

Severity: High

Status: Confirmed mismatch between the algorithm and its crash-safety contract.

Evidence:

- `internal/fsx/fsx.go:45-73` syncs partial-file contents but not the parent-directory entries that
  name those partial files. It also applies `Chmod` after the file sync without syncing the changed
  metadata again.
- `internal/fsx/fsx.go:80-109` renames the first half, then the second half, and syncs directories
  only after both renames.
- `internal/fsx/fsx.go:115-132` decides whether to discard or finish a write entirely from which
  partial files are visible after restart.
- `internal/fsx/fsx_test.go:137-168` stops the function with an in-process failpoint. The kernel still
  retains all unsynced directory state, so the test cannot model metadata loss or reordering after a
  real power failure.
- The helper protects the panel pair at `internal/app/panelcert.go:58,71,134` and the PostgreSQL pair
  at `internal/app/init.go:247,260`.

Failure mode:

After the first rename, a power loss may persist the first destination but lose the unsynced second
partial entry. `RecoverPair` then sees no marker to complete the second half, leaving a new key next
to an old certificate. Other metadata orderings can make it discard a valid staged pair.

Recommendation:

1. Write and sync both partial files, including final modes.
2. Sync every parent directory containing a partial before the first rename.
3. Rename the first file and sync its directory.
4. Rename the second file and sync its directory.
5. In `RecoverPair`, check removal errors and sync every affected directory.

Add subprocess or virtual-filesystem tests that model persistence boundaries. The existing
failpoint test should remain as a fast logical test, but it must not be the only evidence for the
power-loss guarantee.

### AUD-003: Secret permission policy is inconsistent and partly fail-open

Severity: High

Status: Confirmed.

Evidence:

- `docs/08-security.md:134-136` requires mode `0400` for the master key and PostgreSQL administrator
  password. ADR 0014 makes the same decision for boot secrets.
- `internal/secrets/secrets.go:82-99` rejects `mode & 0037`, which intentionally allows group-read
  mode `0440` for every secret read through this function.
- `internal/secrets/secrets_test.go:79-90` explicitly expects `0440` to be accepted.
- `internal/app/app.go:72-106` uses this one reader for the `0400` master key, the `0400`
  administrator password, and the legitimately group-readable `0440` PostgreSQL superuser file.
- `internal/app/doctor.go:113-121` correctly reports any group permission on the first two files as a
  failure, so startup and doctor disagree about whether the installation is safe.
- `internal/app/init.go:75-93,158,211-218,265-268` ignores multiple `Chmod` failures, including
  existing secret files and TLS keys. Initialization can print success after failing to tighten the
  requested mode.

Impact:

A master key or administrator password accidentally changed to `0440` is accepted at startup even
though the documented policy and doctor call it insecure. Any unintended member of that group can
then recover all encrypted credentials or administer PostgreSQL.

Recommendation:

- Split the reader into a private-secret policy and an explicitly group-readable policy, or pass the
  exact allowed mode at each call site.
- Reject group permissions for `master.key` and `pg_admin_password`.
- Check every permission change and return an `SQS-C` error when the requested protection cannot be
  established.
- Test `0400`, `0440`, and chmod failure separately for each secret class.

### AUD-004: Audit recording is not a prerequisite for sensitive actions

Severity: High

Status: Confirmed at multiple call sites.

The design says every state-changing action and secret exposure is recorded
(`docs/08-security.md:189-198`). The implementation often ignores `Audit.Record` errors or records
an `ok` outcome before knowing whether the requested action was accepted.

Examples:

- `internal/core/lifecycle.go:45-46,265-266` records successful delete or purge before enqueueing.
  An enqueue failure leaves a false success in the chain, while an audit failure does not block the
  job.
- `internal/core/restore.go:395-396` does the same for replacement restores.
- `internal/core/credentials.go:123-139` returns a decrypted password even if the audit insert fails.
- `internal/web/h_backups.go:129-144` ignores the audit result and streams a backup anyway.
- `internal/core/backup.go:957-960` deletes backup files before appending the audit entry. If that
  insert fails, the destructive operation has already happened without the promised record.

The hash-chain implementation itself is careful and serialized in `internal/audit/audit.go:103-128`.
The problem is enforcement at callers and coordination with the side effect.

Recommendation:

- Persist job creation and its audit intent in one SQLite transaction.
- Record an accepted or queued outcome only after enqueue succeeds, then record the worker's final
  outcome separately.
- Fail closed on password reveal, recovery-kit download, diagnostic download, and backup download if
  the audit entry cannot be persisted.
- For filesystem or PostgreSQL changes that cannot share the SQLite transaction, persist a durable
  intent before the external side effect and a completion or failure entry afterward.
- Add an audit writer fault injector and verify the promised behavior for every destructive action
  and every secret disclosure.

### AUD-005: TOTP replay protection has a check-then-write race

Severity: Medium

Status: Confirmed.

Evidence:

- `internal/auth/service.go:511-523` compares the presented counter with the counter previously
  loaded into `User`, then performs an unconditional update.
- The update error at line 521 is ignored.
- Two pending sessions can load the same previous value, both pass the comparison, and both update
  the same new counter successfully.
- `internal/auth/auth_test.go:188-220` tests sequential replay only.

Recommendation:

Use one conditional update such as `UPDATE ... SET totp_last_counter = ? WHERE id = ? AND
totp_last_counter < ?`, check the database error, and require exactly one affected row. Add a
barrier-based concurrency test in which two requests submit the same current TOTP and exactly one
succeeds.

### AUD-006: The last-administrator invariant is raceable

Severity: Medium

Status: Confirmed.

`internal/auth/service.go:264-294` counts other active administrators and performs the role or
disabled update in a separate statement. With two active administrators, concurrent requests can
both observe the other one and then both demote or disable their target. The sequential test at
`internal/auth/auth_test.go:238-247` covers only a single administrator.

Recommendation:

Serialize the check and update in a write transaction, or enforce the invariant through a database
mechanism that cannot be bypassed by concurrent writers. Add a two-administrator concurrency test
and assert that one request receives `SQS-A014`.

### AUD-007: The one-time setup code can be used concurrently

Severity: Medium

Status: Confirmed by control-flow inspection.

`internal/web/h_auth.go:84-142` checks for an existing administrator, validates the in-memory code,
creates a user, saves settings, and only then clears the code. `internal/app/app.go:173-184` protects
reading and clearing individually, not the complete consume operation. Two requests using the same
code and different usernames can both pass before either clears it.

Recommendation:

Provide one atomic consume operation for the installation code and coordinate it with first-user
creation. If creation fails, restore a fresh code deliberately rather than leaving an ambiguous
partially completed setup. Add a concurrent setup test that proves only one administrator can be
created through this path.

### AUD-008: JSON request framing is too permissive

Severity: Low

Status: Confirmed.

`internal/api/api.go:140-147` treats EOF on the first decode as success and never performs a second
decode to require EOF. An authenticated empty request to the maintenance endpoint reaches the
service with `enabled=false`, and concatenated JSON values are accepted with everything after the
first value ignored.

Recommendation:

Require the first decode to succeed with a value, then require a second decode to return exactly
`io.EOF`. Add tests for empty bodies, whitespace-only bodies, two JSON values, unknown fields, and
oversized bodies, including exact `SQS-C061` behavior.

### AUD-009: Responsive tables lose header semantics

Severity: Low

Status: Confirmed in static code. Assistive-technology behavior was not exercised in a browser.

At widths up to 768 px, `internal/web/static/app.css:329-332` applies `display:none` to `thead` and
generates labels from `td[data-label]::before`. This removes table headers from the accessibility
tree in common browser implementations and relies on generated content that is not consistently
announced. The pattern is used by the library, backup, settings, and activity tables.

Recommendation:

Keep semantic headers available to assistive technology, or render the mobile presentation as
explicitly labelled records instead of visually reshaping a table. Add a browser accessibility test
at the mobile breakpoint. This follows `accessibility.md` section "Vision" and
`lists-and-tables.md` section "Best practices" from the Apple HIG reference set.

### AUD-010: The implemented assurance level is below the documented plan

Severity: Medium

Status: Confirmed and partly acknowledged in `docs/17-ci-cd-and-releases.md:98-100`.

Evidence:

- `docs/04-requirements.md:177` requires at least 80 percent line coverage under `internal/`.
  The measured aggregate in this audit was 66.7 percent.
- Package coverage was especially low in `internal/api` at 17.0 percent and `internal/web` at
  59.4 percent. Safety-critical helpers were also below the target, including `internal/fsx` at
  66.4 percent.
- The repository has 336 `Test` functions, one fuzz target, and no benchmarks. The test plan calls
  for fuzzing identifiers, cron, retention, encryption, CIDRs, display names, and import paths.
- There is no `api/openapi.yaml`, OpenAPI contract suite, chromedp browser suite, gosec job, nightly
  fuzz job, reproducibility job, or automated ZAP job.
- CI does provide strong race-enabled unit and PostgreSQL integration tests, a 66-case scenario
  suite, image smoke tests under two UIDs, binary `govulncheck`, pinned actions, SBOM provenance,
  and release signing.

Recommendation:

Do not describe the aspirational table in `docs/16-testing.md` as the current gate. Keep the honest
"pending" list in `docs/17`, add owners and target releases, and prioritize coverage of recovery,
audit failure, authentication concurrency, and API request contracts before broad percentage work.
An OpenAPI contract and browser accessibility smoke test provide more immediate value than raising
coverage through low-risk getters.

### AUD-011: Release and architecture documentation is stale

Severity: Low

Status: Confirmed.

- `README.md:79-87` labels the project as version 1.0 while the audited commit is tagged `v1.2.0`
  and the reference Compose deploys the `1.2` series.
- `AGENTS.md:6-12` says only versions 0.1 through 0.6 are implemented.
- `docs/21-tasks.md:9-35` lists packages such as `internal/errs`, `internal/logx`,
  `internal/library`, `internal/backup`, `internal/reconcile`, and `internal/clock` that do not exist
  in the current tree. Much of that behavior is now consolidated elsewhere.
- `docs/21-tasks.md:530` states that individual acceptance criteria were not checked, which makes
  the status table difficult to verify mechanically.

Recommendation:

Update release status and the package map, and make the task status point to executable checks or
acceptance records. Keep unvalidated Resolve-dependent behavior clearly separated from completed
software work.

## UI and interaction review

### Summary

The interface is restrained, coherent, and appropriate for an operational tool. The screenshots and
templates show clear hierarchy, stable navigation, useful status density, and a visual language that
does not look like a generic component library. No critical usability issue was found in the static
review.

### Critical issues

None found.

### Improvements

- Fix the mobile table semantics described in AUD-009.
- Add browser coverage for keyboard traversal, focus after validation errors, dialogs or confirmation
  flows, 200 percent zoom, and 360 px layouts. The current HTTP and template suites cannot validate
  focus, announcement, clipping, or generated-content behavior.
- Test both color schemes with automated contrast checks. The tokens look deliberate, but static
  review cannot prove every text and state combination.

### Craft notes

- The protection timeline is a distinctive and domain-specific visualization, not decoration.
- Statuses pair shape or text with color in `internal/web/static/app.css:225-235`, following
  `color.md` section "Inclusive color".
- The compact typography and tabular numerals suit backup sizes, dates, and operational tables.
- The layout adapts from a desktop sidebar to a mobile navigation row at
  `internal/web/static/app.css:316-339`, consistent with `layout.md` section "Adaptability".

### What works

- `internal/web/templates/layout.html:14` provides a skip link.
- `internal/web/templates/layout.html:17-31` labels navigation and exposes the current page.
- `internal/web/templates/partials.html:1` gives flash messages `status` or `alert` roles.
- `internal/web/static/app.css:77` provides a visible keyboard focus indicator.
- Mobile controls have a 44 px minimum target at `internal/web/static/app.css:337-338`, aligned with
  `accessibility.md` section "Mobility".
- Reduced-motion preferences are honored at `internal/web/static/app.css:346-348`.
- Light and dark color tokens are defined, while important state remains legible through labels and
  shapes rather than color alone.

## Architecture and security observations

No additional finding was opened for the following reviewed areas:

- PostgreSQL identifiers pass through regular-expression validation and
  `pgx.Identifier.Sanitize` in `internal/pg/ident.go:15-64`.
- Reviewed production SQL uses parameters for values. Dynamic identifiers are built by the guarded
  helpers. No direct user-text SQL concatenation was found.
- Production external commands go through `internal/execx`, use `exec.CommandContext` with an
  argument vector, and pass database passwords through a temporary `PGPASSFILE`.
- Child-process groups and Linux parent-death signals are configured in `internal/execx`; no
  production `pkill` or foreign-PID cancellation path was found.
- Secret values implement redacted string, Go-string, slog, and JSON representations. The top-level
  slog handler also redacts sensitive field names.
- The container runs sqsolve as a non-root user with a read-only root filesystem, all capabilities
  dropped, `no-new-privileges`, a bounded tmpfs, and rotating logs.
- PostgreSQL and builder images are pinned by digest. GitHub Actions are pinned by commit SHA.
- The PostgreSQL image tag and digest match between the Dockerfile and reference Compose.
- The audit log is hash-chained and serializes append operations. AUD-004 concerns callers that do
  not enforce a successful append.
- Backup, import, restore, revert, trash, and reconciliation paths have extensive real-PostgreSQL
  coverage and all tested scenarios passed.

These observations are not formal proofs. They record that no contrary implementation was found in
the reviewed production paths.

## Verification performed

| Check | Result |
|---|---|
| Clean baseline before report | Pass |
| `make check` | Pass |
| Formatting and `go vet` | Pass |
| staticcheck v0.8.2 | Pass |
| Race-enabled Go tests | Pass |
| Error-code consistency | Pass |
| `govulncheck` | No reachable vulnerability found; one advisory exists in required modules but not in called code |
| `make test-integration` with real PostgreSQL | Pass |
| `make scenarios` | Pass, 66 of 66 |
| `make tidy-check` | Pass |
| `make pg-image-check` | Pass |
| 10-second identifier fuzz run | Pass, 749,068 executions |
| Aggregate `internal/` statement coverage | 66.7 percent |
| Docker image and Compose smoke test locally | Not run because the Docker daemon was unavailable |
| Browser and assistive-technology test | Not available in the repository |
| gosec and OWASP ZAP | Not run; tools and automated jobs are not present |
| Real DaVinci Resolve validation | Not run in this audit |

Selected package coverage from `go test -cover ./...`:

| Package | Coverage |
|---|---:|
| `internal/auth` | 79.9% |
| `internal/core` | 75.0% |
| `internal/audit` | 75.8% |
| `internal/fsx` | 66.4% |
| `internal/scheduler` | 67.4% |
| `internal/web` | 59.4% |
| `internal/api` | 17.0% |

## Residual product risks

The repository itself records Resolve-dependent gaps that automation cannot settle. They remain
material even after the code findings above are fixed:

- Resolve on macOS and Windows has not been validated.
- Collaboration between two real workstations has not been validated.
- Exchange through Resolve's own Backup and Restore has not been validated.
- Per-person credentials have not been validated with real Resolve.
- Maintenance through `NOLOGIN` and the user-visible Resolve behavior remain to be confirmed.
- The full cross-version Resolve and PostgreSQL compatibility matrix remains incomplete.

Keep these limitations in release notes and require a disposable library test before client projects
depend on any unvalidated path.

## Recommended order of work

1. Fix and fault-test control-plane recovery, AUD-001.
2. Correct and test the durable pair-write protocol, AUD-002.
3. Make secret modes explicit and permission changes fail closed, AUD-003.
4. Define and enforce audit failure semantics, AUD-004.
5. Make TOTP replay, last-admin protection, and setup-code consumption atomic, AUD-005 through
   AUD-007.
6. Tighten JSON framing and mobile table semantics, AUD-008 and AUD-009.
7. Close the highest-value assurance gaps and align documentation, AUD-010 and AUD-011.

## Audit limitations

This audit did not modify product code, run destructive tests against a user installation, simulate a
real power cut or storage-controller cache loss, start Docker, use a browser or screen reader, or run
DaVinci Resolve. Filesystem durability conclusions are based on POSIX rename and `fsync` semantics
and on the exact ordering in the source. Concurrency findings are based on non-atomic control flow
and should be pinned with deterministic barrier tests during remediation.
