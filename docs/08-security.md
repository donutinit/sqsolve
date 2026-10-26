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
| Hook code | Not applicable: sqsolve runs no user code (ADR 0011; hooks are a roadmap direction) |
| Compromised dependency | Supply-chain risk; limited with few dependencies and isolation |

Boundaries: browser and panel (HTTPS), workstations and PostgreSQL (TCP 5432), sqsolve and
PostgreSQL (Unix socket), sqsolve and external processes (`execx`), sqsolve and notification
services (optional egress), containers and host (volumes).

## 3. Threats and controls

| ID | Threat | Controls |
|---|---|---|
| T-01 | Brute force against the panel | Argon2id, at most 4 at once (section 4); per-IP and per-account limit (5 attempts, then exponential wait up to 15 min), counted before the password is hashed so parallel attempts are counted too; after 10 consecutive wrong passwords the account is locked for 15 minutes (`SQS-A004`): password logins are refused while open sessions and API tokens keep working, and `sqsolve admin unlock` lifts it sooner (ADR 0025, amending ADR 0021); optional TOTP, mandatory for administrators when the global option is on; events `auth.login_failed_burst` and `auth.locked` |
| T-02 | Brute force against PostgreSQL | 140-bit passwords; SCRAM; network rules; `log_connections` in the PostgreSQL log. sqsolve does not read that log and raises no event for repeated failed connections |
| T-03 | Session theft | `Secure`, `HttpOnly`, `SameSite=Strict` cookies; 256-bit session token stored as a hash; expiry after 30 days without use and 180 days in total (ADR 0021); changing the password closes the user's other sessions; logout on all devices from Account |
| T-04 | CSRF | `SameSite=Strict` cookies. Every panel form carries a synchronizer token in its `csrf` field: an HMAC-SHA256 of the session token under a key derived from the master key, compared in constant time. In `multipart` forms it must be the first part and is checked before the upload is read. The API with a session cookie needs the same token in the `X-CSRF-Token` header for every method except `GET` and `HEAD`; bearer tokens carry no cookie and need none. Before signing in (login and setup forms), a double-submit token: a random `sqsolve_pre` cookie that must match the `csrf` field |
| T-05 | XSS | `html/template` with contextual escaping; CSP `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; form-action 'self'; base-uri 'none'; object-src 'none'`; the only script is the panel's own `app.js` (ADR 0004): no third-party JavaScript and no inline scripts or styles |
| T-06 | SQL injection | Parameters always; DDL only with validated and sanitized identifiers (`07` §2); fuzzing tests (`make fuzz`, nightly in CI); `go vet` and `staticcheck` in `make check` (no `gosec`) |
| T-07 | Command injection | No shell; `execx` with an allowlist (`pg_dump`, `pg_restore`, `vacuumdb`, `restic`); arguments as an array; paths validated against the allowed directory |
| T-08 | Escalation from SQL injection to command execution | Control role without superuser (no `COPY ... PROGRAM`); the superuser is only used by `pg.Bootstrap` at every start, to create or repair `sqsolve_admin`, and by the doctor checks to test that it can connect (`07` §3); neither writes an audit entry |
| T-09 | Administrative connection from the network | `pg_hba.conf` rejects `postgres` and `sqsolve_admin` over the network; sqsolve uses the local socket |
| T-10 | Eavesdropping on the LAN | TLS on PostgreSQL: opportunistic encryption by default (Resolve 21.1.1 negotiates TLS 1.3 on its own, checked on 2026-10-19) and an optional "TLS only" mode that switches `pg_hba.conf` to `hostssl` (tested with Resolve 21.1.1 on Linux only, PoC-06), with a warning that how each Resolve version implements it is unknown; the sessions view shows whether each connection is encrypted (`pg_stat_ssl`); panel over HTTPS only; VPN recommended outside the LAN |
| T-11 | Takeover of the setup wizard | Single-use installation code printed in the container log; the wizard closes when the first administrator is created. The code is held while that administrator is created and spent with it, so concurrent submissions create one; a failed attempt (a weak password) keeps it |
| T-12 | Destructive operation by mistake or from a stolen session | Typed confirmation with the slug; automatic prior backup; trash; audit log; notification. No reauthentication since ADR 0021 |
| T-13 | Reading backups | Volume with 0700 permissions owned by the sqsolve UID. sqsolve does not encrypt backups and makes no off-site copies (an off-site copy with restic is a roadmap direction) |
| T-14 | Secrets leaking into logs | `secret.String` type that prints as `[redacted]`; `slog` handler that redacts known keys; a test that searches the whole output for test secrets |
| T-15 | Malicious or faulty hook | sqsolve has no hooks and runs no user code (ADR 0011; isolated hooks are a roadmap direction) |
| T-16 | Webhook to the internal network (SSRF) | Only URLs configured by administrators; warning if it points to private addresses; no automatic redirects |
| T-17 | Compromised dependency | Closed list of modules; `govulncheck` on the source and on the binary inside the image; 7-day wait for new versions (also Dependabot's cooldown); SBOM and provenance attached by Buildx; images signed with cosign; binary reproducible with the same toolchain (checked by hand, `06` §10; no CI job checks it); base images pinned by digest; images published only after CI passes |
| T-18 | Container escape | Non-root, `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges`, no Docker socket, `tmpfs` for `/tmp` |
| T-19 | Denial of service by a full disk | Space check before every backup; alerts at 80 % and 90 %; retention; recommended reservation on the data volume |
| T-20 | Tampering with the audit log | Hash chain; verification every day (the `system.housekeeping` job), when the audit log is opened in Activity, in the doctor checks and with `sqsolve audit verify`; the daily control plane backup exports the current month to `_system/audit-<YYYYMM>.jsonl` on the backups volume |
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
  stored as hashes. A code is accepted once: one conditional update moves the account's last
  used time step past it, so two sessions presenting the same code at once cannot both pass, and
  a code whose step cannot be recorded is refused. Administrators: when "Second factor required for administrators" is on, an
  administrator without TOTP goes straight to enrollment after the password, and every other
  page except enrollment and sign out sends them back there; the API refuses that browser
  session with 403 `SQS-A051`. While the option is on, nobody can remove an administrator's
  second factor from the panel (`SQS-A040`, their own or another's); a lost one is reset with
  `sqsolve admin disable-totp` from the console, and the account must enroll again at its next
  request. API tokens are not affected.
- API tokens: `sqs_` prefix, 32 random bytes, shown only once, stored as SHA-256. Scopes:
  `read`, `backup`, `restore`, `export`, `admin`. Maximum expiry 365 days.
- Changing a password (one's own in Account, or another user's in Settings, Users) clears a
  lock and closes that user's other sessions, in one SQLite transaction with the new password and
  its audit entry: if the sessions cannot be closed, the password does not change. API tokens are separate credentials, each revoked
  on its own in Account or by an administrator, and keep working: after a leak, revoke them
  too. Disabling a user closes every session and revokes every token. At least one active
  administrator always remains (`SQS-A014`): the check and the change are one immediate SQLite
  transaction, so two administrators demoting or disabling each other at the same time cannot
  leave none.
- Recovery: `docker compose exec sqsolve sqsolve admin reset-password <user>` or
  `sqsolve admin create --role admin`. Whoever reaches the container console already controls
  the host.

## 5. Authorization

Panel roles (`admin`, `operator`, `viewer`; sqsolve has no collaborator role, and a collaborator
portal is a roadmap direction):

| Action | Viewer | Operator | Administrator |
|---|---|---|---|
| View status, libraries, backups, jobs, events | Yes | Yes | Yes |
| View connection details (without password) | Yes | Yes | Yes |
| Own API tokens | Yes | Yes | Yes |
| Reveal a library's password | No | No | Yes (audited) |
| Back up now, verify | No | Yes | Yes |
| Restore as a copy, download a backup | No | Yes | Yes |
| Cancel or retry a job (needs the permission of the operation the job runs) | No | Yes, for their operations | Yes |
| Restore by replacing, revert, recreate | No | No | Yes |
| Export projects and grades (experimental, if enabled) | No | Yes | Yes |
| Import, pin or delete a backup | No | No | Yes |
| Create, edit, maintenance, delete libraries | No | No | Yes |
| Credentials, network rules, end a session | No | No | Yes |
| Backup policies, notifications | No | No | Yes |
| Users, other people's tokens, system settings, diagnostics bundle | No | No | Yes |
| Audit log | No | Yes (read) | Yes |

The matrix lives in `internal/auth/policy.go` as a table (`RoleAllows`); an API token can only
narrow its owner's role with its scopes. `TestPolicyMatrix` checks every action for the
administrator, the viewer and an unknown role, and the operator's limits on a list of actions.

The detail of `SQS-E005` includes the PostgreSQL socket path and the `sqsolve_admin` role, and
any signed-in user can see it (`19-risks.md` §5 K-02); no secret is disclosed that way.

Authorization in Resolve: it exists only at the connection level. What can really be enforced:

| Mechanism | Enforceable | Note |
|---|---|---|
| Which credential enters which library | Yes | Roles and `pg_hba.conf` |
| From which networks | Yes | `pg_hba.conf`. PoC-08: the workstation's real IP is visible on the LAN with Docker on TrueNAS; through the TrueNAS Tailscale app every connection arrives from the container's address |
| How many connections | Yes | `CONNECTION LIMIT` |
| Temporary lock | Yes | Maintenance: `NOLOGIN` on the library roles (ADR 0024; what Resolve shows when it is refused is To be confirmed); before a rename: `ALLOW_CONNECTIONS` |
| Read only | No | sqsolve offers no read-only access; whether Resolve works with one was not evaluated (PoC-09 step 3 was not run) |
| Permissions per project, bin or timeline | DISCARDED | Resolve does not enforce it: any role that gets in can do everything inside the library |
| Collaborator identity in chat | DISCARDED | Each workstation defines it; it is not verifiable |

The UI offers none of the discarded items. To separate clients or productions, use separate
libraries.

## 6. Secrets

| Secret | Where | Who reads it | Rotation |
|---|---|---|---|
| `postgres` password | `db/sqsolve/superuser_password` (0440, owned by the sqsolve UID and the PostgreSQL group) | PostgreSQL (when initializing the cluster via `POSTGRES_PASSWORD_FILE`), sqsolve | Not implemented: there is no rotation command. The superuser only enters over the local socket |
| `sqsolve_admin` password | `app/secrets/pg_admin_password` (0400) | sqsolve | No automatic rotation. By hand: stop, delete the file and `docker compose up -d`; `sqsolve-init` generates a new one and sqsolve applies it with the superuser at start |
| Master key (32 bytes) | `app/secrets/master.key` (0400) | sqsolve | Not implemented: there is no command to re-encrypt the secrets with a new key; the recovery kit holds the current one |
| Webhook signing key | Encrypted in SQLite, one per channel | sqsolve | From the UI |
| Library passwords | Encrypted in SQLite | sqsolve | Rotation per library |
| TLS certificates | `db/sqsolve/tls` and `app/tls` | PostgreSQL, sqsolve | PostgreSQL: self-signed by `sqsolve-init` for 10 years, generated again when the key or the certificate is missing or they do not match; key 0600 of the PostgreSQL user, certificate readable so sqsolve shows its expiry. Panel: self-signed for 2 years and renewed by sqsolve when fewer than 30 days remain (at start and daily, without a restart); an uploaded certificate is never replaced, sqsolve warns 30 days before and at Error severity in the last 14 days |

Modes are enforced, not only documented. sqsolve refuses to start (`SQS-C010`, wrapped in
`SQS-C100` or `SQS-C013`) when the master key or the `sqsolve_admin` password can be read by its
group or by others, or written by anyone but its owner (at most 0600); the `postgres` password is
the one secret shared with a group, and may be at most 0640 (`SQS-C014`). The doctor checks
(`sqsolve doctor` and the Health page) apply the same rule to the master key and the
`sqsolve_admin` password. `sqsolve-init` stops with `SQS-C085` when it cannot set the mode of an
existing secret, of a TLS key or certificate, or of the folders of the app volume and of the
PostgreSQL configuration that hold them, including when an ACL on the volume keeps a wider mode;
a secret file it has just created is written with its mode but that mode is not checked
afterwards (`19-risks.md` §5 K-05). On the backups volume and the db volume root, which hold no
secret of sqsolve, it only warns, so a NAS share that refuses a mode does not stop PostgreSQL.
The TLS key and certificate pairs are written so that a power cut never leaves a key next to a
certificate of another pair (`fsx.WritePair`: each step is synced before the next).

Encryption of recoverable secrets: XChaCha20-Poly1305 with a random 24-byte nonce; the key
identifier and the column name go as additional authenticated data so that a value cannot be
moved to another column.

Recovery kit: file `sqsolve-recovery-<installation>.txt` that the wizard forces you to download,
with the master key in base64, the fingerprint of the panel certificate, the version and the
recovery steps. It can be downloaded again from Settings. Without it, libraries remain safe but
their passwords must be rotated.

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
  start of sqsolve) validates and reloads it again even though the file did not change. The
  rewrite that follows a settings change ("TLS only", access to the maintenance database) or the
  addition of a member has no audit entry of its own (`19-risks.md` §5 K-07).

## 8. Hardening of the sqsolve container

The `sqsolve` service in `deploy/compose.yaml`:

```yaml
user: "1000:1000"          # must match SQSOLVE_UID and SQSOLVE_GID in x-env
read_only: true
tmpfs:
  - /tmp:size=64m,mode=1777
init: true                 # reaps orphaned pg_dump and pg_restore processes
cap_drop: [ALL]
security_opt: ["no-new-privileges:true"]
```

`pg_dump` writes directly to the backups volume, never to `/tmp`. No service mounts the Docker
socket. The PostgreSQL container uses the official image pinned by digest and runs as
`user: "999:999"` (the image's `postgres` user; `sqsolve-init` prepares the data folder for it,
as TrueNAS apps do), so it never runs as root, with `cap_drop: [ALL]` and `no-new-privileges`.
`sqsolve-init` runs as root, because it changes owners and modes on the volumes, but with
`network_mode: none`, `read_only: true`, `cap_drop: [ALL]`, only `cap_add: [CHOWN,
DAC_OVERRIDE, FOWNER, FSETID]` and `no-new-privileges`, and exits once the volumes are ready.

## 9. Audit log

Every action that changes state or exposes secrets is recorded: logins and failed logins, user
changes, library creation and changes, password reveals, rotations, network rules, manual
backups, restores, reverts, deletions, backup policy changes, settings changes, and the console
commands that change state (actor `cli`). The superuser connections of `pg.Bootstrap` and the
doctor checks are not recorded.

Format of each entry: `seq`, date, actor (type, id, IP), action, target, result, details without
secrets, `prev_hash` and `hash = SHA-256(prev_hash || canonical JSON of the entry)`. Verification
walks the chain; a break raises the critical event `audit.chain_broken`.

The entry of a sensitive action is written with it or before it, never after (ADR 0026): if it
cannot be written, the action is not done and the caller gets `SQS-X040` (503 in the API).

| Kind of action | When the entry is written | Results |
|---|---|---|
| Change inside SQLite (users, roles, panel passwords, API tokens, settings, pin or unpin a backup, unlock a user, turn the second factor on or off, notification channels, remove a blocked backup row from the catalog) | In the same transaction (`RecordTx`) | `ok` |
| Action that runs as a job (library create, delete, purge, untrash; back up now; verify a backup; restores, revert, recreate; import; export preparation; maintenance jobs, also from the console; Retry; upgrade window) | In the transaction that queues the job, with `job_id` (`jobs.Audited`) | `accepted`, then `ok` or `failed` (with `state` and `error_code`) when the job ends |
| Change in PostgreSQL, in files or in several steps (delete a backup, rotate a password, add or revoke a member, network rules, maintenance, end a session, reopen the initialization window, close an export workspace, regenerate `pg_hba.conf`, edit a library, change a backup policy, install a panel certificate, cancel a job) | Before the change; the change does not run without it (`Audit.Do`) | `accepted`, then `ok` or `failed` (with `error`); a result that cannot be written after a successful change is `SQS-X041` |
| Disclosure (reveal a password, download a backup, the recovery kit, the diagnostics, an export) | Before anything is sent (`Audit.Require`) | `ok` |

Recorded on a best-effort basis, with a log line when the entry cannot be written (no result is
ignored silently): logins, failed logins, the account lock (`auth.locked`), the use of a recovery
code at login, logouts, refusals (`denied`), the summary entries of the setup wizard
(`setup.complete`, `setup.recover`; the user and the settings it creates have their own entries),
and what sqsolve does on its own at start or on a timer (`system.upgrade`, `tls.panel_renewed`).
Refusing a login because the audit log cannot be written would lock everyone out of the panel
that shows the problem, and a start or a certificate renewal that stopped on it would turn a disk
problem into an outage. Accepting a new PostgreSQL cluster (`postgres.cluster_accepted`) is
stored with its entry or not at all, and is tried again at the next start; its event of the same
name is emitted even when that transaction fails (`19-risks.md` §5 K-06).

The rewrites of `pg_hba.conf` that follow a settings change or the addition of a member have no
entry of their own (`19-risks.md` §5 K-07); the change that caused them has its entry.

The inventory above was checked against every call of the audit log on 2026-10-25 and against
the code of 1.2.2 on 2026-10-26;
`TestCatalogChangesNeedTheirAuditEntry`, `TestUnlockAndTOTPNeedTheirAuditEntry`,
`TestChannelChangesNeedTheirAuditEntry`, `TestMemberAddNeedsItsAuditEntry` and
`TestJobCancelNeedsItsAuditEntry` inject the failure.

## 10. Incident response and disclosure

- `SECURITY.md`: reports go through GitHub's private vulnerability reporting (no contact
  email); response within 7 days; the fix ships in a patch version of the current series with a
  notice in the release notes; coordinated disclosure at 90 days, or earlier if the fix is
  already published.
- Leaked library credential: rotate the affected library's password, review its sessions and
  the audit log, review its network rules.
- Compromised server: isolate it, keep the volumes, reinstall from scratch, restore from verified
  backups prior to the incident and rotate every credential. sqsolve has no command to change the
  master key (§6).

## 11. Security review of 1.0 (2026-10-19)

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
account lock. Open findings: none. CI does not run ZAP.
