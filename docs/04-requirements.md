# 04. Requirements

Priority: **M** mandatory for 1.0, **S** desirable in 1.x, **C** later. The status column says
what sqsolve 1.2.2 does:

- **Met**: implemented.
- **Partial**: implemented in part; the note says what is missing.
- **Not met**, **Not evidenced** or **Not measured**: the requirement is not satisfied, or there
  is no record that it is.
- **Not implemented**: sqsolve does not do it. Product directions are in `20-roadmap.md`.
- **Changed**: a later decision (an ADR or a release) replaced part of the requirement; the note
  says how.

Behavior that depends on Resolve is validated only as far as `acceptance/1.0.md`,
`acceptance/1.2.2.md` and `poc/` record (Resolve Studio 21.1.1 on Linux); the notes mark what
those records do not cover.

## 1. Functional requirements

### 1.1 Installation and initial setup (RF-INS)

| ID | Requirement | Priority | Status (1.2.2) |
|---|---|---|---|
| RF-INS-01 | Installation with a single Docker Compose file that brings up PostgreSQL and sqsolve | M | Met |
| RF-INS-02 | On first start all secrets are generated (superuser, control role, master key, TLS certificates) without intervention | M | Met |
| RF-INS-03 | Setup wizard: language, time zone, administrator account, address advertised to workstations, default backup policy, recovery kit download | M | Met, except the language (English is the only language) and the time zone (taken from `TZ` and only shown) |
| RF-INS-04 | The wizard is only reachable while no administrator exists and requires the installation code printed in the container log | M | Met |
| RF-INS-05 | If a mandatory volume is not mounted or lacks permissions, sqsolve refuses to start and explains what is missing | M | Met |

### 1.2 Libraries (RF-BIB)

| ID | Requirement | Priority | Status (1.2.2) |
|---|---|---|---|
| RF-BIB-01 | Create a library with display name, identifier (slug) and notes | M | Met |
| RF-BIB-02 | Create a library from a local template (copy of a library freshly initialized by Resolve) | S | Not implemented |
| RF-BIB-03 | List libraries with state, size, active connections, last backup and detected version | M | Met; the Resolve version shown is the declared one, sqsolve does not detect it |
| RF-BIB-04 | View a library's details: connection, backups, schedule, access, activity, settings | M | Met |
| RF-BIB-05 | Edit display name, notes, connection limit and declared Resolve version | M | Met |
| RF-BIB-06 | Maintenance mode: block new connections and, optionally, close the active ones | M | Met |
| RF-BIB-07 | Delete: verified final backup, lock, rename to the trash, purge after N days (30 by default); restorable from the trash while it exists | M | Met |
| RF-BIB-08 | View active sessions per library (user, IP, application, start) and terminate a session | M | Met |
| RF-BIB-09 | Compute and watch the schema fingerprint; record an event when it changes | M | Met; a change during a real Resolve upgrade is not validated (PoC-03) |
| RF-BIB-10 | Adopt an existing Resolve database on the server (created outside sqsolve) | S | Not implemented |
| RF-BIB-11 | Read only (archived) if PoC-09 validates it | C | Not implemented |
| RF-BIB-12 | Scheduled maintenance: weekly `VACUUM (ANALYZE)` per library | S | Met: one weekly job runs `vacuumdb --analyze` on every library that is active or in maintenance |

### 1.3 Credentials and access (RF-ACC)

| ID | Requirement | Priority | Status (1.2.2) |
|---|---|---|---|
| RF-ACC-01 | Each library has an owner role with a strong random password | M | Met |
| RF-ACC-02 | Revealing the password requires recent reauthentication and is recorded in the audit log | M | Changed by ADR 0021: no reauthentication; only whoever has the permission reveals it, and every reveal is in the audit log |
| RF-ACC-03 | Rotate a library's password, with a warning that workstations must update it | M | Met |
| RF-ACC-04 | Per-person credentials (member roles) with creation, removal and immediate revocation | S | Met; not validated with real Resolve (PoC-01 steps 3 and 5) |
| RF-ACC-05 | Network rules per library (list of CIDRs) applied in `pg_hba.conf` | S | Met |
| RF-ACC-06 | Connection instructions per operating system with values ready to copy | M | Partial: one set of instructions for every operating system, with values to copy and print; connecting is validated on Linux only |
| RF-ACC-07 | Downloadable `.resolvedbkey` key (experimental, disabled by default) | C, depends on PoC-05 | Not implemented (PoC-05 not run) |
| RF-ACC-08 | Connection portal for collaborators with access only to their libraries | S | Not implemented |

### 1.4 Backups (RF-RES)

| ID | Requirement | Priority | Status (1.2.2) |
|---|---|---|---|
| RF-RES-01 | Manual backup of a library | M | Met |
| RF-RES-02 | Independent schedule per library (presets and cron expression, time zone) | M | Met |
| RF-RES-03 | Configurable retention: last N, daily, weekly, monthly, minimum verified | M | Met |
| RF-RES-04 | Catalog with date, size, duration, status, kind, verification, PostgreSQL version, fingerprint | M | Met |
| RF-RES-05 | Level 1 verification (SHA-256 checksum and readable index) after each backup | M | Met |
| RF-RES-06 | Level 2 verification (test restore with row counts) on a schedule, weekly by default | M | Met |
| RF-RES-07 | Restore as a new library | M | Met |
| RF-RES-08 | Restore by replacement, with a prior backup, swap and revert for N days | M | Met (7 days by default); the project must be closed in Resolve first (`19-risks.md` T-05) |
| RF-RES-09 | Download a backup | M | Met; restoring the file with Resolve's own Restore is not validated (PoC-04) |
| RF-RES-10 | Import external backups (upload or import folder) with validation | M | Met; files made by Resolve's own Backup are not validated (PoC-04) |
| RF-RES-11 | Pin a backup so that retention never deletes it | M | Met |
| RF-RES-12 | Preview what retention is going to delete | M | Met |
| RF-RES-13 | Encrypted off-site copy with restic (local path, SFTP, REST server, S3 compatible) | S | Not implemented |
| RF-RES-14 | Daily backup of the control plane (SQLite) on the backups volume | M | Met |
| RF-RES-15 | Rebuild the catalog from the manifests on the backups volume | M | Met |
| RF-RES-16 | Point-in-time recovery with pgBackRest | C | Not implemented |

### 1.5 Jobs and automation (RF-TAR)

| ID | Requirement | Priority | Status (1.2.2) |
|---|---|---|---|
| RF-TAR-01 | Every long operation is a persistent job with state, progress and log | M | Met |
| RF-TAR-02 | Jobs view: queued, running, succeeded, failed, interrupted, cancelled | M | Met |
| RF-TAR-03 | Cancel a running job when its type allows it | M | Met |
| RF-TAR-04 | After a restart, running jobs become "interrupted" and their leftovers are cleaned up | M | Met |
| RF-TAR-05 | Misfire policy: run once at startup if the last run was missed within a window | M | Met |
| RF-TAR-06 | Default maintenance jobs (`11-backups-and-automation.md` §12) | M | Met |
| RF-TAR-07 | Event webhooks (before and after operations) signed with HMAC | M | Met |
| RF-TAR-08 | Hooks run in an isolated container | S | Not implemented |

### 1.6 Notifications (RF-NOT)

| ID | Requirement | Priority | Status (1.2.2) |
|---|---|---|---|
| RF-NOT-01 | Channels: generic webhook | M | Met |
| RF-NOT-02 | Channels: SMTP email and ntfy | S | Met; not tested against real SMTP and ntfy servers |
| RF-NOT-03 | Rules per event type and severity; test button | M | Met |
| RF-NOT-04 | Grouping of repeated notifications | S | Met: repeats are held for 30 minutes per channel, event type, library and subject and sent as one summary; a more severe event always goes out |

### 1.7 Panel users (RF-USR)

| ID | Requirement | Priority | Status (1.2.2) |
|---|---|---|---|
| RF-USR-01 | Local accounts with Administrator, Operator and Viewer roles | M | Met |
| RF-USR-02 | Optional TOTP second factor per account, configurable as mandatory for administrators | M | Met |
| RF-USR-03 | API tokens with scope and expiry | M | Met |
| RF-USR-04 | Access recovery via CLI inside the container | M | Met |
| RF-USR-05 | Collaborator role (connection portal) | S | Not implemented |
| RF-USR-06 | Login with OIDC (self-hosted provider) | C | Not implemented |
| RF-USR-07 | WebAuthn passkeys | C | Not implemented |

### 1.8 Observation and audit (RF-OBS)

| ID | Requirement | Priority | Status (1.2.2) |
|---|---|---|---|
| RF-OBS-01 | Health dashboard: PostgreSQL, space on each volume, certificates, latest jobs | M | Met |
| RF-OBS-02 | Searchable and filterable event log | M | Partial: filters by minimum severity and type in the panel, and by library in the API; no text search |
| RF-OBS-03 | Hash-chained audit log of every administrative action | M | Met |
| RF-OBS-04 | Export audit log and events as JSON Lines | M | Partial: the audit log exports as JSON Lines (panel, and one file per month on the backups volume); events are only listed as JSON by the API |
| RF-OBS-05 | Diagnostic bundle without secrets | M | Met |
| RF-OBS-06 | Metrics in Prometheus format (optional, with token) | S | Not implemented |

### 1.9 Upgrades and compatibility (RF-ACT)

| ID | Requirement | Priority | Status (1.2.2) |
|---|---|---|---|
| RF-ACT-01 | Automatic SQLite schema migrations with a prior copy | M | Met |
| RF-ACT-02 | At startup, check the PostgreSQL version and the data directory version and refuse to continue if they do not match | M | Partial: a server whose major version differs from the bundled `pg_dump` is shown as critical on the dashboard and fails `sqsolve doctor`, but sqsolve keeps running |
| RF-ACT-03 | Resolve upgrade window: verified backups of all libraries and a list of steps | M | Met; not validated during a real Resolve upgrade (PoC-03) |
| RF-ACT-04 | Show the compatibility matrix bundled with the installed version | M | Not met: the panel points to `14-compatibility.md`; the registry of known fingerprints (`internal/compat`) is used only by the export |
| RF-ACT-05 | Logical migration wizard to a new PostgreSQL major version | C | Not implemented |
| RF-ACT-06 | Migration of libraries from another PostgreSQL server | S | Not implemented |

## 2. Non-functional requirements

### 2.1 Reliability and integrity (RNF-FIA)

| ID | Requirement | Status (1.2.2) |
|---|---|---|
| RNF-FIA-01 | No sqsolve operation writes to a library's active database, except `GRANT`, connection-related `ALTER DATABASE` and the swap renames | Met |
| RNF-FIA-02 | A power cut at any moment leaves the system in a state from which it recovers on its own at startup, without loss of libraries or of complete backups | Partial: tested with processes killed mid-job (`make scenarios`) and a power-loss model in the Go tests; no power cut during a backup on real hardware is recorded |
| RNF-FIA-03 | A backup only counts as valid if its file, its manifest and its checksum match | Met |
| RNF-FIA-04 | The backups volume is identified by a marker file; if it is missing, nothing is written | Met |
| RNF-FIA-05 | Retention never leaves an active library without at least one verified backup | Met |
| RNF-FIA-06 | The control plane works with PostgreSQL down (to diagnose and restore) | Met |

### 2.2 Security (RNF-SEG)

| ID | Requirement | Status (1.2.2) |
|---|---|---|
| RNF-SEG-01 | No administrative PostgreSQL role accepts network connections; local socket only | Met |
| RNF-SEG-02 | PostgreSQL passwords with `scram-sha-256` exclusively | Met |
| RNF-SEG-03 | Panel over HTTPS only; HTTP redirects | Met; the HTTP redirect listens only when `SQSOLVE_HTTP_REDIRECT` is set |
| RNF-SEG-04 | Panel passwords with Argon2id; sessions with `Secure`, `HttpOnly`, `SameSite=Strict` cookie and CSRF token | Met |
| RNF-SEG-05 | sqsolve container without root, with a read-only file system, no capabilities and no Docker socket | Met |
| RNF-SEG-06 | Zero shell executions; external processes with an allowlist of binaries | Met |
| RNF-SEG-07 | Recoverable secrets encrypted with XChaCha20-Poly1305 and the master key in a separate file | Met |
| RNF-SEG-08 | Strict Content Security Policy without inline scripts | Met |
| RNF-SEG-09 | Dangerous operations with typed confirmation, recent reauthentication and audit entry | Changed by ADR 0021: typed confirmation and audit entry, no reauthentication |

### 2.3 Operation (RNF-OPE)

| ID | Requirement | Status (1.2.2) |
|---|---|---|
| RNF-OPE-01 | Images for `linux/amd64` and `linux/arm64` | Met |
| RNF-OPE-02 | sqsolve container start in under 5 s with PostgreSQL already available | Not measured |
| RNF-OPE-03 | sqsolve idle consumption under 100 MB of RAM | Not measured |
| RNF-OPE-04 | No Internet access in any essential feature; the only outbound connections are the ones the administrator configures (notifications, off-site copies) | Not evidenced: no run without Internet is recorded (AC-10); the panel loads no external resources |
| RNF-OPE-05 | All startup configuration through documented environment variables; the rest in the interface | Met |
| RNF-OPE-06 | Upgrading means changing the image tag and recreating containers | Met; a new PostgreSQL minor version also needs the `postgres` line of the compose file replaced |

### 2.4 Usability (RNF-USA)

| ID | Requirement | Status (1.2.2) |
|---|---|---|
| RNF-USA-01 | Interface in Spanish and English from 1.0 | Changed in 1.1.0: English is the only supported language |
| RNF-USA-02 | Responsive design from 360 px wide | Not evidenced: no check at 360 px is recorded (tables turn into cards under 768 px) |
| RNF-USA-03 | WCAG 2.2 level AA | Not evidenced: no formal accessibility audit; the 44 px targets are done and tested |
| RNF-USA-04 | Every error shows what happened, why and what to do, with an `SQS-` code searchable in the documentation | Partial: every expected error has an `SQS-` code listed in `docs/manual/errors.md`; many messages say what happened but not why or what to do |
| RNF-USA-05 | p95 page response under 300 ms with 50 libraries and 5000 backups in the catalog | Not measured |

### 2.5 Maintainability (RNF-MAN)

| ID | Requirement | Status (1.2.2) |
|---|---|---|
| RNF-MAN-01 | Test coverage of `internal/` at or above 80 % of lines, 100 % in identifier validators, retention and swap | Met for the 80 % (87.1 % of statements, `16-testing.md` §1); the 100 % for validators, retention and swap is not recorded: To be confirmed |
| RNF-MAN-02 | Direct Go dependencies limited to those in `06-stack.md` | Met |
| RNF-MAN-03 | Reproducible build: two builds of the same commit produce the same binary | Not evidenced: one check by hand on one machine (`06-stack.md` §10); two clean builds have not been compared |
| RNF-MAN-04 | Buildable without Internet with `go mod vendor` | Not met: there is no `vendor/` directory and the build downloads the modules |
| RNF-MAN-05 | API described in OpenAPI 3.1 and validated in tests | Not met: there is no `api/openapi.yaml`; `internal/api` tests the request contract without a schema |

### 2.6 Compatibility (RNF-COM)

| ID | Requirement | Status (1.2.2) |
|---|---|---|
| RNF-COM-01 | Resolve 21.1.x supported; 20.3.2 supported if PoC-02 passes | Partial: validated with Resolve Studio 21.1.1 on Linux only; macOS, Windows and 20.3.2 (PoC-02) not validated |
| RNF-COM-02 | PostgreSQL 18 bundled; external server mode with 16 or later (2.0) | Partial: PostgreSQL 18 bundled (18.6); there is no external server mode |
| RNF-COM-03 | Browsers: last two major versions of Firefox, Chromium and Safari | Not evidenced: no browser test is recorded |
| RNF-COM-04 | Docker Engine 24 or later with Compose v2 | Not evidenced: the Docker versions used in the tests are not recorded |
