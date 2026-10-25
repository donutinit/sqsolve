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

Each of the three carries a marker `.sqsolve-volume` at its root with the installation it belongs
to; `sqsolve-init` refuses or leaves alone a folder it cannot identify (ADR 0023, §5).

Suggestion for ZFS (not mandatory): db with `recordsize=16K` and `compression=lz4`; backups with
`recordsize=1M`. A reservation on the db dataset prevents a disk filled by something else from
stopping PostgreSQL.

## 3. Reference compose

The current file is [`deploy/compose.yaml`](../deploy/compose.yaml) and CI brings it up on every
change (`smoke` job). Services:

- `sqsolve-init`: as root, without network, with a read-only root file system and only the
  capabilities `CHOWN`, `DAC_OVERRIDE`, `FOWNER` and `FSETID`, prepares volumes, secrets,
  `postgresql.conf`, the initial `pg_hba.conf` and the PostgreSQL TLS certificate. It runs on
  every `up` and is idempotent.
- `postgres`: official image pinned by digest, started with
  `postgres -c config_file=/var/lib/postgresql/sqsolve/postgresql.conf` and with the image's
  user 999 (`user: "999:999"`) and no capabilities; it never runs as root, just like in TrueNAS
  apps.
- `sqsolve`: unprivileged user, read-only file system, no capabilities; publishes 9432. It
  starts even if PostgreSQL is not ready, so it can be diagnosed.

To install, change the three paths (the `x-volumes` block, where each is written once), `TZ`
and, if needed, the IP where 5432 is published.

Decisions of the reference file that are not obvious:

- **The image tag is the major version (`1`).** Every `1.x.y` release moves it, so TrueNAS
  shows "Update available" for each release and `docker compose pull` gets it elsewhere; 2.0, a
  PostgreSQL major migration, never arrives through it (ADR 0027).
- **`sqsolve-init` keeps four capabilities.** The same three as the permissions container of
  TrueNAS apps (`CHOWN`, `DAC_OVERRIDE`, `FOWNER`) plus `FSETID`: init sets the setgid bit of
  `sqsolve/`, whose group is PostgreSQL's and not root's, so files created in it keep that
  group. Without `FSETID` the kernel clears the bit without an error. Its health check is off:
  the image's one asks the panel, which does not run in that container.
- **The PostgreSQL health check calls the binary** `/usr/lib/postgresql/18/bin/pg_isready`. The
  `pg_isready` on `PATH` is Debian's Perl wrapper, which would start every 10 seconds. The path
  changes with the major version, together with the rest of the file (ADR 0017).
- **Keys for TrueNAS.** `x-portals` gives the app a "Web UI" button to the panel on the NAS
  address and `x-notes` the notes on its page; the TrueNAS middleware reads both from the file
  (checked in the 25.10 source) and other platforms ignore `x-` keys.

- **Logs rotate.** Every service uses the `json-file` driver with `max-size: 10m` and
  `max-file: 5` (the `x-logging` block), so a service never keeps more than about 50 MB of
  container logs. Without it Docker keeps them forever. The history that matters (jobs, events,
  audit) lives in sqsolve's own catalog, not in these logs.
- **`init: true` on `sqsolve`.** A minimal init process runs as PID 1, reaps orphaned child
  processes (a `pg_dump` or `pg_restore` that outlives the job that started it) and passes the
  stop signal on to sqsolve. It works with the read-only root file system: Docker mounts the init
  binary from the host.
- **No resource limits on purpose.** There is no `mem_limit`: a cap makes the kernel kill
  PostgreSQL in the middle of a large restore, which is the worst moment; PostgreSQL's memory is
  sized by `SQSOLVE_PG_MEMORY_MB` instead. There is no `pids_limit` either: PostgreSQL starts one
  process per connection (up to `SQSOLVE_PG_MAX_CONNECTIONS`, 200 by default) plus background and
  parallel workers, and `pg_restore` runs parallel jobs, so no single value is safe for every
  studio. An operator who wants a cap should try it with the largest restore they expect.
- **The PostgreSQL line is literal and pinned by digest.** It is the operator's copy: a new
  sqsolve release does not change it (section 8.2). CI keeps it equal to the one in the
  `Dockerfile` and Dependabot proposes new minor versions weekly.

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
| `SQSOLVE_PG_BIN_DIR` | empty | Folder searched first for `pg_dump`, `pg_restore` and `vacuumdb`; empty uses `/usr/lib/postgresql/*/bin` and `PATH`. Only for installations outside the image |
| `SQSOLVE_BACKUPS_ADOPT` | empty | `true` lets `init` mark a backups folder without a marker: one that has files, or an empty one on an installation that already has an identity (a new disk). Remove it after that start (ADR 0023) |
| `SQSOLVE_DB_ADOPT` | empty | `true` lets `init` accept a db volume it would refuse (`SQS-S011`, `SQS-S012`, `SQS-S015`). Remove it after that start |
| `SQSOLVE_APP_ADOPT` | empty | `true` lets `init` start over an empty app volume while db and backups belong to an installation (`SQS-S013`): new secrets and control plane. Remove it after that start |
| `SQSOLVE_PG_ACCEPT_SYSTEM_ID` | empty | Accepts a PostgreSQL cluster whose system identifier changed (`SQS-S020`); its value is the new identifier shown by the event, the Health page and `sqsolve doctor`, and it accepts only that cluster |
| `SQSOLVE_FIX_PERMS` | empty | `true` fixes owners recursively in `init` |
| `SQSOLVE_INSECURE_HTTP` | empty | Development and tests only (CI): panel over HTTP. In production the panel is always HTTPS (ADR 0021). Encryption of Resolve connections is not configured here: PostgreSQL always offers TLS and the "TLS only" mode is turned on in Settings |
| `SQSOLVE_PG_MEMORY_MB` | `2048` | Memory assumed for PostgreSQL when computing `shared_buffers` |
| `SQSOLVE_PG_MAX_CONNECTIONS` | `200` | Written to `postgresql.conf` (requires restarting PostgreSQL) |
| `SQSOLVE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SQSOLVE_LOG_FORMAT` | `json` | `json` or `text` |
| `SQSOLVE_TRUSTED_PROXIES` | empty | CIDRs of trusted reverse proxies |
| `SQSOLVE_WORKERS` | `2` | Job workers |
| `SQSOLVE_HEAVY_JOBS` | `1` | Simultaneous heavy jobs |

## 5. First start

1. `sqsolve-init` (as root, without network): checks the identity of the three volumes and marks
   them (`.sqsolve-volume`, ADR 0023). On a new installation it marks all three. Once the
   installation has an identity it never marks an empty, unmarked backups folder (an unmounted
   disk looks like that): it warns `SQS-S014` and goes on, and `sqsolve` refuses to write
   backups there; a backups folder with foreign files (`SQS-S004`) or another installation's
   marker (`SQS-S003`) is also only a warning, so PostgreSQL still starts. It stops with a coded
   error before PostgreSQL could create an empty cluster or sqsolve new secrets: an empty `db`
   volume when the catalog already exists (`SQS-S012`), a `db` volume of another installation
   (`SQS-S011`), an empty `app` volume while `db` and `backups` belong to an installation
   (`SQS-S013`), or a volume of this installation on the path of another one, when the host paths
   are swapped (`SQS-S019`; on the backups path it is a warning). If `app` is empty and the
   backups volume carries a marker, it adopts that identity for a recovery. It generates missing
   secrets with `crypto/rand`; generates
   `postgresql.conf` and an initial `pg_hba.conf`; generates the PostgreSQL TLS certificate
   (self-signed, ECDSA P-256, 10 years; key and certificate written as a pair and generated
   again if one is missing or they do not match; the key belongs to the PostgreSQL user with
   mode 0600, the certificate is 0644 in a 0755 folder so sqsolve can show its expiry); creates
   the PostgreSQL data folder owned by 999 with mode 0700, and stops with `SQS-C054` if the
   volume does not keep that mode (NFSv4-type ACL); adjusts owners and permissions; exits.
2. `postgres` initializes the cluster the first time with the superuser password.
3. `sqsolve` creates or repairs `sqsolve_admin` with the superuser over the socket, stores the
   cluster's system identifier at the first connection (ADR 0023), prints the **installation
   code** in its log if there is no administrator yet and opens the wizard.

The PostgreSQL certificate lasts 10 years because Resolve does not verify it (PoC-06): its job
is to encrypt, not to identify. sqsolve generates the panel's for 2 years and renews it when
fewer than 30 days remain, at startup and in the daily certificate check, without a restart.
The administrator can upload their own; sqsolve never renews an uploaded certificate and warns
30 days before it expires, at Error severity (sent by the notification channels by default) in
the last 14 days. At startup, `LoadPanelCert` replaces a panel certificate and key that cannot be
loaded as a pair (unparseable, or a key that does not match) with a self-generated certificate,
and keeps the old files next to them as `.replaced-<time>` (`SQS-C083` in the log).

## 6. Platforms

| Platform | Notes |
|---|---|
| Generic Linux | `docker compose up -d` in the folder of `compose.yaml` |
| TrueNAS | Install via YAML with the same file; point the three paths of `x-volumes` to datasets (`/mnt/<pool>/...`, Generic preset), backups on another pool. Once a day TrueNAS compares the digest of each image tag with the registry and shows "Update available" when it moved; Update runs `docker compose pull` and recreates the app, `sqsolve-init` included. It stores the YAML as data: a later edit shows it without comments, with the anchors expanded and the keys sorted, so the sqsolve image appears three times. TrueNAS uses ports 80 and 443 for its interface, not 9432 or 5432 |
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

1. `docker compose pull` with the reference tag `1`, which every 1.x release moves (TrueNAS:
   the Update button). With an exact version (`1.2.1`), change the tag first.
2. `docker compose up -d`.
3. On startup: SQLite `VACUUM INTO` copy in `app/state-backups/`
   (`sqsolve-before-<migration>-<time>.db`; one per pending migration, so a migration that fails
   on every start does not fill the volume, and only the newest 5 are kept), migrations,
   reconciliation, event `system.upgraded` with the old and new version.

Going back: stop, restore the matching SQLite copy from `app/state-backups/` as `sqsolve.db`,
move `sqsolve.db-wal` and `sqsolve.db-shm` aside together with the replaced database, and use
the previous image (`docs/runbooks/upgrade.md`). A version does not start on a catalog with a
migration it does not know (`SQS-X012`), so going back after a migration always needs that copy;
the release notes say whether a version migrates.

### 8.2 PostgreSQL minor version

Each sqsolve release pins the digest of a recent PostgreSQL 18 minor version in
`deploy/compose.yaml` and the `Dockerfile` (`make pg-image-check`). An installed compose file
keeps its own digest, so updating sqsolve does not change PostgreSQL. When the release notes
announce a new minor version, the administrator replaces the `image:` line of the `postgres`
service (user guide, "Updating"). PostgreSQL restarts, so run "Back up now" on each library
first.

### 8.3 PostgreSQL major version (2.0)

By logical migration to a new cluster (ADR 0017): new `pgdata` volume, temporary container with
the new version, backup and restore of each library with L2 verification, switching the services
in the YAML, and the old volume intact until the administrator deletes it. Never `pg_upgrade`
over the data directory in use. The new cluster has another system identifier: sqsolve holds
until `SQSOLVE_PG_ACCEPT_SYSTEM_ID` accepts it (ADR 0023).

## 9. Installation backup and disaster recovery

What to keep outside the server: the `backups` volume (or its restic copy) and the recovery kit.

Recovering on a new server:

1. Install with the reference YAML pointing `backups` at the copy of the backups.
2. In the wizard choose "Recover existing installation" and paste the kit's master key.
3. sqsolve restores the control plane from `_system/` (users, policies, encrypted credentials)
   or, if there is none, rebuilds the catalog from the manifests. The restored database and key
   are staged and applied at the next start through a journal, so a power cut in the middle is
   finished by the following start, and the replaced database and key are kept aside as a pair
   (`docs/runbooks/recover-installation.md`).
4. "Restore all" creates each library from its most recent verified backup with the same slug
   and the original passwords (if the master key is available; otherwise it generates new ones).
5. Workstations reconnect with the new IP if it changed.

## 10. Uninstalling

`docker compose down` keeps all volumes. Deleting data is an explicit, separate step
(`docker volume rm ...` or deleting folders), documented with warnings.
