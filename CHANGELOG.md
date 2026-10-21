# Changelog

Format: one section per version, newest on top. The notes of each version on GitHub are taken
from here (`.github/workflows/release.yml`).

## [Unreleased]

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

Upgrade: with the `1.1` tag, `docker compose pull` and `docker compose up -d` (TrueNAS:
Update). Recommended, from the new `deploy/compose.yaml`: the `x-logging` block with
`logging: *logging` on the three services, and `init: true` on `sqsolve`. Copy those lines into
your file, or paste the file and set your three paths, `TZ`, UID and GID and the IP where 5432 is
published again, keeping the `1.1` tag (the compose file attached to the GitHub release pins the
exact version). Changing the `postgres` service restarts PostgreSQL: do it when no workstation
has a project open. One migration (`0005`, an index on the backups catalog). At the first start
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
