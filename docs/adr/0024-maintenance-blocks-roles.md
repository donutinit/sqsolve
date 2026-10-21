# ADR 0024. Maintenance blocks the library roles, not the database

**Status:** Accepted (2026-10-20).

**Context.** Maintenance mode was `ALTER DATABASE <slug> ALLOW_CONNECTIONS false`. That flag
blocks every role, `sqsolve_admin` included, so a library in maintenance could not be backed up,
restored by swap, reverted, deleted (its final backup) or exported, although `docs/07` §4
promised that backups go on in maintenance (audit finding H6, confirmed twice). A restore that
replaced a library in maintenance also left the new database accepting connections while the
catalog still said "maintenance" (the restored database is created open). The same audit found
that the temporary locks of delete and revert could leave a library shown as Active with its
roles `NOLOGIN` or its database closed after a failure or a cut (finding M1).

PoC-09 (2026-10-19) observed the behaviour workstations rely on: with maintenance on and without
"close sessions", an open Resolve session keeps saving; new connections are refused.

**Decision.**

- Maintenance keeps `datallowconn = true` and sets every library role (the owner and the members
  with an active credential) `NOLOGIN`. With "close sessions" checked it then ends the sessions
  of those roles only, never sqsolve's own (a backup running at that moment). `pg_hba.conf` does
  not change: the library keeps its lines.
- Leaving maintenance sets `LOGIN` again only for roles whose credential is active; a revoked
  member stays `NOLOGIN`. A person added while the library is in maintenance gets a `NOLOGIN`
  role and can connect when maintenance ends.
- One function applies the access each state promises and is used everywhere: maintenance
  (`NOLOGIN`, database open), active (`LOGIN`, database open, grants and the members' per-database
  role repeated), awaiting initialization (`LOGIN` and `CREATEDB` for the owner), initialization
  expired and missing (`LOGIN`). It is idempotent. It is the compensation of every lock that
  fails before its point of no return (delete, restore by swap, revert), it runs after every swap
  and recreate, and reconciliation runs it at every start for every library that is not in the
  trash or purged.
- Existing installations: at reconciliation, a library in `maintenance` whose database has
  `datallowconn = false` is converted (roles `NOLOGIN` first, then the database opened).
- In maintenance the database is opened only after every role is `NOLOGIN`. If one cannot be
  changed, the connections of the database are left as they were (closed for a legacy
  maintenance, open for an active library) and the error is `SQS-L054`: putting a library in
  maintenance then fails and the library stays active; reconciliation reports it.
- The temporary locks of delete, restore by swap and revert keep `ALLOW_CONNECTIONS false` on the
  database being renamed: nobody, sqsolve included, needs to connect to it before the rename. The
  previous-state databases (`__prev_*`) stay closed too; sqsolve opens one only for the moment of
  backing it up (only `sqsolve_admin` reaches it, through the local socket, because
  `pg_hba.conf` has no line for those names) and closes it again.

**Alternatives.** Keep `ALLOW_CONNECTIONS false` and make every job that needs the database open
it and close it again: a crash in between leaves maintenance open to workstations, and an open
session of a workstation would see the database reopen. Revoke `CONNECT` from the library roles:
it is a privilege of the database, so it stays behind with the old database after a swap, and
members would need it revoked one by one as well.

**Consequences.**

- For operators: in maintenance, backups (scheduled and manual), restore by replacement, revert,
  delete, export and the weekly verification work. Workstations cannot open new connections:
  PostgreSQL now answers that the role "is not permitted to log in" instead of "the database is
  not accepting connections" (what Resolve shows for it: To be confirmed). Open sessions keep
  working unless "close sessions" is checked, as before.
- After a restart, reconciliation reopens any library left locked by a cut (roles `LOGIN`,
  database open) and logs it; libraries in maintenance are converted once and the log says so.
- Turning maintenance on and off reapplies the whole access of the library (grants, `LOGIN`, the
  members' role): it is also the panel's way to repair a library whose reopening after a swap
  failed (`SQS-R054`).
- Documented in `docs/07` §4 and §8, `docs/08` §5, `docs/10` §4, `docs/11` §7, the user guide and the
  runbook `docs/runbooks/restore-and-revert.md`.
