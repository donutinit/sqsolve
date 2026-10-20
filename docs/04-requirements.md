# 04. Requirements

Priority: **M** mandatory for 1.0, **S** desirable in 1.x, **C** later. Each requirement is
traced to tasks in `21-tasks.md` and to tests in `16-testing.md`.

## 1. Functional requirements

### 1.1 Installation and initial setup (RF-INS)

| ID | Requirement | Priority |
|---|---|---|
| RF-INS-01 | Installation with a single Docker Compose file that brings up PostgreSQL and sqsolve | M |
| RF-INS-02 | On first start all secrets are generated (superuser, control role, master key, TLS certificates) without intervention | M |
| RF-INS-03 | Setup wizard: language, time zone, administrator account, address advertised to workstations, default backup policy, recovery kit download | M |
| RF-INS-04 | The wizard is only reachable while no administrator exists and requires the installation code printed in the container log | M |
| RF-INS-05 | If a mandatory volume is not mounted or lacks permissions, sqsolve refuses to start and explains what is missing | M |

### 1.2 Libraries (RF-BIB)

| ID | Requirement | Priority |
|---|---|---|
| RF-BIB-01 | Create a library with display name, identifier (slug) and notes | M |
| RF-BIB-02 | Create a library from a local template (copy of a library freshly initialized by Resolve) | S |
| RF-BIB-03 | List libraries with state, size, active connections, last backup and detected version | M |
| RF-BIB-04 | View a library's details: connection, backups, schedule, access, activity, settings | M |
| RF-BIB-05 | Edit display name, notes, connection limit and declared Resolve version | M |
| RF-BIB-06 | Maintenance mode: block new connections and, optionally, close the active ones | M |
| RF-BIB-07 | Delete: verified final backup, lock, rename to the trash, purge after N days (30 by default); restorable from the trash while it exists | M |
| RF-BIB-08 | View active sessions per library (user, IP, application, start) and terminate a session | M |
| RF-BIB-09 | Compute and watch the schema fingerprint; record an event when it changes | M |
| RF-BIB-10 | Adopt an existing Resolve database on the server (created outside sqsolve) | S |
| RF-BIB-11 | Read only (archived) if PoC-09 validates it | C |
| RF-BIB-12 | Scheduled maintenance: weekly `VACUUM (ANALYZE)` per library | S |

### 1.3 Credentials and access (RF-ACC)

| ID | Requirement | Priority |
|---|---|---|
| RF-ACC-01 | Each library has an owner role with a strong random password | M |
| RF-ACC-02 | Revealing the password requires recent reauthentication and is recorded in the audit log | M |
| RF-ACC-03 | Rotate a library's password, with a warning that workstations must update it | M |
| RF-ACC-04 | Per-person credentials (member roles) with creation, removal and immediate revocation | S |
| RF-ACC-05 | Network rules per library (list of CIDRs) applied in `pg_hba.conf` | S |
| RF-ACC-06 | Connection instructions per operating system with values ready to copy | M |
| RF-ACC-07 | Downloadable `.resolvedbkey` key (experimental, disabled by default) | C, depends on PoC-05 |
| RF-ACC-08 | Connection portal for collaborators with access only to their libraries | S |

### 1.4 Backups (RF-RES)

| ID | Requirement | Priority |
|---|---|---|
| RF-RES-01 | Manual backup of a library | M |
| RF-RES-02 | Independent schedule per library (presets and cron expression, time zone) | M |
| RF-RES-03 | Configurable retention: last N, daily, weekly, monthly, minimum verified | M |
| RF-RES-04 | Catalog with date, size, duration, status, kind, verification, PostgreSQL version, fingerprint | M |
| RF-RES-05 | Level 1 verification (SHA-256 checksum and readable index) after each backup | M |
| RF-RES-06 | Level 2 verification (test restore with row counts) on a schedule, weekly by default | M |
| RF-RES-07 | Restore as a new library | M |
| RF-RES-08 | Restore by replacement, with a prior backup, swap and revert for N days | M |
| RF-RES-09 | Download a backup | M |
| RF-RES-10 | Import external backups (upload or import folder) with validation | M |
| RF-RES-11 | Pin a backup so that retention never deletes it | M |
| RF-RES-12 | Preview what retention is going to delete | M |
| RF-RES-13 | Encrypted off-site copy with restic (local path, SFTP, REST server, S3 compatible) | S |
| RF-RES-14 | Daily backup of the control plane (SQLite) on the backups volume | M |
| RF-RES-15 | Rebuild the catalog from the manifests on the backups volume | M |
| RF-RES-16 | Point-in-time recovery with pgBackRest | C |

### 1.5 Jobs and automation (RF-TAR)

| ID | Requirement | Priority |
|---|---|---|
| RF-TAR-01 | Every long operation is a persistent job with state, progress and log | M |
| RF-TAR-02 | Jobs view: queued, running, succeeded, failed, interrupted, cancelled | M |
| RF-TAR-03 | Cancel a running job when its type allows it | M |
| RF-TAR-04 | After a restart, running jobs become "interrupted" and their leftovers are cleaned up | M |
| RF-TAR-05 | Misfire policy: run once at startup if the last run was missed within a window | M |
| RF-TAR-06 | Default maintenance jobs (`11-backups-and-automation.md` §12) | M |
| RF-TAR-07 | Event webhooks (before and after operations) signed with HMAC | M |
| RF-TAR-08 | Hooks run in an isolated container | S |

### 1.6 Notifications (RF-NOT)

| ID | Requirement | Priority |
|---|---|---|
| RF-NOT-01 | Channels: generic webhook | M |
| RF-NOT-02 | Channels: SMTP email and ntfy | S |
| RF-NOT-03 | Rules per event type and severity; test button | M |
| RF-NOT-04 | Grouping of repeated notifications | S |

### 1.7 Panel users (RF-USR)

| ID | Requirement | Priority |
|---|---|---|
| RF-USR-01 | Local accounts with Administrator, Operator and Viewer roles | M |
| RF-USR-02 | Optional TOTP second factor per account, configurable as mandatory for administrators | M |
| RF-USR-03 | API tokens with scope and expiry | M |
| RF-USR-04 | Access recovery via CLI inside the container | M |
| RF-USR-05 | Collaborator role (connection portal) | S |
| RF-USR-06 | Login with OIDC (self-hosted provider) | C |
| RF-USR-07 | WebAuthn passkeys | C |

### 1.8 Observation and audit (RF-OBS)

| ID | Requirement | Priority |
|---|---|---|
| RF-OBS-01 | Health dashboard: PostgreSQL, space on each volume, certificates, latest jobs | M |
| RF-OBS-02 | Searchable and filterable event log | M |
| RF-OBS-03 | Hash-chained audit log of every administrative action | M |
| RF-OBS-04 | Export audit log and events as JSON Lines | M |
| RF-OBS-05 | Diagnostic bundle without secrets | M |
| RF-OBS-06 | Metrics in Prometheus format (optional, with token) | S |

### 1.9 Upgrades and compatibility (RF-ACT)

| ID | Requirement | Priority |
|---|---|---|
| RF-ACT-01 | Automatic SQLite schema migrations with a prior copy | M |
| RF-ACT-02 | At startup, check the PostgreSQL version and the data directory version and refuse to continue if they do not match | M |
| RF-ACT-03 | Resolve upgrade window: verified backups of all libraries and a list of steps | M |
| RF-ACT-04 | Show the compatibility matrix bundled with the installed version | M |
| RF-ACT-05 | Logical migration wizard to a new PostgreSQL major version | C |
| RF-ACT-06 | Migration of libraries from another PostgreSQL server | S |

## 2. Non-functional requirements

### 2.1 Reliability and integrity (RNF-FIA)

| ID | Requirement |
|---|---|
| RNF-FIA-01 | No sqsolve operation writes to a library's active database, except `GRANT`, connection-related `ALTER DATABASE` and the swap renames |
| RNF-FIA-02 | A power cut at any moment leaves the system in a state from which it recovers on its own at startup, without loss of libraries or of complete backups |
| RNF-FIA-03 | A backup only counts as valid if its file, its manifest and its checksum match |
| RNF-FIA-04 | The backups volume is identified by a marker file; if it is missing, nothing is written |
| RNF-FIA-05 | Retention never leaves an active library without at least one verified backup |
| RNF-FIA-06 | The control plane works with PostgreSQL down (to diagnose and restore) |

### 2.2 Security (RNF-SEG)

| ID | Requirement |
|---|---|
| RNF-SEG-01 | No administrative PostgreSQL role accepts network connections; local socket only |
| RNF-SEG-02 | PostgreSQL passwords with `scram-sha-256` exclusively |
| RNF-SEG-03 | Panel over HTTPS only; HTTP redirects |
| RNF-SEG-04 | Panel passwords with Argon2id; sessions with `Secure`, `HttpOnly`, `SameSite=Strict` cookie and CSRF token |
| RNF-SEG-05 | sqsolve container without root, with a read-only file system, no capabilities and no Docker socket |
| RNF-SEG-06 | Zero shell executions; external processes with an allowlist of binaries |
| RNF-SEG-07 | Recoverable secrets encrypted with XChaCha20-Poly1305 and the master key in a separate file |
| RNF-SEG-08 | Strict Content Security Policy without inline scripts |
| RNF-SEG-09 | Dangerous operations with typed confirmation, recent reauthentication and audit entry |

### 2.3 Operation (RNF-OPE)

| ID | Requirement |
|---|---|
| RNF-OPE-01 | Images for `linux/amd64` and `linux/arm64` |
| RNF-OPE-02 | sqsolve container start in under 5 s with PostgreSQL already available |
| RNF-OPE-03 | sqsolve idle consumption under 100 MB of RAM |
| RNF-OPE-04 | No Internet access in any essential feature; the only outbound connections are the ones the administrator configures (notifications, off-site copies) |
| RNF-OPE-05 | All startup configuration through documented environment variables; the rest in the interface |
| RNF-OPE-06 | Upgrading means changing the image tag and recreating containers |

### 2.4 Usability (RNF-USA)

| ID | Requirement |
|---|---|
| RNF-USA-01 | Interface in Spanish and English from 1.0 |
| RNF-USA-02 | Responsive design from 360 px wide |
| RNF-USA-03 | WCAG 2.2 level AA |
| RNF-USA-04 | Every error shows what happened, why and what to do, with an `SQS-` code searchable in the documentation |
| RNF-USA-05 | p95 page response under 300 ms with 50 libraries and 5000 backups in the catalog |

### 2.5 Maintainability (RNF-MAN)

| ID | Requirement |
|---|---|
| RNF-MAN-01 | Test coverage of `internal/` at or above 80 % of lines, 100 % in identifier validators, retention and swap |
| RNF-MAN-02 | Direct Go dependencies limited to those in `06-stack.md` |
| RNF-MAN-03 | Reproducible build: two builds of the same commit produce the same binary |
| RNF-MAN-04 | Buildable without Internet with `go mod vendor` |
| RNF-MAN-05 | API described in OpenAPI 3.1 and validated in tests |

### 2.6 Compatibility (RNF-COM)

| ID | Requirement |
|---|---|
| RNF-COM-01 | Resolve 21.1.x supported; 20.3.2 supported if PoC-02 passes |
| RNF-COM-02 | PostgreSQL 18 bundled; external server mode with 16 or later (2.0) |
| RNF-COM-03 | Browsers: last two major versions of Firefox, Chromium and Safari |
| RNF-COM-04 | Docker Engine 24 or later with Compose v2 |
