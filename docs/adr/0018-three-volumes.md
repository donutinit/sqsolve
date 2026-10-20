# ADR 0018. Three visible volumes

**Status:** Accepted (2026-10-18). Supersedes the volume layout of `12-deployment.md` (six
volumes) and the location of secrets in ADR 0014.

**Context.** Diego asked that the installation not depend on any NAS and asked whether it was
worth separating database, application and backups. Six volumes (pgdata, pgconf, pgsocket,
state, secrets, backups) were too many to map by hand in an "Install via YAML".

**Decision.** The administrator maps three paths, each with a different storage profile:

| Volume | Path in containers | Contains | Storage |
|---|---|---|---|
| db | `/var/lib/postgresql` | Cluster (`18/docker`) and the configuration sqsolve generates (`sqsolve/`: `postgresql.conf`, `pg_hba.conf`, TLS certificate, superuser password) | SSD |
| app | `/var/lib/sqsolve` | Control plane SQLite, sqsolve secrets (`secrets/`), panel certificate, copies taken before migrations | SSD |
| backups | `/backups` | Backups, manifests, control plane copies (`_system`), imports | Another disk or pool |

The PostgreSQL socket uses an internal named volume that stores no data.

**Why this separation.** Database and backups must live on different disks: losing one disk
cannot take both. The application goes apart because its content (secrets and catalog) must not
be copied together with the backups that leave the building. The superuser password lives with
the cluster it protects: whoever has that volume already has the data.

**Alternatives.** A single volume (simple, but mixes data with backups); the original six
(precise, but confusing to map).

**Consequences.** The `sqsolve/` directory of the db volume is setgid and owned by the
PostgreSQL group, so the files sqsolve rewrites stay readable by the server. The recovery kit is
still needed to recover passwords on another server.
