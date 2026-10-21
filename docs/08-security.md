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
| T-01 | Brute force against the panel | Argon2id, at most 4 at once (section 4); per-IP and per-account limit (5 attempts, then exponential wait up to 15 min), counted before the password is hashed so parallel attempts are counted too; after 10 consecutive wrong passwords the account is locked for 15 minutes (`SQS-A004`): password logins are refused while open sessions and API tokens keep working, and `sqsolve admin unlock` lifts it sooner (ADR 0025, amending ADR 0021); optional TOTP, mandatory for administrators when the global option is on; events `auth.login_failed_burst` and `auth.locked` |
| T-02 | Brute force against PostgreSQL | 140-bit passwords; SCRAM; network rules; `log_connections` and an event for repeated failures read from the PostgreSQL logs (1.1) |
| T-03 | Session theft | `Secure`, `HttpOnly`, `SameSite=Strict` cookies; 256-bit session token stored as a hash; expiry after 30 days without use and 180 days in total (ADR 0021); changing the password closes the user's other sessions; logout on all devices from Account |
| T-04 | CSRF | `SameSite=Strict` cookies. Every panel form carries a synchronizer token in its `csrf` field: an HMAC-SHA256 of the session token under a key derived from the master key, compared in constant time. In `multipart` forms it must be the first part and is checked before the upload is read. The API with a session cookie needs the same token in the `X-CSRF-Token` header for every method except `GET` and `HEAD`; bearer tokens carry no cookie and need none. Before signing in (login and setup forms), a double-submit token: a random `sqsolve_pre` cookie that must match the `csrf` field |
| T-05 | XSS | `html/template` with contextual escaping; CSP `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; object-src 'none'`; the only script is the panel's own `app.js` (ADR 0004): no third-party JavaScript and no inline scripts or styles |
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
| T-17 | Compromised dependency | Closed list of modules; `govulncheck` on the source and on the binary inside the image; 7-day wait for new versions (also Dependabot's cooldown); SBOM attached by Buildx; binary reproducible with the same toolchain (checked by hand, `06` §10, no CI job yet); images pinned by digest and published only after CI passes |
| T-18 | Container escape | Non-root, `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges`, no Docker socket, `tmpfs` for `/tmp` |
| T-19 | Denial of service by a full disk | Space check before every backup; alerts at 80 % and 90 %; retention; recommended reservation on the data volume |
| T-20 | Tampering with the audit log | Hash chain; daily verification and on export; periodic export to the backups volume |
| T-21 | Redirect to another site after signing in (phishing through a crafted link) | The `next` parameter only accepts a path on this server: no scheme or host, exactly one leading slash, and no control characters, whitespace or backslashes, before or after decoding; anything else goes to the dashboard |

## 4. Panel authentication

- Local users. Username, password of at least 8 characters with at least 4 distinct ones, not
  containing the username and not in the local list of common passwords (embedded file
  `internal/auth/common_passwords.txt`: well-known passwords of 8 to 11 characters from public
  leaked-password lists plus studio and Resolve terms, compared without case, no external
  lookups). The panel forms ask for the same minimum.
- Argon2id with `m=64 MiB, t=3, p=2` and a 16-byte salt; parameters stored in the hash so they
  can be raised later. At most 4 hashes run at once in the process, so a burst of logins
  cannot exhaust the server's memory; a request that waits more than 2 seconds for a slot gets
  `SQS-A050` (HTTP 503 with `Retry-After`) and can try again. It does not count towards the
  account lock, only towards the rate limit.
- Account lock (ADR 0025): 10 wrong passwords in a row lock the account for 15 minutes
  (`SQS-A004`, event `auth.locked`); then the count starts again from zero. The lock only
  refuses password logins: sessions already open and API tokens keep working, so an anonymous
  caller who knows a username cannot log its owner out. `sqsolve admin unlock <user>` lifts it
  sooner. Attempts against a locked account are refused before the rate limiter counts them,
  so retrying during the lock leaves no wait behind the unlock. The Users table shows the lock
  while it lasts.
- TOTP RFC 6238 (SHA-1, 6 digits, 30 s, one-step tolerance), with 10 single-use recovery codes
  stored as hashes. Administrators: when "Second factor required for administrators" is on, an
  administrator without TOTP goes straight to enrollment after the password, and every other
  page except enrollment and sign out sends them back there; the API refuses that browser
  session with 403 `SQS-A051`. While the option is on, nobody can remove an administrator's
  second factor from the panel (`SQS-A040`, their own or another's); a lost one is reset with
  `sqsolve admin disable-totp` from the console, and the account must enroll again at its next
  request. API tokens are not affected.
- API tokens: `sqs_` prefix, 32 random bytes, shown only once, stored as SHA-256. Scopes:
  `read`, `backup`, `restore`, `export`, `admin`. Maximum expiry 365 days.
- Changing a password (one's own in Account, or another user's in Settings, Users) clears a
  lock and closes that user's other sessions. API tokens are separate credentials, each revoked
  on its own in Account or by an administrator, and keep working: after a leak, revoke them
  too. Disabling a user closes every session and revokes every token.
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
| Temporary lock | Yes | Maintenance: `NOLOGIN` on the library roles (ADR 0024); before a rename: `ALLOW_CONNECTIONS` |
| Read only | TO INVESTIGATE | PoC-09; Resolve probably needs to write even when only viewing |
| Permissions per project, bin or timeline | DISCARDED | Resolve does not enforce it: any role that gets in can do everything inside the library |
| Collaborator identity in chat | DISCARDED | Each workstation defines it; it is not verifiable |

The UI offers none of the discarded items. To separate clients or productions, use separate
libraries.

## 6. Secrets

| Secret | Where | Who reads it | Rotation |
|---|---|---|---|
| `postgres` password | `db/sqsolve/superuser_password` (0440, PostgreSQL group) | PostgreSQL (when initializing the cluster via `POSTGRES_PASSWORD_FILE`), sqsolve | Not implemented: there is no rotation command. The superuser only enters over the local socket |
| `sqsolve_admin` password | `app/secrets/pg_admin_password` (0400) | sqsolve | No automatic rotation. By hand: stop, delete the file and `docker compose up -d`; `sqsolve-init` generates a new one and sqsolve applies it with the superuser at start |
| Master key (32 bytes) | `app/secrets/master.key` (0400) | sqsolve | Not implemented: there is no command to re-encrypt the secrets with a new key; the recovery kit holds the current one |
| Webhook signing key | Encrypted in SQLite, one per channel | sqsolve | From the UI |
| Library passwords | Encrypted in SQLite | sqsolve | Rotation per library |
| restic password (1.1) | Encrypted in SQLite and in the recovery kit | sqsolve | Not rotatable without rebuilding the repository; documented |
| TLS certificates | `db/sqsolve/tls` and `app/tls` | PostgreSQL, sqsolve | PostgreSQL: self-signed by `sqsolve-init` for 10 years, generated again when the key or the certificate is missing or they do not match; key 0600 of the PostgreSQL user, certificate readable so sqsolve shows its expiry. Panel: self-signed for 2 years and renewed by sqsolve when fewer than 30 days remain (at start and daily, without a restart); an uploaded certificate is never replaced, sqsolve warns 30 days before and at Error severity in the last 14 days |

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
  `X-Forwarded-For` only from the IPs declared in `SQSOLVE_TRUSTED_PROXIES`, in the panel and
  in the API alike: it takes the right-most address that is not itself a trusted proxy, so a
  client cannot prepend one, and ignores the header when a value is not an address.
- On hosts with several interfaces, the reference Compose allows publishing 5432 only on the LAN
  IP (`192.168.1.10:5432:5432`), as on Synology.
- Network rules of a library (and every change of libraries, credentials, "TLS only" or access to
  the maintenance database) are applied by regenerating `pg_hba.conf`: sqsolve writes it keeping
  the previous one as `pg_hba.conf.prev`, validates it with `pg_hba_file_rules` and reloads
  PostgreSQL. An invalid file is rolled back to `.prev` without reloading (`SQS-P042`). A change
  counts as applied only after a successful reload: if the validation query or the reload fails
  (`SQS-P001`, `SQS-P043`), the change is reported as not applied, the new file stays on disk, and
  the next sync (the next change, Settings > Maintenance > "Regenerate pg_hba.conf", or the next
  start of sqsolve) validates and reloads it again even though the file did not change. Before
  this, an identical second attempt was taken as "unchanged" and reported success while the old
  rules stayed in force.

## 8. Hardening of the sqsolve container

```yaml
user: "${SQSOLVE_UID:-1000}:${SQSOLVE_GID:-1000}"
read_only: true
cap_drop: [ALL]
security_opt: ["no-new-privileges:true"]
tmpfs: ["/tmp:size=64m,mode=1777"]
```

`pg_dump` writes directly to the backups volume, never to `/tmp`. The PostgreSQL container uses
the official image pinned by digest and runs as `user: "999:999"` (the image's `postgres` user;
`sqsolve-init` prepares the data folder for it, as TrueNAS apps do), so it never runs as root,
with `no-new-privileges`. The compose file sets no `cap_drop` for it: it keeps Docker's default
capabilities. A minimal capability list is To be confirmed (task SQS-031).

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
