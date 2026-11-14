# Changelog

Format: one section per version, newest on top. The notes of each version on GitHub are taken
from here (`.github/workflows/release.yml`).

## [Unreleased]

## [1.2.5] 2026-11-13

The rebuilt PostgreSQL 18.6 image: the sqsolve image is built on it, and the compose file pins it.
No change in sqsolve itself.

- Dependencies: the PostgreSQL image is the rebuild of `postgres:18.6-trixie` published on
  2026-11-05 (digest `e3d24f69...`, same PostgreSQL 18.6, `18.6-1.pgdg13+2`), in the `Dockerfile`
  and in `deploy/compose.yaml`; the release workflow uses `softprops/action-gh-release` 3.0.4
  (Dependabot; both published more than 7 days before).

Upgrade: with the `1` tag, `docker compose pull` and `docker compose up -d` (TrueNAS: Update) bring
the new sqsolve image. The `postgres` service keeps the image line of your compose file: replace it
with the one below (user guide, "PostgreSQL minor updates"); PostgreSQL restarts, so back up first.
There is no migration and the PostgreSQL version is the same.

```
    image: postgres:18.6-trixie@sha256:e3d24f6906d0760bf4da0dae872415391ededefad23fd471c47dc53b0ddf3c42
```

Rollback: image `1.2.4`. The `postgres` line can stay as it is: it is the same PostgreSQL 18.6.

## [1.2.4] 2026-11-13

Fixes for the known issues of 1.2.3 (K-01 to K-11) and from the repository audit of 2026-11-12
(purge, trash and recovery, retention, blocked backups, the backups volume, `pg_hba.conf`, sessions,
the API, secrets in output, tests and CI). What is still known to be wrong is in `docs/19-risks.md`
§5 (K-03 and K-12 to K-20).

- Security: the experimental export no longer shows the PostgreSQL socket path, the
  `sqsolve_admin` role and the database name to every signed-in user when the connection to
  PostgreSQL fails (known issue K-02). The detail of `SQS-E005` now ends in "the connection to
  PostgreSQL failed (the cause is in the sqsolve log)"; the whole error goes to the process log,
  one `WARN` line per failed request, and the panel and the API show it only to whoever has the
  `diagnostics` permission (administrators; with a token, the `admin` scope).
- Security: the PostgreSQL socket path and the `sqsolve_admin` role of a failed connection no
  longer reach every signed-in user outside the export either. The Health page with its doctor
  checks, `GET /api/v1/system/status`, every API problem and the notice of a panel action that
  failed show the driver's text, and the path of the superuser password file, only to whoever
  has the `diagnostics` permission; everyone else reads "the connection to PostgreSQL failed
  (the cause is in the sqsolve log)". The message of `postgres.down`, also in the
  notifications, and the stored error of a failed export preparation keep that text for
  everyone. The whole error goes to the process log; the ping and the superuser connection
  keep the driver's error so it can be recognized. `sqsolve doctor` and the diagnostic bundle
  are unchanged.
- Security: the rewrite of `pg_hba.conf` no longer uses the factory defaults when the stored
  settings cannot be read, which turned "TLS only" off and opened the maintenance database. It
  writes nothing and fails with `SQS-P044`, which the dashboard and Health report as for any
  rewrite that did not take effect.
- Security: when PostgreSQL rejects a new `pg_hba.conf` (`SQS-P042`) and the previous file cannot
  be put back either, the error, its audit entry and Health say so; that failure was ignored, and
  the rejected file stayed on disk without a word.
- Data safety: with the backups disk not mounted (an empty folder at the mount point), the purge
  of a library in the trash and the drop of a previous state (`__prev`: its expiry, a newer
  restore, the purge, the orphan sweep) took a backup row as proof although its file could not be
  seen, since the path of every backup still checks there, and dropped the database. They now
  check the marker of the volume first: the purge stops at `check_backup` with its error
  (`SQS-S001` when it is missing) before its journal, and the `__prev` is kept, with that error
  in place of `SQS-R055`. Also in 1.2.3.
- A backup whose path cannot be checked because of a disk error (an I/O error, a folder sqsolve
  may not enter, a stale mount, a file where a folder should be) is now blocked while the error
  lasts (`SQS-B045`, 409 in the API, the backup page hides Restore, Download and Export). Before,
  it counted as a readable verified backup: retention could expire readable backups in its
  stead, down to the only readable verified one, a purge could accept it as the final backup and
  drop the trash database, and the guard of the manual delete protected it and not the newest
  readable backup. Every start and the catalog check report such rows in a new
  `backup.path_unchecked` event (error) with the first cause. The row is not rewritten, cannot
  be removed with `sqsolve catalog forget` (`SQS-B044`), and counts again as soon as the check
  works (`docs/runbooks/invalid-backup-path.md`).
- The check of the catalog paths at a start runs in the background: the panel, the connection
  to PostgreSQL and reconciliation no longer wait for it. `/readyz` is unchanged. Before the
  panel opens sqsolve still reads the marker of the backups volume and lists `_system/` once.
- A purge that stops with `SQS-L046` now stops before anything is journaled or dropped, so the
  library stays in the trash with its database and can be recovered; new job step "Check other
  databases"; a previous state left invalid by an interrupted DROP DATABASE is reported there;
  the check runs again before the roles are dropped.
- A purge no longer fails with `SQS-L047` when the purge of expired items already dropped and
  forgot the previous state of the same library, and that pass now drops an expired previous
  state before it queues the purge (known issue K-11).
- An interrupted purge whose previous-state database is still on the server no longer reports
  `SQS-L046` at every start: the journal records the previous state, the library stays in the
  trash and can be purged again; a journal of an earlier version uses the catalog (known
  issue K-04).
- `SQS-L043` removed: the unique index on the identifier of libraries that are not purged
  makes it impossible.
- Restoring by replacing, moving a library to the trash, recovering it from the trash and a purge
  started by hand have the unique job identity of ADR 0028 (K-03, ADR 0029): a second identical
  request while the job is queued or running gets that job instead of queueing another one, and
  writes no audit entry. A second replacement with another backup or another choice for the open
  sessions is refused with `SQS-J005` (409 in the API, which used to answer 202 and queue a second
  job). A purge started by hand and the automatic purge of expired trash share one job. From the
  console, a `restore --replace` repeated while the server is already running the first one still
  queues a second job (K-03).
- Retention: a backup taken before a restore (`pre_restore`) no longer takes one of the
  `keep_last` places (known issue K-09). Every replacement or revert leaves two of them,
  protected for 30 days, and they were also counted among the latest N: a few restores in a row
  pushed the regular backups out, the one just restored included (seen in the 1.2.2 acceptance).
  `keep_last` now counts the backups of the other kinds. A `pre_restore` backup is still kept
  while fewer than N backups of other kinds are newer than it, so no backup that 1.2.3 kept
  expires because of this change, and a library that takes no new backups keeps its
  `pre_restore` backups past their 30 days. The daily, weekly and monthly rules, the 30 days of
  protection and the minimum of verified backups are as before. The count of kept backups in the
  Schedule tab can go up.
- Settings, General sets the schedule mode of the default policy for new libraries, as the
  Schedule tab of a library does: activity-based with its interval (5, 10, 15, 30 or 60 minutes)
  or a cron expression, and it shows the time zone of the policy. The old "Schedule (cron)" field
  only stored an expression that the activity-based mode never ran (known issue K-10). In
  activity-based mode the cron field is not read; in cron mode an empty or wrong expression is
  refused with `SQS-J020`. Saving no longer puts the default policy back to its factory values:
  saving the field empty in activity-based mode did, the retention numbers of the same form
  included. Upgrade: an expression typed in the old field while the default mode was
  activity-based had no effect and is not kept, so choose "Cron expression" in Settings, General
  if new libraries must use it; where the field was once saved empty, the default policy has been
  the factory one since then and stays so until General is saved again. Libraries that have a
  policy of their own are not changed.
- The rewrite of `pg_hba.conf` that follows a settings change ("TLS only", access of library
  users to the `postgres` database) or the addition of a person has its own audit entry,
  `postgres.hba`, with who made the change: `accepted` before the file is written, then `ok` or
  `failed` with the error. Without the entry the file is not rewritten (`SQS-X040`); the settings
  or the person stay stored and take effect at the next rewrite. Closes known issue K-07.
- The errors of the three statements that carry a SCRAM verifier keep their cause, so code can
  match it with `errors.Is` and `errors.As`: `SQS-P001`, `SQS-P050` and `SQS-P051` in a failed
  password change, and the SQLSTATE of PostgreSQL in a failed password change, role creation
  (`SQS-P020`) or preparation of `sqsolve_admin` (`SQS-P002`). The verifier stays redacted in
  their text, in the logs and in the PostgreSQL error they hand out. The messages do not
  change. This was known issue K-08.
- API: an empty list is `[]`. The lists of jobs, job steps, job log lines, events and audit
  entries, and `attention` in `GET /system/status`, came back as `null` when they were empty.
  `GET /jobs/{id}/logs` answers `404` with `SQS-J002` for a job that does not exist; it answered
  `200` with `null` items (known issue K-01). `GET /jobs/{id}` answers `500` when the steps of
  the job cannot be read; it answered `200` with `null` steps. API clients that read `null` as an
  empty list keep working; a client that asks for the logs of a job that does not exist, one
  that housekeeping removed 90 days after it ended included, now gets `404`.
- `sqsolve-init` checks the mode of a file it has just written the same way it checks one that
  was already there (`instance_id`, the master key, the `sqsolve_admin` password, the PostgreSQL
  superuser password and the initial `pg_hba.conf` and `pg_ident.conf`): on a volume that does
  not keep the mode, that run stops with `SQS-C085 ... it stays 0700` and PostgreSQL and
  `sqsolve` are not started. Until now only the next run caught it, after `sqsolve` had refused
  to start with `SQS-C010` (known issue K-05 of 1.2.3). The file stays in place and the next run
  checks it again; no new secret is generated.
- The event `postgres.cluster_accepted` is emitted only after the new cluster identifier and its
  audit entry are stored. Until now it was also emitted when that transaction failed, and it said
  that `SQSOLVE_PG_ACCEPT_SYSTEM_ID` could be removed while the next start still needed it (known
  issue K-06).
- A blocked backup (`SQS-B043`, or `SQS-B045` while its path cannot be checked) no longer stands for
  data sqsolve cannot read back. The expiry of a previous state, the purge of a library in the trash
  and a replace that supersedes an older previous state accepted it as the backup of a `__prev`
  database and dropped that database; they now take a new backup first and keep the `__prev`
  (`SQS-R055`, `library.prev_kept` warning) when none can be taken. In activity mode, a library
  whose newest verified backup was blocked skipped its scheduled backups for as long as nothing
  changed; the gate now compares with the newest backup sqsolve can read, as the `backup.stale`
  warning already did, and backs up when there is none.
- Retention no longer deletes the backups of a library that has no backup policy of its own: one
  that "Rebuild the catalog" registered (state Purged) or one not active yet. It pruned them with
  the default policy for new libraries (Settings, General), so after installing from scratch on
  the backups volume of a lost server and rebuilding the catalog without the recovery kit, the
  first retention pass (04:00, or "Apply retention") deleted every backup beyond the 10 latest,
  14 daily, 8 weekly and 12 monthly of the new installation, whatever the old one kept, pinned
  ones included: the manifests record neither policies nor pins. Such a library now keeps every
  backup until a policy is saved in its Schedule tab, which says so and shows how many backups
  that policy would delete, and the retention job names it in its log. Leftovers of failed
  backups are still cleaned, and libraries with a policy, purged ones included, are pruned as
  before.
- Retention and the manual delete of a backup check the marker of the backups volume first.
  While the backups disk was not mounted (Docker leaves an empty folder in its place), they
  recorded backups as deleted although their files were still on that disk, removed the rows of
  old failed backups without their leftovers and announced deletions that never happened; once
  the disk was back those files were outside the catalog, retention never removed them and a
  catalog rebuild skipped them. Now nothing is deleted or recorded deleted unless the volume
  carries this installation's marker of the backups volume: the retention job fails with the
  code of the volume (`SQS-S001` for a disk that is not mounted; `SQS-S002`, `SQS-S003` or
  `SQS-S019` for an unreadable marker, another installation's or one of another volume) and Retry
  or the next run applies it once the disk is back, and a delete by hand is refused with the same
  code. Files left this way by earlier versions stay on the disk; the catalog check counts them
  among the files that are not in the catalog.
- A delete whose catalog write fails after its database was renamed to the trash name now fails
  with `SQS-L056`, keeps its lock and stays pending reconciliation, and the next start records
  the move with a `library.trashed` event. Until now the job just failed, and the next start
  reported the library missing and said to recreate it from its latest backup while its data sat
  under a trash name no row recorded. A delete of a library without a database that cannot be
  recorded gives the access back. Recover from the trash now journals its rename (`SQS-L057`
  without the journal) and is settled by reconciliation: a failure after the journal
  (`SQS-L058`) or a stop is finished at the next start (the library active with its access and
  backup schedule, or still in the trash if its database was not moved) instead of leaving it in
  the trash with its database back under its name and Recover answering `SQS-L051`. A purge of a
  library without databases whose catalog write fails after its roles were dropped stays pending
  reconciliation too.
- Saving Settings, General again, even unchanged, rewrites `pg_hba.conf` when the last rewrite
  did not take effect (it failed, or its audit entry could not be written, `SQS-X040`). Until
  now it answered "Settings saved." without rewriting, so "TLS only" or the access to the
  `postgres` database could show one value while PostgreSQL applied the previous rules. Until a
  rewrite takes effect, whatever caused the one that did not (a settings or member change, a job,
  the start), the dashboard lists it under Attention, also in `GET /api/v1/system/status`, and
  the Health check `pg_hba.conf` fails with its error.
- Revoking a person's credential, and putting a library in maintenance with "Also close open
  sessions", no longer report success when the open sessions could not be listed or closed.
  The revocation stays recorded and `SQS-L081` says that a workstation still connected keeps
  working until it disconnects; the library stays in maintenance and `SQS-L082` says how many
  sessions were closed. Both are recorded as failed in the audit log.
- The automatic purge of a library whose time in the trash ended now has its audit entries:
  `library.purge` with actor `system`, `accepted` with the job and the details `slug` and
  `purge_after`, then the result. Until now it dropped the database and the roles with no entry,
  and a purge by hand that joined it wrote none either. Without the entry the purge is not
  queued: the pass logs a warning with `SQS-X040` and a later pass tries again.
- Cancelling a job while its `pg_dump`, `pg_restore`, `vacuumdb` or `restic` runs now always
  ends with SIGKILL to the program's whole process group. When the SIGTERM of the cancel made
  the program exit before sqsolve started waiting for it, that SIGKILL could be skipped, and a
  process of the group that ignores SIGTERM kept running after the job ended.
- Errors that were dropped before a success no longer are. A delete stops with the error of a
  failed lookup (the database of an initialization window, a role in the lock) or of the removal
  of `CREATEDB`, and a recovery from the trash with that of the lookup of the library's name,
  instead of reading it as "no database" or "role gone". Maintenance, and reopening an
  initialization window, whose catalog write fails are recorded failed and put back the access
  they changed. An expired window loses its `pg_hba.conf` lines at once, not at the next rewrite.
  The purge of expired items keeps a previous state left invalid by an interrupted
  `DROP DATABASE` (`SQS-R062`) instead of logging it deleted and forgetting it, and says when the
  catalog could not forget one it dropped. Retention stops when the library cannot be read, and
  an audit result that cannot be written after a failed change is logged.
- Error answers of the API and the panel. `GET /api/v1/libraries/{id}/backups` answers `404`
  with `SQS-L002` for a library that does not exist; it answered `200` with an empty list.
  Cancelling a job that has already ended is refused with the new `SQS-J006` (`409` in the API);
  the panel and the API said `SQS-J002` "job not found" (`404` in the API). When the catalog
  cannot be read, `GET /jobs/{id}` and the routes that first read the library of their path
  (`GET` and `PATCH /libraries/{id}` and the `GET` routes of its projects and grades) answer
  `500` instead of `422`. A `PATCH` of a library or a reveal whose answer cannot be read back
  answers a `500` problem; the handler panicked and the panel's plain `SQS-X999` answered. An
  upload to the import folder is refused with the error of the backups volume check (`SQS-S001`
  on an unmounted disk) before anything is written; it could fill the empty mount point. A test
  now covers the panel's answer to a panic.
- Small fixes from the audit. `sqsolve-init` stops with the new `SQS-C055` and the path when it
  cannot write a file (a full disk, a refused mode on the new `.partial` file); it returned the
  bare error. A `secrets.String` prints `[redacted]` with every `fmt` verb and from an unexported
  field; `%d`, `%p` or a `%v` of `pg.Client` printed the value. `pg.Bootstrap` behind a lock
  reports `SQS-P050` inside `SQS-P002`, and its first read, which had no code, reports the same.
  A disk error while `fsx.Confined` follows a symlink of a backup path is `SQS-B045`, blocked
  while it lasts; it was `SQS-B043`, which `sqsolve catalog forget` can remove. `08-security.md`
  says what is checked of the panel key and names the secret tests that exist.
- Small fixes in the core. The hours without a backup before the warning and the maximum upload
  are refused outside 1 to 87600 and 1 to 1048576 MB (the new `SQS-C033` and `SQS-C034`), and
  larger stored values are read capped; they overflowed. The `settings.update` audit entry
  carries every setting, the second factor and the default policy included. The newest verified
  backup, a manual delete and retention stop with the error of a lookup that fails; they
  answered a previous-state backup, deleted the newest verified backup or skipped the cleanup of
  failed backups. A precreated library that cannot be recorded active fails its job and is
  rolled back; it succeeded while still being prepared. A recovery from the trash refuses a name
  an invalid database holds with `SQS-L005` before its journal; it failed on the rename with
  `SQS-L058`, pending reconciliation. Reconciliation at start touches no file on a backups volume
  that is not this installation's, and its note says why; it removed the `.partial` files of
  another installation's volume. The retention log no longer names libraries without backups.
- Panel: while the job that creates a library is queued or running, the Connection tab of the
  library says "Preparing the library" and reloads itself until the job ends. It showed
  "Initialize the library from Resolve" with "Reveal password" (with an empty database, the
  values to connect) as soon as the job had set the state, one step before it writes the
  `pg_hba.conf` lines of the library, and a workstation that connected then was refused once
  ("pg_hba.conf rejects connection"). `docs/10-api.md` says that an API client waits for that
  job to succeed before it connects.
- Reconciliation: a recovery from the trash pending reconciliation now supersedes an older
  pending purge of the same library. A purge that stopped with its trash database still there,
  followed by a recovery that moved that database back and stopped before reactivating the
  library (`SQS-L058`), made the next start finish the purge first, which refused with
  `SQS-L046` naming the library's own database. Nothing was dropped.
- Access: the session of a revoked per-person credential that could not be closed (`SQS-L081`)
  can be closed now with "Close" in the Activity tab or with maintenance and "Also close open
  sessions"; both left it out and "Close" answered `SQS-L016`. The revoked role stays `NOLOGIN`.
- Dependencies: a base image that fixes a vulnerability may be taken before the 7 days the
  project waits, as a Go module already could (`docs/17-ci-cd-and-releases.md` §7). The digest
  is changed by hand, the change goes through the full CI and the release notes name the
  vulnerability, as 1.2.3 did for the Go builder image. Dependabot's cooldown stays at 7 days.
- Dependencies: the image workflow uses `docker/login-action` 4.8.0 and `sigstore/cosign-installer`
  4.1.3 (Dependabot; both were published more than 7 days before). The commit of the first one says
  4.7.0 in its title by mistake: it pins 4.8.0.
- Tests: `TestCancelKillsTheWholeGroup` (`internal/execx`) failed now and then under load
  although `Run` had killed the whole process group: PID 1 reaps the killed member at any
  moment, and the test read it as running when it caught that reaping in progress. The test now
  checks once, without waiting, that when `Run` returns the member is gone, waits to be reaped
  or has a SIGKILL pending.
- Tests: the error codes that had no test of their code and message have one. Failures are
  simulated for `SQS-B002`, `SQS-B010`, `SQS-B011`, `SQS-B012`, `SQS-P021`, `SQS-P030`,
  `SQS-P042`, `SQS-P043`, `SQS-R010`, `SQS-R011`, `SQS-R020`, `SQS-R031`, `SQS-X002` and
  `SQS-X998`, and the tests of `SQS-A024`, `SQS-B041`, `SQS-C020`, `SQS-C063`, `SQS-P050`,
  `SQS-P051`, `SQS-R060`, `SQS-R061`, `SQS-S004`, `SQS-S010`, `SQS-S015` to `SQS-S017` and
  `SQS-X011` now check the message too. The backup has one more fault point for tests
  (`backup.count`); nothing changes in how sqsolve behaves.
- Tests: `TestPanelEndToEnd` (`internal/web`) failed now and then under load with "pg_hba.conf
  rejects connection": it connected as soon as the new library was waiting for Resolve, a state
  the creation job sets one step before it rewrites `pg_hba.conf` and reloads PostgreSQL. The
  panel tests now wait for that job to end and then connect once, without the retries that hid
  the same race. The reload itself needs no wait: PostgreSQL applies it before it accepts the
  next connection.
- Tests: the throwaway cluster of `internal/testpg` starts again on another port when another
  process took its port first ("Address already in use"), and it stops when its test process is
  killed. The next start of a cluster removes the folders that killed tests left in
  `~/.cache/sqsolve-test` (`pg-<pid>-<digits>`, only when that process and the postmaster are
  gone); folders of earlier versions of the fixture stay.
- Tests: `TestAPIExportWithPostgreSQL` (`internal/api`) failed now and then: it looked for the
  letters `PK` anywhere in the body of a refused download, and the identifier of a backup in the
  path of the error can contain them. It now looks at the start of the body, where a download
  begins.
- Tests: the rule that external programs run only through `internal/execx` is written as a rule
  for the code that ships (`AGENTS.md`, `docs/16-testing.md` §4): test code, `internal/testpg`
  included, starts what it needs directly, with an argument array. A new test fails when a file
  that ships starts a program outside `internal/execx` or imports `internal/testpg`.

Upgrade: with the `1` tag, `docker compose pull` and `docker compose up -d` (TrueNAS: Update). There
is no SQLite migration, and the PostgreSQL image is the same as in 1.2.3 (`postgres:18.6-trixie`).
Retention now applies only a policy of the library's own: a library without one (registered by a
catalog rebuild) keeps every backup until a policy is saved in its Schedule tab, and the retention
job names it in its log. In Settings, General, choose the schedule mode of the default policy again
if a cron expression had been typed there while the mode was activity-based. API clients: the
answers of some error cases changed status (the entries above name each one), and a new library is
ready for a workstation when its creation job has succeeded.

Rollback: image `1.2.3`; there is no schema change. Before going back, let 1.2.4 settle the jobs
that are pending reconciliation (a delete that ended with `SQS-L056`, a recovery from the trash that
ended with `SQS-L058`): 1.2.3 does not finish them. 1.2.3 again applies the default policy of
Settings to the libraries that have no policy of their own.

## [1.2.3] 2026-11-10

Fixes from the repository audit of 2026-11-10 (network rules, schedules, retention, restore as
new, verification, setup, console, tests and CI).

- Security: a library whose identifier is a keyword of `pg_hba.conf` no longer changes what the
  file means. `all`, `sameuser`, `samerole`, `samegroup` and `replication` are valid identifiers,
  and until now they were written bare: from the moment a library called `all` was created, its
  line admitted every library role to every database from that library's addresses, so the
  network rules of the other libraries and the closed maintenance database stopped applying
  (passwords and database privileges still did); one called `replication` or `sameuser` got a
  line that did not match its own connections. The database and the roles of every library line
  are now written between double quotes, where PostgreSQL reads them as plain names. The file is
  rewritten and reloaded at the first start of this version.
- The fixed maintenance schedules follow `TZ`. They were stored with the time zone of the first
  start and kept it: after a change of `TZ`, retention, the weekly verification, optimization,
  the control plane backup, the catalog check, the purge and housekeeping went on running at
  their hours of the old zone. The next start now moves them to the new zone, each with its own
  expression and state; a run that was already due is still made, or reported as missed, first.
- Console: `sqsolve help` shows `--terminate` for `restore --replace` and lists itself; the flag
  worked but the usage text did not name it. `docs/08-security.md` wrote the recovery command as
  `sqsolve admin create --role admin`, which takes `--role` as the user name; it is
  `sqsolve admin create <user>`.
- The files of catalog migrations 1 to 4 have English names (`0001_initial.sql`,
  `0002_account_lockout.sql`, `0003_activity_based_backups.sql`, `0004_export_from_backup.sql`);
  their statements are the same. A migration is known by its number and its statements, so an
  installed database, which recorded the old names, opens as before and applies nothing again.
- Tests: `TestControlPlaneBackupKeepsTheNewestCopies` pinned the job clock to October 2026 while
  the audit log stamps its entries with the real clock, so `make check` failed from 2026-11-01
  on. The test now runs the job in the month its audit entry was written in.
- A manifest that is JSON but not a valid version 1 manifest (`SQS-B042`) is a verdict of the
  level 1 check, as `docs/11-backups-and-automation.md` §5.1 says: the backup is marked failed.
  Since 1.2.2 the check answered that it could not run (`SQS-B070`), so the backup stayed
  verified and kept counting as the latest verified backup and among the verified backups that
  retention keeps, while no restore could read it.
- A library restored as new keeps the schedule mode of the library its backup came from. The
  copy of the policy left the mode out, so the backup of a library with activity-based backups
  gave a new library with a cron schedule at the check cadence: a backup every 5 to 60 minutes,
  with changes or without.
- The row of a new library is forgotten only when its job was not queued. When the request was
  cancelled right after the job was stored, the row was removed and the queued job failed with
  `SQS-L002`.
- Reconciliation no longer notes that it deleted the leftovers of an interrupted backup whose
  catalog path is not valid (`SQS-B043`); it only says that its files were not touched.
- Setup checks the address the workstations use before it creates the administrator. An address
  with a port, or the IPv6 address the panel was opened with, was refused (`SQS-C030`) only when
  the settings were saved: the account already existed and the installation code was spent, so
  the setup ended on the login page with none of its settings stored.
- Retention leaves a blocked backup (`SQS-B043`) out of its candidates. It was already left out
  of the verified backups retention keeps, but it still took one of the places of the policy
  (latest, daily, weekly, monthly): a readable backup could expire in its stead, and a blocked
  one that fell among the expired made every pass log a deletion that never happens. The count
  of the Schedule tab no longer includes blocked backups.
- Importing a file that is not in the import folder says so (`SQS-B055`). The job answered
  `SQS-B051`, "the file is outside the import folder", for a name that did not exist; that code
  is left for a name that leads out of the folder.
- API: `PUT /libraries/{id}/backup-policy` answers with the policy as stored, the object the
  next `GET` returns. It echoed the body: an empty `mode` instead of `cron`, and in activity mode
  the `cron` that was sent instead of the check cadence.
- A long error shown as a notice in the panel is shortened at a character boundary; a name with
  accents that fell on the cut left half a character.
- Tests: the error codes that no test named are now checked with their message (`SQS-A002`,
  `SQS-A005`, `SQS-B051`, `SQS-C012`, `SQS-C014`, `SQS-C050` to `SQS-C053`, `SQS-E004`,
  `SQS-J003`, `SQS-J004`, `SQS-P040`, `SQS-P041`, `SQS-X001`, `SQS-X003`, `SQS-X900`).
- The image is built with Go 1.27.4. The pinned builder image carried Go 1.27.3, whose
  `net/http` has GO-2026-6859 (an HTTP/1 server crash through `MaxBytesReader`, which the panel
  uses for every form), and `govulncheck` on the binary inside the image stopped the
  publication. The new builder image was published on 2026-11-10: it is taken before the 7 days
  the project waits for base images because it is the security fix.
- Tests: `TestPanelCertificatePair` passed only when two of its steps ran within the same
  second, and failed in CI when they did not. In `make scenarios` the simulated workstation
  connected to the second library before PostgreSQL had loaded the new `pg_hba.conf`, and the
  script did not check that step: it failed much later, at the console backup. The workstation
  now tries again while `pg_hba.conf` refuses it, and the creation and the activation of the
  second library are checked (68 checks).
- Documentation: every document describes the current version as it is, checked against the
  code, with what is not implemented or not validated with Resolve said plainly. The task
  breakdown (`docs/21-tasks.md`), `PLAN.md` and the audit report `REPORT.md` are removed: the
  known issues are in `docs/19-risks.md` §5 (two more, K-10 and K-11, found in this audit and
  not fixed), what comes next in `docs/20-roadmap.md` and the code layout in
  `docs/05-architecture.md` §4.

Upgrade: with the `1` tag, `docker compose pull` and `docker compose up -d` (TrueNAS: Update).
There is no SQLite migration. At the first start `pg_hba.conf` is rewritten with the library
names between quotes and PostgreSQL reloads it; open sessions are not affected. If `TZ` was
changed after the first start of the installation, the maintenance jobs move to the hours of the
current zone. API clients that read the answer of `PUT /libraries/{id}/backup-policy` now get the
stored policy. The PostgreSQL image is the same as in 1.2.2 (`postgres:18.6-trixie`).

Rollback: image `1.2.2`; there is no schema change. 1.2.2 writes `pg_hba.conf` again without the
quotes at its first start.

## [1.2.2] 2026-10-26

Stabilization after the repository audit of 2026-10-25 (backup paths, job journal, audit log,
destructive jobs, durability, unique jobs, accessibility), checked on the test installation and
with Resolve (`docs/acceptance/1.2.2.md`).

- Reference compose, tuned for TrueNAS: the image tag is `1`, which every 1.x release moves, so
  TrueNAS offers "Update available" for every release, minor versions included; the image
  workflow publishes that tag (ADR 0027). `x-portals` and `x-notes` give the TrueNAS app a "Web
  UI" button to the panel and a notes card. The three host paths are written once (`x-volumes`)
  and `TZ` once. `sqsolve-init` runs with a read-only root file system and only the capabilities
  `CHOWN`, `DAC_OVERRIDE`, `FOWNER` and `FSETID`, without the image's health check; `postgres`
  runs without capabilities and its health check calls `pg_isready` itself instead of the Perl
  wrapper.
- Backup paths are confined to the backups volume. A manifest is read only when it is a complete
  version 1 manifest (`api/schemas/manifest.v1.json`, `SQS-B042`) whose `file` is the base name of
  a `.backup` file; rebuilding the catalog also requires the manifest to name its own backup file
  and both to stay on the volume once symlinks are resolved. A catalog row whose path is absolute,
  contains `..`, is not a `.backup` under `libraries/` or leaves the volume through a symlink is
  never read, verified, restored, downloaded or deleted (`SQS-B043`, 409 in the API), retention
  leaves it alone, and it is not rewritten: every start and every catalog check report such rows
  in one critical `backup.path_invalid` event, and after review `sqsolve catalog forget
  <backup-id>` removes the row without touching any file (`SQS-B044`,
  `docs/runbooks/invalid-backup-path.md`). A blocked backup does not count as verified for any
  safety rule: not as the latest verified backup, not toward the verified backups retention
  keeps, not for the guard on the newest usable one, and not as the final backup a purge needs.
- The job journal is required. A step is stored before it runs, and when its row cannot be
  stored the step does not run (`SQS-J040`); a journal that changes no row, or fails, is
  `SQS-J041`, and the action it describes is not done. Restoring by replacing now also requires
  the journal of the copy being restored and the one written between the two renames
  (`SQS-R059`): without the second one the original database goes back under its name.
- Audit log (ADR 0026 finished): every state change and disclosure was inventoried. Settings,
  pinning a backup, unlocking a user, turning the second factor on or off, and notification
  channels are now stored in one transaction with their entry; editing a library, changing a
  backup policy, adding a person, installing a panel certificate and cancelling a job write
  `accepted` before the change and their result after it; verifying a backup and rebuilding the
  catalog from the console write their entry with the job. Without the entry the action is not
  done (`SQS-X040`, 503 in the API); a result that cannot be written after the change is
  `SQS-X041`. Logins, logouts, refusals and what sqsolve does on its own stay best effort, but
  a failed entry is now logged instead of ignored, and a second-factor login no longer fails
  after completing the session when only its entry could not be written.
- Purge and recreate no longer report a false success. Every PostgreSQL and SQLite call they
  depend on is checked: role and database lookups, credential storage, grants, the library state
  and the schedules. A purge journals what it is about to drop and records the purged library in
  one transaction; a recreate stops with `SQS-R061` before creating the database when it cannot
  rebuild the access. When either fails after its irreversible step (`SQS-L047`, `SQS-R060`), the
  job fails and stays pending reconciliation, and the next start finishes the purge or adopts the
  restored database; backups are always kept. A drop of the trash database that reports a
  failure may still have happened on the server, so it leaves the purge pending too.
- Changing a password closes the user's other sessions in the same transaction as the new
  password and its audit entry: if they cannot be closed, the password does not change.
- Deleting a backup records it deleted only once the removal of its files is durable: a folder
  that cannot be synced makes the deletion fail and leaves the row, and a retry syncs it again;
  retention removes the row of an old failed backup the same way. The power-loss model of the
  file helpers covers the removal.
- A unique job is its type, its library and its parameters (ADR 0028): a second recreate or
  revert, or a second automatic purge of expired trash, with the same parameters gets the queued
  job, and one with different parameters (for example another backup) is refused with
  `SQS-J005` (409 in the API) instead of silently getting the job of the first request. Backups
  keep "the first request wins".
- `sqsolve-init` checks the mode an existing secret, a TLS key or a folder that holds secrets has
  after it sets it: a volume whose ACL keeps a wider mode (TrueNAS datasets with inherited NFSv4 entries, which
  can also give other users access to the secrets) stops it with `SQS-C085 ... it stays 0700`
  instead of printing `done` and leaving `sqsolve` to fail with `SQS-C010`. Found when updating
  the test installation from 1.1; `docs/12-deployment.md` ("Permissions and ACLs") says how to
  strip the ACL.
- Found while raising coverage: creating a library or restoring a backup as a new one with the
  audit log down left a `provisioning` row, so the same request then failed with `SQS-L004`;
  nothing is left now. A schedule whose date never comes (31 February) is refused with
  `SQS-J020` when the policy is saved, instead of failing without a code after storing it.
  `GET /api/v1/libraries/{id}/backup-policy` answers 404 `SQS-L002` for an unknown library. The
  recovery form of the setup wizard now shows why a recovery was refused (a wrong code or key
  sent you back to the form without a word); a schedule expression typed for the default policy
  is checked also in activity mode (a default policy saved by 1.2.1 with an expression that had
  no effect still gives new libraries their policy and schedule); an unknown maintenance action is `SQS-C041`.
- Tests: statement coverage of `internal/` rises from 70.6 % to 87.1 % (`docs/16-testing.md`),
  with handler tests for every API route and most panel forms, and fault tests for recovery,
  system jobs, PostgreSQL failures and audit failures.
- The identifier suggested for a name that does not start with a letter now starts with
  `library_` (it was the Spanish `biblioteca_`), as the panel already suggested while typing.
- Panel: notices are one block of text next to their shape again, so a long list (for
  example the open connections on the restore page) wraps inside the card instead of turning
  each address into a column that overflows it. Every control takes taps on at least 44 x 44 px
  at every width: buttons, small buttons, "Sign out", the account link and the badges of the
  top bar keep their look with an invisible area around them; sidebar links, tabs and fields
  are 44 px tall. On a phone the top bar no longer grows on short pages.
- Documentation: the restore and delete screens and the mobile navigation in `docs/09` now say
  what the panel does (no reauthentication, ADR 0021; a top bar with a row of sections, no
  dropdown and no bottom actions).
- The replace and revert pages always ask to close the project in Resolve on every workstation
  first. Checked with Resolve 21.1.1 on Linux: Resolve shows nothing when its sessions are
  closed, reconnects on its own, its next save writes its pending changes into the restored
  library, and the open project can keep showing the previous state until it is reopened.

Upgrade: take a control-plane backup, then change the image tag to `1` once (user guide,
"Updating"); on TrueNAS it appears three times in the stored YAML. Pasting the new file instead
needs your paths, `TZ` and user again. There is no SQLite migration. If `sqsolve-init` stops with
`SQS-C085` (or `sqsolve` with `SQS-C010`), the volume has an ACL wider than the file modes: strip
it as `docs/12-deployment.md` ("Permissions and ACLs") says. A critical `backup.path_invalid`
event at the first start lists catalog rows whose path is refused:
`docs/runbooks/invalid-backup-path.md`.

Rollback: image `1.2.1`; there is no schema change. Roll back only when Activity shows no
"reconciliation pending" warning, and keep the control-plane backup: a rollback must not discard
catalog or audit entries written by 1.2.2.

## [1.2.1] 2026-10-22

Fixes from the repository audit of 2026-10-21 (recovery, durability, secrets, audit log,
authentication races, API framing, accessibility, assurance and documentation).

- Recovery of the installation: the recovered database and master key are applied through a
  journal (`app/recovery-journal.json`). The staged key must open the staged copy before anything
  is touched (`SQS-C084`); the current database (with its `-wal` and `-shm` files) and key are set
  aside as a pair, every move is checked and synced before the next, and a start cut at any point
  finishes the recovery at the next start (`SQS-C086`) instead of leaving a database next to a
  key it does not match. A staging that cannot be applied is set aside and sqsolve starts as it
  was, so the setup page stays reachable (`SQS-C084`, `SQS-C089`); a file in the way stops it for
  the operator (`SQS-C087`, `SQS-C088`, `docs/runbooks/recover-installation.md`). Staging writes
  the copy through a `.partial` file and the key last; while a recovery is applied,
  `sqsolve-init` generates no new master key and console commands refuse to open the
  installation (`SQS-C090`).
- Key and certificate pairs (panel and PostgreSQL TLS) survive a power cut: each `.partial`
  file and each rename is synced before the next step, the recovery of an interrupted pair
  removes its files in a safe order and reports what it cannot remove, and a new write of a pair
  (a renewal, an upload) first finishes one that was interrupted. File modes are now set
  before the content is synced. A model of what a power cut keeps checks every state.
- Secrets: sqsolve no longer starts with a master key or `sqsolve_admin` password readable by
  its group (only the PostgreSQL superuser password is shared, at 0440), the same rule
  `sqsolve doctor` applies. `sqsolve-init` stops with `SQS-C085` when it cannot set the mode of
  a secret, a TLS key or the folders that hold them; on the backups volume and the db volume root
  it only warns.
- Audit log (ADR 0026): the entry of a sensitive action is written with it or before it, and
  without it the action is not done (`SQS-X040`, 503 in the API). Actions that run as a job write
  an `accepted` entry with the job in one transaction and the job's result when it ends; changes
  in PostgreSQL or in files (delete a backup, rotate or revoke a credential, network rules,
  maintenance, end a session) write `accepted` before and `ok` or `failed` after; user, role,
  password and token changes are one transaction with their entry; a password, a backup file,
  the recovery kit, the diagnostics and exports are not served without their entry. The Activity
  page shows `accepted` as a neutral badge.
- Authentication: a TOTP code is accepted once even by two sessions at the same time, and a code
  whose use cannot be recorded is refused; two administrators demoting or disabling each other
  at the same time can no longer leave none (`SQS-A014`); concurrent setups with the installation
  code create one administrator, and a failed one keeps the code. Two requests to revert or
  recreate the same library queue one job.
- API: a request body must be exactly one JSON object; an empty body or anything after the
  object is refused with `SQS-C061` (an empty request to maintenance no longer means "off").
- Interface: tables shown as cards on narrow screens keep their headers and table roles for
  screen readers.
- Tests and CI: fuzz targets for CIDRs, cron, retention, display names and encryption, run for
  10 seconds in CI and 5 minutes every night (`make fuzz`, `fuzz.yml`); request contract tests
  for the API; `docs/16` now says which test levels are gates and which are planned.
- Documentation: the package map of `docs/21`, the status in the README and `AGENTS.md`, and the
  API conventions that are not implemented yet (OpenAPI contract, `Idempotency-Key`, `ETag`).

Upgrade: with the `1.2` tag, `docker compose pull` and `docker compose up -d` (TrueNAS: Update).
One migration (`0007`, the audit column of jobs). API clients that sent an empty body to an
endpoint that reads JSON must send `{}` or the fields it expects. If `sqsolve-init` now stops
with `SQS-C085`, the volume refuses permission changes: on TrueNAS, use a dataset with the
Generic preset for the app and db volumes.

Rollback: image `1.2.0` with the SQLite copy `app/state-backups/sqsolve-before-0007-*.db`
(`docs/runbooks/upgrade.md`); 1.2.0 refuses the migrated catalog (`SQS-X012`). Changes made since
the upgrade are lost with the copy; "Rebuild the catalog" brings back the backups taken since.

## [1.2.0] 2026-10-21

Fixes from the audit of 2026-10-20 (stability and long-term operation).

- Data safety:
  - A failed or repeated restore as new, recreate or library creation no longer drops a
    database it did not create (a double click on Recreate could drop the recreated library).
    Every restore, revert, delete, recover and purge job checks again, when it runs, that the
    library is still in the state it was queued for.
  - Retry is offered only for jobs that are safe to repeat (backups, verification, export,
    import, system maintenance jobs), and Retry and Cancel need the permission of the job's type.
    Restores, deletes, purges and reverts are started again from their own form.
  - A verification that could not run (no space, cancelled, server trouble, sqsolve stopping)
    no longer marks a good backup as failed; only a real integrity verdict does.
  - An import reads the whole archive before cataloguing it, waits until the file stops
    growing, and never deletes the user's file if it changed during the import.
  - Before a previous version (`__prev`) is dropped, sqsolve takes a verified backup of it.
    A library whose database comes back after being missing returns to Active only when that
    database has tables (or the library never had any); one closed to connections is opened only
    for that check. An empty, invalid or unreadable database with its name is kept, the library
    stays missing and a warning says why. An
    interrupted recreate is cleaned up instead of being adopted empty, and a recreate whose
    cleanup could not drop the database it created says so in the job error. A library left
    "restoring" by a cut goes back to the state it had (maintenance stays maintenance), or to
    missing when its database is gone.
  - A backup cut by a cancel or by a graceful stop (for example an update during the nightly
    backup) ends as interrupted and no longer sends an Error notification.
  - Revoking a member credential reports an error when the revocation cannot be recorded
    (`SQS-L080`), instead of the member getting its login back at the next start.
  - Maintenance now sets the library roles `NOLOGIN` and keeps the database open to sqsolve,
    so backups, restores and deletes keep working in maintenance (ADR 0024).
  - The per-library connection limit applies to the library roles, so a busy library can still
    be backed up. `sqsolve_admin` has 3 reserved connection slots.
  - Trash and previous-version retention are capped at 3650 days (a larger value overflowed
    and purged at once).
- Reconciliation: an interrupted restore, revert, delete or recreate that the start after a cut
  cannot settle is tried again at every start until it is settled, and the job purge keeps it
  meanwhile. Until then its library keeps its state and access, nothing its journal names is
  dropped (`SQS-R090` for a previous version) and a `library.reconcile_pending` warning says
  why; a journal tried again never undoes what was done with the library since. A database
  checked for adoption always ends closed when it is not adopted, and a reference to a previous
  version whose database is gone is forgotten.
- Volumes and startup (ADR 0023): the db and app volumes now carry a marker like the backups
  volume. `init` refuses an empty or foreign db or app volume, refuses swapped mounts, and never
  marks an empty backups folder on an installation that already has an identity. A problem with
  the backups disk no longer stops `init` (and with it PostgreSQL); sqsolve refuses to write
  backups there and raises a critical alert. sqsolve stores the PostgreSQL cluster identifier
  and holds if it changes. New variables: `SQSOLVE_DB_ADOPT`, `SQSOLVE_APP_ADOPT`,
  `SQSOLVE_PG_ACCEPT_SYSTEM_ID`; `SQSOLVE_BACKUPS_ADOPT` accepts `true` like the others.
- Jobs and alerts: a failed database write no longer crashes the process or leaves a job
  "running" forever that stopped its schedule; background loops recover from panics. Jobs cut
  by a graceful stop end as interrupted. If PostgreSQL never comes up after a start, a critical
  `postgres.down` is sent after 5 minutes. Notification grouping always delivers an escalation
  and sends a summary when the window ends; webhook errors no longer store the URL. Locks of
  two accounts, failed-login bursts from two addresses and the expiry of the panel and the
  PostgreSQL certificates are no longer merged into one notification, and a second
  `postgres.down` after `postgres.up` is delivered at once.
- Health: `GET /api/v1/system/health` reports backup freshness and volume use and answers 503
  when one is critical, for uptime monitors; it uses the same staleness rule as the
  `backup.stale` alert. A volume that cannot be measured raises a critical
  `storage.volume_missing`. Health warns when the PostgreSQL server is older than the bundled
  `pg_dump`.
- Housekeeping removes the catalog rows of backups deleted more than 365 days ago (the audit log
  keeps the history).
- Panel certificate: the self-generated certificate is renewed 30 days before it expires.
- Security (ADR 0025): the login lock lasts 15 minutes and no longer closes open sessions or
  disables API tokens; password hashing is limited to 4 at a time; the open redirect through
  `next=` is closed; the second factor is enforced for administrators when the setting is on;
  common passwords of 8 to 11 characters are refused; installing the panel certificate from
  Settings works again.
- PostgreSQL: listing databases no longer fails while one is being dropped; a failed
  `pg_hba.conf` reload is retried instead of being reported as applied; password rotation and
  role creation no longer leave PostgreSQL and the panel disagreeing; admin queries have lock
  and statement timeouts; the export closes its snapshot before the download.
- Build and deploy: the compose file rotates container logs (10 MB, 5 files) and runs the
  sqsolve container with `init: true`; Dependabot proposes new digests for the base images and
  actions; images are published only after CI passes, and the GitHub release only after the
  image; staticcheck runs on the shipped Go version and govulncheck scans the image binary.
  Duplicate error codes were renumbered: `SQS-B090`, `SQS-B091` and `SQS-C100` replace the
  second meanings of `SQS-B033`, `SQS-B034` and `SQS-C010`, and `make check` now fails on a
  code with two messages. API problems point at `/docs/errors/<code>`.
- All remaining Spanish strings in the interface and logs are now in English.
- Security: the image is built with Go 1.27.3, which fixes GO-2026-6809 (`crypto/x509`
  certificate verification, used by sqsolve's HTTPS and SMTP clients). The 1.1.2 image was built
  with Go 1.27.2. The new CI scan of the binary inside the image found it.

Upgrade: this is a new minor version, so the `1.1` tag does not move to it: change the
`x-sqsolve` line of your compose file to `ghcr.io/donutinit/sqsolve:1.2`, then
`docker compose pull` and `docker compose up -d` (TrueNAS: edit the app's YAML and Update).
Recommended, from the new `deploy/compose.yaml`: the `x-logging` block with `logging: *logging`
on the three services, and `init: true` on `sqsolve`. Copy those lines into your file, or paste
the file and set your three paths, `TZ`, UID and GID and the IP where 5432 is published again,
keeping the `1.2` tag (the compose file attached to the GitHub release pins the exact version).
Changing the `postgres` service restarts PostgreSQL: do it when no workstation has a project
open. The PostgreSQL image is the same as in 1.1.2 (`postgres:18.6-trixie`). Two migrations
(`0005`, an index on the backups catalog; `0006`, the reconciliation flag of interrupted jobs).
At the first start
`init` writes the db and app markers by itself. The 3 reserved connections take effect the next
time PostgreSQL restarts (`docs/07` §10). An uptime monitor on `GET /api/v1/system/health` now
also gets 503 for a stale library or a volume at 90 %.

Rollback: first reopen every library in maintenance (1.1.2 does not give the library roles
`LOGIN` back when maintenance ends). Then use image `1.1.2` with the SQLite copy
`app/state-backups/sqsolve-before-0005-*.db` (`docs/runbooks/upgrade.md`); 1.1.2 refuses the
migrated catalog (`SQS-X012`). Changes made since the upgrade are lost with the copy; "Rebuild
the catalog" brings back the backups taken since. Under 1.1.2, a library that the restored
catalog shows in maintenance accepts connections: reopen it and put it back in maintenance.

## [1.1.2] 2026-10-20

- The export catalog shows Projects and Stills as separate views, each with a search box, and
  every project links to its own stills: a library with hundreds of projects no longer needs a
  long scroll to download a `.drx`.

Upgrade: same as 1.1.0. No migrations. Rollback: image `1.1.1`.

## [1.1.1] 2026-10-20

- Fix: the role name in the panel sidebar (Administrator, Operator, Viewer) and the notes of a
  library whose creation failed were still in Spanish.
- README with screenshots of the panel.

Upgrade: same as 1.1.0. No migrations. Rollback: image `1.1.0`.

## [1.1.0] 2026-10-20

- The whole project moves to English, the only supported language: web panel, URLs, CLI, error
  messages, documentation and brand assets. Panel URLs changed (for example `/bibliotecas` is now
  `/libraries`); update your bookmarks. API paths under `/api/v1` are unchanged.
- New concise README; the full manual lives in `docs/manual/user-guide.md`.
- The example paths in `deploy/compose.yaml` are now `/path/to/sqsolve/...`.

Upgrade: put the `1.1` tag in `x-sqsolve` (`docker compose pull` and `up -d`, or on TrueNAS edit
the app's YAML and Update). No catalog migrations compared with 1.0.0. Rollback: image `1.0.0`,
with the old URLs.

## [1.0.0] 2026-10-19

- First stable version. Scope and evidence in `docs/acceptance/1.0.md` and `docs/poc`.
- The "Empty database" strategy (E0) is marked as not compatible with Resolve 21.1: on connect,
  Resolve declares it incompatible instead of initializing it (PoC-01).
- The README says what is tested and what is not.
- A library in "No database on the server" (after recovering an installation) can now be deleted
  and purged; before, it could only be recreated (`SQS-L041`).

Upgrade: same as 0.1.x, with the `1.0` tag in `x-sqsolve` (`docker compose pull` and `up -d`, or
Update in TrueNAS). No migrations compared with 0.1.6. Rollback: image `0.1.6`.

## [0.1.6] 2026-10-19

- Image signing with keyless cosign and publication of each version on GitHub with the compose
  file and these notes.
- `SECURITY.md`, `CONTRIBUTING.md`, runbooks and `docs/manual/errors.md`.
- `Strict-Transport-Security` (with HTTPS) and `Cross-Origin-Resource-Policy` headers in the
  panel, after the ZAP review.
- PoC-03, 06, 08 and 09 reports in `docs/poc`; ADR 0019 accepted as experimental.

Upgrade: same as 0.1.5. No migrations.

## [0.1.5] 2026-10-19

- Security proportionate to LAN and VPN (ADR 0021): no reauthentication, 8-character passwords,
  30-day sessions, mandatory HTTPS, lock after ten wrong passwords that is lifted with
  `sqsolve admin unlock`.
- Activity-based backups (ADR 0022): check every 15 minutes and back up only if the library
  changed; a current backup does not count as overdue. Default mode in new installations.
- Export `.drp` and `.drx` from any backup, without restoring the library.
- "TLS only" mode for Resolve connections, with the "Encrypted" column in the sessions.
- Catalog migrations 0002 to 0004; they apply on their own at startup.

Upgrade: with the `0.1` tag, `docker compose pull` and `docker compose up -d`, or the Update
button in TrueNAS. Rollback: image `0.1.4` starts with the migrated catalog (the new columns are
ignored).

## [0.1.4] 2026-10-19

- "Node" visual identity (ADR 0020): logo and icons in the panel, favicon, new README.

## [0.1.3] 2026-10-19

- Fix: the Export tab failed with `SQS-X998` on libraries with stills.

## [0.1.2] 2026-10-19

- Experimental export of projects (`.drp`) and grades (`.drx`) (ADR 0019), off by default.

## [0.1.1] 2026-10-19

- PostgreSQL runs as user 999 and `sqsolve-init` prepares its data folder; detection of volumes
  with NFSv4 ACLs (`SQS-C054`).
- The reference compose file uses the `0.1` tag.

## [0.1.0] 2026-10-18

- First version: libraries, access, verified backups, restore, panel, API, notifications,
  recovery kit.
