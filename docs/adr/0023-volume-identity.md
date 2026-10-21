# ADR 0023. Volume identity and what init may refuse

**Status:** Accepted (2026-10-20). Complements ADR 0018 (three volumes).

**Context.** The audit of 2026-10-20 found that sqsolve could not tell its volumes from an empty
folder:

- **H3** (reproduced): with `instance_id` present and the backups folder empty and without a
  marker, `sqsolve-init` wrote a new marker. Docker creates a missing bind-mount path as an
  empty folder (short syntax in `deploy/compose.yaml`), so a backups disk that was not mounted
  became the backups volume and the backups went to the system disk.
- **H4**: the `db` and `app` volumes had no identity at all. An unmounted `db` disk is an empty
  folder; PostgreSQL then initializes a new, empty cluster, sqsolve reconciles against it, marks
  every library "No database on the server" and rewrites `pg_hba.conf`. An unmounted `app` disk
  made init generate new secrets and a new control plane.
- **M6**: every backups problem (`SQS-S003`, `SQS-S004`, an unreadable marker) was fatal for
  init, and `postgres` waits for init (`service_completed_successfully`): a problem with the
  backups disk took every library offline.

**Decision.**

1. Each of the three volumes carries a marker `.sqsolve-volume` at its root, in the existing
   `fsx.Marker` format (`instance_id`, `volume`, `created_at`): `backups`, `db`
   (`/var/lib/postgresql/.sqsolve-volume`) and `app` (`/var/lib/sqsolve/.sqsolve-volume`).
   The markers belong to the sqsolve user with mode 0600. A marker only identifies the volume it
   names: both the installation and the `volume` field must match the path it is found on.
2. Init decides everything before writing anything. The identity comes from the app volume
   (`instance_id` or its marker); without it, from the backups marker (recovery), then from the
   db marker; with no identity anywhere it is a new installation.

   | Situation | Init |
   |---|---|
   | New installation (no identity anywhere) | Marks the three volumes |
   | Backups marker of this installation | Nothing to do |
   | Backups folder empty and unmarked, installation with an identity | Does not mark it; warns `SQS-S014`; continues |
   | Backups with files and no marker | Does not mark it; warns `SQS-S004`; continues |
   | Backups of another installation (`SQS-S003`) or unreadable marker (`SQS-S002`) | Warns; continues |
   | `SQSOLVE_BACKUPS_ADOPT=true` and an unmarked backups folder | Marks it |
   | db volume without marker but with a cluster (`PG_VERSION`) | Marks it (upgrade of installations from before this ADR) |
   | db volume empty and unmarked, and the app volume already has `sqsolve.db` | Refuses: `SQS-S012` |
   | db marker of another installation | Refuses: `SQS-S011` |
   | A marker of this installation for another volume on the db or app path (host paths swapped) | Refuses: `SQS-S019`; no adopt variable accepts it |
   | A marker of this installation for another volume on the backups path | Warns `SQS-S019`; continues, writes nothing there; `serve` refuses backups (the backups check compares the volume too) |
   | No identity in app, and the only marker is on the wrong path | Refuses: `SQS-S019` (the identity is only taken from a marker on its own path) |
   | db marker unreadable | Refuses: `SQS-S015` |
   | app volume empty while db and backups belong to an installation | Refuses: `SQS-S013` |
   | app marker against `instance_id` (`SQS-S010`) or unreadable (`SQS-S017`) | Refuses |
   | No identity anywhere and the backups marker cannot be read | Refuses: `SQS-S016` |
   | New server, empty `db` and `app`, existing backups volume (recovery runbook) | Adopts the identity of the backups volume and marks `db` and `app` |

   A refusal stops init with a coded error, so PostgreSQL does not start on that volume. Once
   the installation has an identity, a backups problem never stops init (without one, an
   unreadable backups marker stops it with `SQS-S016` and a marker on the wrong path with
   `SQS-S019`): `serve` keeps refusing to write backups without a valid marker (`SQS-S001`,
   `SQS-S003`) and emits the critical event `storage.volume_missing`, so the libraries stay
   online while the backups disk is fixed. Init never writes into a backups folder it cannot
   identify (no marker, no `libraries/`, `_system/` or `_import/`).
3. Adopt variables, booleans like the others (`true`, `1`, `yes`, `on`):
   `SQSOLVE_BACKUPS_ADOPT`, `SQSOLVE_DB_ADOPT`, `SQSOLVE_APP_ADOPT`. Each one only lets init mark
   or accept the volume it names. Init prints a warning on every run while one is set, because
   a variable left in the compose file would adopt the next unmounted disk too.
4. PostgreSQL cluster identity. At the first successful connection the control plane stores
   `SELECT system_identifier FROM pg_control_system()` (setting `pg_system_identifier`; a catalog
   function, not a Resolve table, ADR 0007). When a later start finds a different identifier,
   sqsolve does not mark interrupted jobs, does not reconcile, does not regenerate
   `pg_hba.conf`, starts no dispatcher nor scheduler, emits the critical event
   `postgres.cluster_changed` (`SQS-S020`), shows it on the dashboard, the Health page,
   `sqsolve doctor` and `GET /api/v1/system/health` (`postgres_cluster: changed`), and keeps
   `/readyz` at 503. It waits for an explicit acceptance:
   `SQSOLVE_PG_ACCEPT_SYSTEM_ID=<new identifier>` on the `sqsolve` service and a restart. The
   value only matches that exact cluster, so leaving it set accepts no other one. The
   acceptance is recorded in the audit log (`postgres.cluster_accepted`). Applying a staged
   recovery forgets the stored identifier, because the recovery runbook creates a new cluster by
   design. If the cluster's identifier or the stored one cannot be read, the start goes on as
   before and is logged, and nothing is stored: a failed read never pins the current cluster.

**Alternatives.** A console subcommand to accept the new cluster: it would have to reach the
running server, which already holds while it waits; a variable checked at start is simpler and
cannot accept another cluster by accident. Making backups problems fatal (as before): it takes
every library offline for a disk that only holds copies. Comparing directory contents instead
of markers: an empty folder and a lost disk look the same.

**Consequences for operators.**

- Upgrading needs no manual step: the first `sqsolve-init` of this version marks the `db` and
  `app` volumes of an existing installation silently, and the first connection stores the
  cluster identifier.
- An unmounted backups disk no longer stops the installation nor receives backups: the panel and
  the notifications show `storage.volume_missing` (critical); mount the disk and restart, or set
  `SQSOLVE_BACKUPS_ADOPT=true` once for a new, empty disk.
- An unmounted `db` or `app` disk stops `sqsolve-init` with `SQS-S012` or `SQS-S013` before
  PostgreSQL creates an empty cluster or sqsolve new secrets. Mount the disk and `docker compose
  up -d`.
- Swapped host paths (for example db and backups exchanged in the three path fields of TrueNAS)
  stop `sqsolve-init` with `SQS-S019`; fix the paths in the compose file.
- Replacing the database volume on purpose (a PostgreSQL major migration, ADR 0017, or a cluster
  restored from elsewhere) needs `SQSOLVE_PG_ACCEPT_SYSTEM_ID` once; the value is in the event,
  the Health page and `sqsolve doctor`.
- The recovery runbook keeps working unchanged; on a server with another UID, add
  `SQSOLVE_FIX_PERMS=true` for that first start.
