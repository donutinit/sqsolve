# Architecture decision records

Format: context, decision, alternatives, consequences. Statuses: **Proposed**, **Accepted**,
**Accepted, pending PoC** (the decision stands unless the named PoC refutes it),
**Superseded by NNNN**.

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-license.md) | AGPL-3.0-only for the code, CC BY-SA 4.0 for the documentation | Accepted |
| [0002](0002-topology.md) | Two containers (official PostgreSQL and sqsolve) plus an init one, with Docker Compose | Accepted |
| [0003](0003-go.md) | Backend in Go, single binary | Accepted |
| [0004](0004-server-rendered-ui.md) | Server-rendered UI with standard forms, no Node and no third-party JavaScript | Accepted (updated) |
| [0005](0005-sqlite.md) | Control plane state in SQLite | Accepted |
| [0006](0006-postgresql-18.md) | Official PostgreSQL 18 with `builtin` collation | Accepted, pending PoC-01 and PoC-02 |
| [0007](0007-black-box.md) | Resolve libraries are a black box | Accepted; narrowed by 0019 |
| [0008](0008-roles.md) | Control role without superuser and one owner role per library | Accepted, pending PoC-01 |
| [0009](0009-logical-backups.md) | Logical backups per library with custom-format `pg_dump` and manifest | Accepted, pending PoC-04 |
| [0010](0010-restore-by-swap.md) | Always restore into a new database and swap by rename | Accepted, pending PoC-09 |
| [0011](0011-hooks.md) | No executable hooks in 1.0; webhooks; hooks in an isolated container in 1.1 | Accepted |
| [0012](0012-port-50059.md) | The server does not publish port 50059 | Accepted, pending PoC-07 |
| [0013](0013-resolvedbkey.md) | `.resolvedbkey` as an experimental module | Accepted, pending PoC-05 |
| [0014](0014-secrets.md) | Secrets in files and encryption of recoverable credentials | Accepted (location per 0018) |
| [0015](0015-remote-access.md) | Remote access only over VPN; 5432 never to the Internet | Accepted |
| [0016](0016-base-image.md) | sqsolve image based on the official PostgreSQL image | Accepted |
| [0017](0017-postgresql-major-version.md) | PostgreSQL major versions by logical migration to a new cluster | Accepted |
| [0018](0018-three-volumes.md) | Three visible volumes: database, application and backups | Accepted (complemented by 0023) |
| [0019](0019-experimental-export.md) | Experimental export of projects (`.drp`) and grades (`.drx`) reading a fixed set of tables | Accepted (experimental in 1.0) |
| [0020](0020-visual-identity.md) | "Node" visual identity: symbol, outlined wordmark, brand palette only in the ports, icons | Accepted |
| [0021](0021-proportionate-security.md) | Security proportionate to LAN and VPN: no reauthentication, 8-character passwords, long sessions, mandatory HTTPS, account lock with CLI unlock | Accepted (account lock amended by 0025) |
| [0022](0022-activity-based-backups.md) | Activity-based backups: check every N minutes and back up only if the library changed; a current backup is not overdue | Accepted |
| [0023](0023-volume-identity.md) | Every volume carries a marker with the installation and its role; init refuses an empty or foreign db or app volume and never adopts an unmarked backups folder; backups problems do not stop init; sqsolve holds when the PostgreSQL cluster identifier changes | Accepted |
| [0024](0024-maintenance-blocks-roles.md) | Maintenance sets the library roles `NOLOGIN` and leaves the database open to `sqsolve_admin`, so backups, restores and deletes go on in maintenance | Accepted |
| [0025](0025-temporary-login-lockout.md) | The login lockout lasts 15 minutes, spares open sessions and API tokens, and password hashing runs in at most 4 slots | Accepted |
| [0026](0026-audit-before-action.md) | The audit entry of a sensitive action is written with it (same SQLite transaction) or before it; without the entry the action is not done | Accepted |
