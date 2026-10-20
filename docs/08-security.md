# 08. Security

## 1. Assets

| Asset | Why it matters |
|---|---|
| Libraries (project metadata) | Months of editing and color work; hard to rebuild |
| Backups | A copy of all of the above; an attacker who can read them has the projects |
| Library credentials | Give full read and write access to a library |
| Panel accounts and tokens | Control everything else |
| Master key and secrets | Decrypt stored credentials |
| Availability | A server that is down stops the whole studio |

## 2. Actors and boundaries

| Actor | Trust |
|---|---|
| Administrator with host access | Total (can read the volumes). sqsolve does not try to protect itself from them |
| Panel administrator or operator | According to their role |
| Workstation with a credential | Total over its library; Resolve stores the password on the workstation |
| Another machine on the LAN | None; it can try to connect to 5432 and 9432 |
| Attacker on the Internet | None; should not reach the ports (VPN) |
| Hook code | Minimal: it sees neither secrets nor PostgreSQL |
| Compromised dependency | Supply-chain risk; limited with few dependencies and isolation |

Boundaries: browser and panel (HTTPS), workstations and PostgreSQL (TCP 5432), sqsolve and
PostgreSQL (Unix socket), sqsolve and external processes (`execx`), sqsolve and notification
services (optional egress), containers and host (volumes).

## 3. Threats and controls

| ID | Threat | Controls |
|---|---|---|
| T-01 | Brute force against the panel | Argon2id; per-IP and per-account limit (5 attempts, then exponential wait up to 15 min); after 10 consecutive wrong passwords the account is locked and only `sqsolve admin unlock` in the container console lifts it (ADR 0021); optional TOTP; events `auth.login_failed_burst` and `auth.locked` |
| T-02 | Brute force against PostgreSQL | 140-bit passwords; SCRAM; network rules; `log_connections` and an event for repeated failures read from the PostgreSQL logs (1.1) |
| T-03 | Session theft | `Secure`, `HttpOnly`, `SameSite=Strict` cookies; 256-bit session token stored as a hash; expiry by inactivity (8 h) and absolute (7 days); logout on all devices |
| T-04 | CSRF | `SameSite=Strict` and a synchronizer token in every form and in `hx-headers` |
| T-05 | XSS | `html/template` with contextual escaping; CSP `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; frame-ancestors 'none'; form-action 'self'`; htmx with `allowEval = false` and `includeIndicatorStyles = false` |
| T-06 | SQL injection | Parameters always; DDL only with validated and sanitized identifiers (`07` §2); fuzzing tests; `gosec` |
| T-07 | Command injection | No shell; `execx` with an allowlist (`pg_dump`, `pg_restore`, `vacuumdb`, `restic`); arguments as an array; paths validated against the allowed directory |
| T-08 | Escalation from SQL injection to command execution | Control role without superuser (no `COPY ... PROGRAM`); the superuser is only used in listed and audited code paths |
| T-09 | Administrative connection from the network | `pg_hba.conf` rejects `postgres` and `sqsolve_admin` over the network; sqsolve uses the local socket |
| T-10 | Eavesdropping on the LAN | TLS on PostgreSQL: opportunistic encryption by default (Resolve 21.1.1 negotiates TLS 1.3 on its own, checked on 2026-10-19) and an optional "TLS only" mode that switches `pg_hba.conf` to `hostssl`, with a warning that how each Resolve version implements it is unknown; the sessions view shows whether each connection is encrypted (`pg_stat_ssl`); panel over HTTPS only; VPN recommended outside the LAN |
| T-11 | Takeover of the setup wizard | Single-use installation code printed in the container log; the wizard closes when the first administrator is created |
| T-12 | Destructive operation by mistake or from a stolen session | Typed confirmation with the slug; automatic prior backup; trash; audit log; notification. No reauthentication since ADR 0021 |
| T-13 | Reading backups | Volume with 0700 permissions owned by the sqsolve UID; age encryption for off-site copies; restic always encrypts |
| T-14 | Secrets leaking into logs | `secret.String` type that prints as `[redacted]`; `slog` handler that redacts known keys; a test that searches the whole output for test secrets |
| T-15 | Malicious or faulty hook | No hooks in 1.0; in 1.1, separate container, no secrets, no socket, with a time limit and read-only access to backups |
| T-16 | Webhook to the internal network (SSRF) | Only URLs configured by administrators; warning if it points to private addresses; no automatic redirects |
| T-17 | Compromised dependency | Closed list of modules; `govulncheck`; 7-day wait for new versions; SBOM; reproducible build; images pinned by digest |
| T-18 | Container escape | Non-root, `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges`, no Docker socket, `tmpfs` for `/tmp` |
| T-19 | Denial of service by a full disk | Space check before every backup; alerts at 80 % and 90 %; retention; recommended reservation on the data volume |
| T-20 | Tampering with the audit log | Hash chain; daily verification and on export; periodic export to the backups volume |

## 4. Panel authentication

- Local users. Username, password of at least 8 characters checked against a local list of
  common passwords (embedded file, no external lookups).
- Argon2id with `m=64 MiB, t=3, p=2` and a 16-byte salt; parameters stored in the hash so they
  can be raised later.
- TOTP RFC 6238 (SHA-1, 6 digits, 30 s, one-step tolerance), with 10 single-use recovery codes
  stored as hashes. Administrators: TOTP mandatory if the global option is enabled.
- API tokens: `sqs_` prefix, 32 random bytes, shown only once, stored as SHA-256. Scopes:
  `read`, `backup`, `restore`, `export`, `admin`. Maximum expiry 365 days.
- Recovery: `docker compose exec sqsolve sqsolve admin reset-password <user>` or
  `sqsolve admin create --role admin`. Whoever reaches the container console already controls
  the host.

## 5. Authorization

Panel roles:

| Action | Viewer | Operator | Administrator | Collaborator (1.1) |
|---|---|---|---|---|
| View status, libraries, backups, jobs, events | Yes | Yes | Yes | Only their libraries |
| View connection details (without password) | Yes | Yes | Yes | Only their libraries |
| Reveal a library's password | No | No | Yes (audited) | Only their own (audited) |
| Back up now, verify | No | Yes | Yes | No |
| Restore as a copy, download a backup | No | Yes | Yes | No |
| Restore by replacing, revert | No | No | Yes | No |
| Export projects and grades (experimental, if enabled) | No | Yes | Yes | No |
| Create, edit, maintenance, delete libraries | No | No | Yes | No |
| Credentials, network rules | No | No | Yes | No |
| Schedules, retention, notifications | No | No | Yes | No |
| Users, other people's tokens, system settings | No | No | Yes | No |
| Audit log | No | Yes (read) | Yes | No |

The matrix lives in `internal/auth/policy.go` as a table and is tested exhaustively: every
combination of role, action and resource has a case.

Authorization in Resolve: it exists only at the connection level. What can really be enforced:

| Mechanism | Enforceable | Note |
|---|---|---|
| Which credential enters which library | Yes | Roles and `pg_hba.conf` |
| From which networks | Yes | `pg_hba.conf` (if PoC-08 confirms the real IP is visible) |
| How many connections | Yes | `CONNECTION LIMIT` |
| Temporary lock | Yes | `ALLOW_CONNECTIONS` |
| Read only | TO INVESTIGATE | PoC-09; Resolve probably needs to write even when only viewing |
| Permissions per project, bin or timeline | DISCARDED | Resolve does not enforce it: any role that gets in can do everything inside the library |
| Collaborator identity in chat | DISCARDED | Each workstation defines it; it is not verifiable |

The UI offers none of the discarded items. To separate clients or productions, use separate
libraries.

## 6. Secrets

| Secret | Where | Who reads it | Rotation |
|---|---|---|---|
| `postgres` password | `db/sqsolve/superuser_password` (0440, PostgreSQL group) | PostgreSQL (when initializing the cluster via `POSTGRES_PASSWORD_FILE`), sqsolve | `sqsolve admin rotate-superuser` |
| `sqsolve_admin` password | `app/secrets/pg_admin_password` (0400) | sqsolve | Automatic every 90 days or on demand |
| Master key (32 bytes) | `app/secrets/master.key` (0400) | sqsolve | `sqsolve admin rotate-master-key` re-encrypts everything in one transaction |
| Webhook signing key | Encrypted in SQLite, one per channel | sqsolve | From the UI |
| Library passwords | Encrypted in SQLite | sqsolve | Rotation per library |
| restic password (1.1) | Encrypted in SQLite and in the recovery kit | sqsolve | Not rotatable without rebuilding the repository; documented |
| TLS certificates | `db/sqsolve/tls` and `app/tls` | PostgreSQL, sqsolve | Automatic renewal 30 days before expiry (self-signed, 2 years) |

Encryption of recoverable secrets: XChaCha20-Poly1305 with a random 24-byte nonce; the key
identifier and the column name go as additional authenticated data so that a value cannot be
moved to another column.

Recovery kit: file `sqsolve-recovery-<installation>.txt` that the wizard forces you to download,
with the master key in base64, the certificate fingerprint, the version and the recovery steps.
It can be downloaded again from Settings. Without it, libraries remain safe but their passwords
must be rotated.

## 7. Network

- PostgreSQL only on the LAN or the VPN. Never publish 5432 on the Internet (ADR 0015).
- Recommended remote access: WireGuard (for example, the wg-easy app many NAS offer) or
  Headscale with Tailscale clients. The documentation covers both; sqsolve does not include a VPN.
- The panel can be published behind the user's own reverse proxy; sqsolve honors
  `X-Forwarded-For` only from the IPs declared in `SQSOLVE_TRUSTED_PROXIES`.
- On hosts with several interfaces, the reference Compose allows publishing 5432 only on the LAN
  IP (`192.168.1.10:5432:5432`), as on Synology.

## 8. Hardening of the sqsolve container

```yaml
user: "${SQSOLVE_UID:-1000}:${SQSOLVE_GID:-1000}"
read_only: true
cap_drop: [ALL]
security_opt: ["no-new-privileges:true"]
tmpfs: ["/tmp:size=64m,mode=1777"]
```

`pg_dump` writes directly to the backups volume, never to `/tmp`. The PostgreSQL container uses
the official image with its default user; `no-new-privileges` and `cap_drop` are applied with the
minimal list its entrypoint needs (`CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETGID`, `SETUID`),
validated in SQS-031.

## 9. Audit log

Every action that changes state or exposes secrets is recorded: logins and failed logins, user
changes, library creation and changes, password reveals, rotations, network rules, manual
backups, restores, reverts, deletions, schedule and retention changes, settings changes,
superuser use, CLI command execution.

Format of each entry: `seq`, date, actor (type, id, IP), action, target, result, details without
secrets, `prev_hash` and `hash = SHA-256(prev_hash || canonical JSON of the entry)`. Verification
walks the chain; a break raises the critical event `audit.chain_broken`.

## 10. Incident response and disclosure

- `SECURITY.md` with a contact email (To be confirmed) and a 90-day coordinated disclosure
  policy.
- Security advisories published as GitHub Security Advisories and in the changelog.
- "Leaked credential" guide: rotate the affected library, review sessions and the audit log,
  review network rules.
- "Compromised server" guide: isolate, keep the volumes, reinstall from scratch, restore from
  verified backups prior to the incident, rotate every credential and the master key.

## 10. Security review of 1.0 (2026-10-19)

Local run of OWASP ZAP baseline (image `ghcr.io/zaproxy/zaproxy:stable`, passive, 3 minutes of
crawling, no session) against the panel on mininas with sqsolve 0.1.5: 0 failures, 59 rules
green, 8 warnings. What was done with each warning:

| ZAP warning | Decision |
|---|---|
| `Strict-Transport-Security` missing | Fixed: the panel sends a one-year HSTS header when serving HTTPS |
| `Cross-Origin-Resource-Policy` and `Cross-Origin-Embedder-Policy` missing | CORP `same-origin` added. COEP no: there are no cross-origin resources to isolate and it would break future local image integrations |
| `Cache-Control: no-store` and "non-storable content" | Deliberate: no panel page should be cached |
| "Suspicious" comment in `app.js` | A normal code comment (an identifier suggestion); nothing to hide |
| User-controllable attribute on `/login?u=` | The value goes through `html/template`, which escapes it; not XSS. Kept to prefill the username after an error |
| Authentication request and session response identified | Informational: ZAP recognizes the login and the cookie |

Manual review against this document with ADR 0021 applied: threats T-01 to T-13 remain covered;
T-12 changes from "reauthentication" to "typed confirmation and audit log" and T-01 gains the
account lock. Open findings: none. Nightly ZAP in CI is left for 1.1.
