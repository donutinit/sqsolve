# 12. Deployment, persistence and configuration

## 1. Model

A single `compose.yaml` file is the only official way to install. It works the same on:

- any Linux with Docker Engine 24+ and Compose v2 (`docker compose up -d`);
- NAS devices or panels that accept Compose: TrueNAS ("Apps > Discover > Custom App > Install
  via YAML"), Portainer (Stacks), Unraid (Compose Manager plugin), Synology Container Manager
  (Projects), and similar.

There are no templates or integrations specific to any NAS. The differences between platforms
(volume paths, the IP where 5432 is published) are resolved by editing two or three lines of
the YAML.

## 2. Volumes

Three visible paths (ADR 0018):

| Volume | Path in containers | Contents | Recommendation |
|---|---|---|---|
| db | `/var/lib/postgresql` | PostgreSQL cluster (`18/docker`) and the configuration sqsolve generates (`sqsolve/`) | SSD; file system with snapshots if available |
| app | `/var/lib/sqsolve` | SQLite, sqsolve secrets, panel certificate | SSD; not copied together with the offsite backups |
| backups | `/backups` | Backups, manifests, `_system`, `_import` | **Another disk or pool** |

Plus a volume with an internal name for the PostgreSQL socket, with no data.

Suggestion for ZFS (not mandatory): db with `recordsize=16K` and `compression=lz4`; backups with
`recordsize=1M`. A reservation on the db dataset prevents a disk filled by something else from
stopping PostgreSQL.

## 3. Reference compose

The current file is [`deploy/compose.yaml`](../deploy/compose.yaml) and CI brings it up on every
change (`smoke` job). Services:

- `sqsolve-init`: as root and without network, prepares volumes, secrets, `postgresql.conf`, the
  initial `pg_hba.conf` and the PostgreSQL TLS certificate. It runs on every `up` and is
  idempotent.
- `postgres`: official image pinned by digest, started with
  `postgres -c config_file=/var/lib/postgresql/sqsolve/postgresql.conf` and with the image's
  user 999 (`user: "999:999"`); it never runs as root, just like in TrueNAS apps.
- `sqsolve`: unprivileged user, read-only file system, no capabilities; publishes 9432. It
  starts even if PostgreSQL is not ready, so it can be diagnosed.

To install, change the three paths, `TZ` and, if needed, the IP where 5432 is published.

## 4. Environment variables

| Variable | Default | Use |
|---|---|---|
| `TZ` | `UTC` | Container time zone and initial value of the policies |
| `SQSOLVE_UID`, `SQSOLVE_GID` | `1000` | Owner of the sqsolve volumes (applied by `sqsolve-init`); must match `user:` |
| `SQSOLVE_LISTEN` | `:9432` | Address of the HTTPS panel |
| `SQSOLVE_HTTP_REDIRECT` | empty | If set (`:9080`), listens on HTTP only to redirect to HTTPS |
| `SQSOLVE_ADVERTISED_HOST` | empty | Address workstations will see; if empty, it is set in the wizard |
| `SQSOLVE_APP_DIR` | `/var/lib/sqsolve` | App volume |
| `SQSOLVE_BACKUP_DIR` | `/backups` | Backups volume |
| `SQSOLVE_PG_DIR` | `/var/lib/postgresql` | db volume; the configuration lives in `sqsolve/` inside it |
| `SQSOLVE_PG_DATA_DIR` | `/var/lib/postgresql/18/docker` | PGDATA of the official 18 image |
| `SQSOLVE_PG_SOCKET_DIR` | `/var/run/postgresql` | Socket |
| `SQSOLVE_PG_PORT` | `5432` | PostgreSQL port |
| `SQSOLVE_PG_LISTEN` | `*` | PostgreSQL `listen_addresses` |
| `SQSOLVE_PG_UID`, `SQSOLVE_PG_GID` | `999` | PostgreSQL user in the official image |
| `SQSOLVE_BACKUPS_ADOPT` | empty | `1` allows marking a backups volume that already has files |
| `SQSOLVE_FIX_PERMS` | empty | `1` fixes owners recursively in `init` |
| `SQSOLVE_INSECURE_HTTP` | empty | Development and tests only (CI): panel over HTTP. In production the panel is always HTTPS (ADR 0021). Encryption of Resolve connections is not configured here: PostgreSQL always offers TLS and the "TLS only" mode is turned on in Settings |
| `SQSOLVE_PG_MEMORY_MB` | `2048` | Memory assumed for PostgreSQL when computing `shared_buffers` |
| `SQSOLVE_PG_MAX_CONNECTIONS` | `200` | Written to `postgresql.conf` (requires restarting PostgreSQL) |
| `SQSOLVE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SQSOLVE_LOG_FORMAT` | `json` | `json` or `text` |
| `SQSOLVE_TRUSTED_PROXIES` | empty | CIDRs of trusted reverse proxies |
| `SQSOLVE_WORKERS` | `2` | Job workers |
| `SQSOLVE_HEAVY_JOBS` | `1` | Simultaneous heavy jobs |

## 5. First start

1. `sqsolve-init` (as root, without network): creates the `backups` marker if it does not exist
   and the volume is empty (if there is content without a marker, it stops with an error so as
   not to adopt the wrong folder; if the volume already carries another installation's marker
   and app is empty, it adopts its identity for a recovery); generates missing secrets with
   `crypto/rand`; generates `postgresql.conf` and an initial `pg_hba.conf`; generates the
   PostgreSQL TLS certificate (self-signed, ECDSA P-256, 10 years, owned by root with group 999
   in mode 0640 as PostgreSQL requires); creates the PostgreSQL data folder owned by 999 with
   mode 0700, and stops with `SQS-C054` if the volume does not keep that mode (NFSv4-type ACL);
   adjusts owners and permissions; exits.
2. `postgres` initializes the cluster the first time with the superuser password.
3. `sqsolve` creates or repairs `sqsolve_admin` with the superuser over the socket, prints the
   **installation code** in its log if there is no administrator yet and opens the wizard.

The PostgreSQL certificate lasts 10 years because Resolve does not verify it (PoC-06): its job
is to encrypt, not to identify. The panel's lasts 2 years and sqsolve renews it by itself; the
administrator can upload their own.

## 6. Platforms

| Platform | Notes |
|---|---|
| Generic Linux | `docker compose up -d` in the folder of `compose.yaml` |
| TrueNAS | Install via YAML with the same file; change `/path/to/backups` to the path of a dataset (`/mnt/<pool>/...`) and, if desired, map the other volumes to datasets. TrueNAS uses ports 80 and 443 for its interface, not 9432 or 5432 |
| Portainer | Stack with the YAML |
| Unraid | Compose Manager plugin with the YAML |
| Synology | Publish 5432 on the LAN IP because DSM occupies 5432 on localhost [01 §13] |
| Podman | Compatible with `podman compose` in principle; documented as "community" until tested |
| ARM64 | Same multi-architecture images |

## 7. Network and names

- The server needs a fixed IP (DHCP reservation or static). The advertised address can be a
  local DNS name; PoC-08 confirms whether Resolve accepts names in the location.
- Host firewall: allow 5432 and 9432 from the LAN or VPN; nothing from the Internet.
- Workstations: allow TCP 50059 to 50314 inbound and outbound between them to collaborate
  (PoC-07).

## 8. Upgrades

### 8.1 sqsolve

1. Change the image tag (or `docker compose pull` if a minor version tag such as `1.1` is used).
2. `docker compose up -d`.
3. On startup: SQLite `VACUUM INTO` copy, migrations, reconciliation, event `system.upgraded`
   with the old and new version.

Going back: stop, restore the matching SQLite copy from `state/backups/` and use the previous
image. SQLite migrations never drop columns in a minor version, so going back to the previous
minor version works without restoring in most cases; the release notes say so.

### 8.2 PostgreSQL minor version

Each sqsolve version pins the digest of a recent PostgreSQL 18 minor version. Upgrading sqsolve
updates the reference YAML; the administrator applies the image change. Before restarting
PostgreSQL, the interface offers "back up everything now".

### 8.3 PostgreSQL major version (2.0)

By logical migration to a new cluster (ADR 0017): new `pgdata` volume, temporary container with
the new version, backup and restore of each library with L2 verification, switching the services
in the YAML, and the old volume intact until the administrator deletes it. Never `pg_upgrade`
over the data directory in use.

## 9. Installation backup and disaster recovery

What to keep outside the server: the `backups` volume (or its restic copy) and the recovery kit.

Recovering on a new server:

1. Install with the reference YAML pointing `backups` at the copy of the backups.
2. In the wizard choose "Recover existing installation" and paste the kit's master key.
3. sqsolve restores the control plane from `_system/` (users, policies, encrypted credentials)
   or, if there is none, rebuilds the catalog from the manifests.
4. "Restore all" creates each library from its most recent verified backup with the same slug
   and the original passwords (if the master key is available; otherwise it generates new ones).
5. Workstations reconnect with the new IP if it changed.

## 10. Uninstalling

`docker compose down` keeps all volumes. Deleting data is an explicit, separate step
(`docker volume rm ...` or deleting folders), documented with warnings.
