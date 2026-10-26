# 10. API and contracts

## 1. REST API conventions

- Base: `https://<server>:9432/api/v1` (the port of `SQSOLVE_LISTEN`). JSON UTF-8. sqsolve has no
  OpenAPI file: this document and the handler tests in `internal/api` are the reference.
- Authentication: `Authorization: Bearer sqs_...` (tokens with scope `read`, `backup`,
  `restore`, `export` or `admin`, created in the panel under My account) or a session cookie plus
  the `X-CSRF-Token` header on every request other than `GET` and `HEAD` (browser use). Without
  valid credentials the answer is `401` with `WWW-Authenticate: Bearer realm="sqsolve"`:
  `SQS-A024` for an invalid, expired or revoked token, `SQS-A004` for a missing or expired
  session, `SQS-A031` for a cookie without `X-CSRF-Token`. A role or scope that does not allow
  the action gets `403` with `SQS-A030` (§2). With a session cookie, an administrator who has no
  second factor while the installation requires one gets `403` with `SQS-A051` until they enable
  it in the panel; tokens are not affected. A login lock (`SQS-A004`) does not stop tokens or open
  sessions (ADR 0025). `SQS-A050` (too many passwords being checked at once) answers `503` with
  `Retry-After: 5`.
- Errors: RFC 9457 `application/problem+json`. `type` is the relative path
  `/docs/errors/<code>` (`about:blank` when the error has no code), `title` is the HTTP status
  text, `detail` is the whole message with its code and `instance` the request path:

```json
{
  "type": "/docs/errors/SQS-B003",
  "title": "Unprocessable Entity",
  "status": 422,
  "detail": "SQS-B003 cannot back up a library in state In the trash",
  "code": "SQS-B003",
  "instance": "/api/v1/libraries/01JB.../backups"
}
```

  Status codes: `400` for a body that cannot be read, `404` for an unknown library, backup or
  job, `409` for a job that cannot be cancelled, a backup with an invalid path or a job conflict
  (`SQS-J005`), `503` when the audit log or PostgreSQL is unavailable, `507` for `SQS-B012`, and
  `422` for the other refusals of the services. Any other path under `/api/` answers `404` with
  `SQS-C062`.
- Long operations: `202 Accepted` with the job (alone, or next to the library or export workspace
  it creates). `POST /libraries` and `POST /libraries/{id}/backups` also set
  `Location: /api/v1/jobs/{id}`.
- Idempotency: sqsolve does not support `Idempotency-Key`. A revert or recreate repeated from the
  panel returns the job already queued for that library.
- Optimistic concurrency: sqsolve uses neither `ETag` nor `If-Match`.
- Pagination by cursor, newest first: `GET /backups` and `GET /libraries/{id}/backups` take
  `limit` (up to 500, default 200) and `cursor`, and answer `next_cursor` when the page is full;
  `GET /jobs` pages by 50 with `cursor` and `next_cursor`; `GET /events` (100 per page) and
  `GET /audit` (200 per page) take `cursor` (the last `id`, or the last `seq` for the audit log)
  and answer no `next_cursor`.
- Lists answer `{"items": [...]}`. Those read from the job, event and audit stores
  (`GET /jobs`, `GET /jobs/{id}/logs`, the `steps` of `GET /jobs/{id}`, `GET /events`,
  `GET /audit`) are `null` instead of `[]` when empty, and so is `attention` in
  `GET /system/status`; `GET /jobs/{id}/logs` answers `200` with `null` items for a job that
  does not exist (K-01 in `19-risks.md` §5).
- Dates RFC 3339 in UTC. Sizes in bytes. Identifiers are ULIDs, except the `seq` of audit
  entries, job steps and log lines.
- Dangerous operations require `"confirm": "<slug>"` in the body and, with a token, the scope
  `admin` or `restore`. There is no reauthentication (ADR 0021).
- Request bodies hold exactly one JSON object: an empty body, unknown fields, or anything after
  the object (a second value, stray text) are refused with `400` and `SQS-C061`, so an empty
  request to `POST /libraries/{id}/maintenance` never means `"enabled": false`. Bodies are
  limited to 1 MiB. `POST /libraries/{id}/backups`, `POST /backups/{id}/verify` and `POST` and
  `DELETE /backups/{id}/export` read no body.
- An action whose audit entry cannot be written is not done: `503` with `SQS-X040` (ADR 0026).
  Actions that queue a job record it as `accepted` with the job's id, and the job's result when
  it ends.

## 2. Resources

Each route needs a permission of `internal/auth` (`08-security.md` §5). The role allows it
(viewer: `view` and own tokens; operator: also `backup.create`, `backup.verify`,
`backup.download`, `restore.new`, `audit.view`, `job.cancel` and `library.export`;
administrator: every permission) and, with a token, so must one of its scopes (`read`: `view`;
`backup`: also `backup.create` and `backup.verify`; `restore`: also `backup.download`,
`restore.new`, `restore.replace` and `restore.revert`; `export`: also `library.export`; `admin`:
every permission).

| Method and path | Permission | Minimum role | Token scope | Description |
|---|---|---|---|---|
| `GET /system/health` | none | no authentication | none | `200` or `503` with states and counts only: PostgreSQL, volumes, backup freshness (for uptime monitors; fields in `13-observability.md` §3) |
| `GET /system/status` | `view` | viewer | any | What the dashboard shows: `postgres` (`up`, `version`, `connections`, `max_connections`, `error`), `volumes` (name, path, total, free, used percent, error), `jobs` (`queued`, `running`, `failed_24h`), `libraries` (`total`, `active`) and `attention` (`severity`, `message`) |
| `GET /system/version` | `view` | viewer | any | `sqsolve`, `commit`, `postgres` (server version) and `instance_id` |
| `GET /libraries` | `view` | viewer | any | List; `?state=` filters by one state |
| `POST /libraries` | `library.create` | administrator | `admin` | `{ "display_name": "...", "slug": "...", "strategy": "resolve", "notes": "..." }`, the slug suggested from the name when empty; `202` with `library` and `job` |
| `GET /libraries/{id}` | `view` | viewer | any | Detail |
| `PATCH /libraries/{id}` | `library.edit` | administrator | `admin` | `display_name`, `notes`, `resolve_version`, `connection_limit`; returns the library |
| `DELETE /libraries/{id}` | `library.delete` | administrator | `admin` | `{ "confirm": "<slug>" }`; to the trash; `202` with the job |
| `POST /libraries/{id}/maintenance` | `library.maintenance` | administrator | `admin` | `{ "enabled": true, "terminate_sessions": false }`; returns `maintenance` and `terminated_sessions` |
| `GET /libraries/{id}/connection` | `view` | viewer | any | `library_name`, `host`, `port`, `database`, `user`; no password. `?credential=<id>` for a per-person credential |
| `POST /libraries/{id}/connection/reveal` | `library.reveal` | administrator | `admin` | The same plus `password` (also `?credential=`); audited, emits `library.credential_revealed` |
| `GET /libraries/{id}/sessions` | `view` | viewer | any | Active sessions: `pid`, `user`, `client_addr`, `application`, `started_at`, `state`, `ssl`, `tls_version` |
| `GET /libraries/{id}/backup-policy` | `view` | viewer | any | Policy: `enabled`, `mode` (`cron` or `activity`), `check_minutes` (5, 10, 15, 30, 60), `cron`, `timezone`, `keep_last`, `keep_daily`, `keep_weekly`, `keep_monthly`, `min_verified`; `last_check_at` and `last_check_same` read-only (ADR 0022) |
| `PUT /libraries/{id}/backup-policy` | `policy.edit` | administrator | `admin` | The same object; returns it |
| `GET /libraries/{id}/backups` | `view` | viewer | any | The library's catalog; `status`, `limit`, `cursor` |
| `POST /libraries/{id}/backups` | `backup.create` | operator | `backup` | Back up now; `202` with the job |
| `GET /libraries/{id}/projects` | `view` | viewer | any | Projects with folder and date, and the `schema` state (experimental, `22`) |
| `GET /libraries/{id}/projects/{pid}/export` | `library.export` | operator | `export` | Download of the `.drp` (experimental) |
| `GET /libraries/{id}/grades` | `view` | viewer | any | Gallery albums with their stills (experimental) |
| `GET /libraries/{id}/grades/{sid}/export` | `library.export` | operator | `export` | Download of a still's `.drx` (experimental) |
| `GET /libraries/{id}/grades/albums/{vid}/export` | `library.export` | operator | `export` | Zip with an album's `.drx` files (experimental) |
| `GET /backups` | `view` | viewer | any | Global catalog; `library_id`, `status`, `limit`, `cursor` |
| `GET /backups/{id}` | `view` | viewer | any | Detail: kind, status, verification, size, SHA-256, `pg_dump` version, fingerprint, pin, dates, error |
| `GET /backups/{id}/download` | `backup.download` | operator | `restore` | The file, with `Content-Disposition`, `X-Sqsolve-SHA256` and range requests; audited; `404` unless the backup is available |
| `POST /backups/{id}/verify` | `backup.verify` | operator | `backup` | Test restore (L2); `202` with the job |
| `POST /backups/{id}/restore` | `restore.new`; `restore.replace` to replace | operator; administrator to replace | `restore` | `{ "mode": "new", "slug": "...", "display_name": "..." }` (`202` with `library` and `job`) or `{ "mode": "replace", "confirm": "<slug>", "terminate_sessions": false }` (`202` with the job); another mode: `400` with `SQS-C063` |
| `GET /backups/{id}/export` | `view` | viewer | any | The backup's export workspace and, when `ready`, its `projects`, `albums` and `schema` (experimental, `22` §7) |
| `POST /backups/{id}/export` | `library.export` | operator | `export` | Restore the backup into a temporary database to export from it; `202` with `workspace` and `job` |
| `DELETE /backups/{id}/export` | `library.export` | operator | `export` | Close the workspace and drop its temporary database; `204` |
| `GET /backups/{id}/export/projects/{pid}`, `.../export/grades/{sid}`, `.../export/grades/albums/{vid}` | `library.export` | operator | `export` | Downloads from the workspace, as for a library |
| `GET /jobs` | `view` | viewer | any | Filters `state`, `type`, `library_id`; `cursor` |
| `GET /jobs/{id}` | `view` | viewer | any | `{ "job": {...}, "steps": [...] }` (§3) |
| `GET /jobs/{id}/logs` | `view` | viewer | any | Lines with `seq`, `ts`, `level`, `message`; `?after=<seq>` returns the ones after it (up to 5000) |
| `POST /jobs/{id}/cancel` | `job.cancel` and the job type's | operator | `admin` | Cancel; `202` with `{ "status": "cancel_requested" }`, `409` if it cannot be cancelled. The type's permission is the one that starts its operation (`restore.replace` for a restore by replacing, `settings.edit` for system jobs); without it, `403` with `SQS-A030` and a `denied` entry in the audit log |
| `GET /events` | `view` | viewer | any | Filters `library_id`, `min_severity`, `type` (prefix); `cursor` |
| `GET /audit` | `audit.view` | operator | `admin` | Audit log; filter `target_id`; `cursor` |

The experimental routes answer `409` with `SQS-E002` while the setting is off. A library that is
not active or in maintenance, or that sqsolve cannot open, answers `503` with `SQS-E005`; when the
connection fails, that detail names the PostgreSQL socket and the `sqsolve_admin` role (K-02 in
`19-risks.md` §5). Downloads carry `X-Sqsolve-Name` (the suggested file name),
`X-Sqsolve-Experimental: true` and, when there are any, `X-Sqsolve-Notes`.

The API has no route for the rest, which only the panel does (`09-web-ui.md`): reopening the
initialization window, untrash and purge, revert and recreate, credentials and network rules,
ending a session, pinning, deleting and importing backups, retrying a job, live progress (the
panel's `GET /jobs/{id}/events` stream, for its own pages and its session), users, tokens,
notification channels, settings, maintenance actions and the Resolve upgrade window, the audit
log export (`/activity/audit.jsonl`), the recovery kit and the diagnostic bundle. sqsolve has no
`/metrics` endpoint (`13-observability.md` §5).

## 3. Job object

`GET /jobs/{id}` answers the job and its steps; lists and `202` answers carry the job alone:

```json
{
  "job": {
    "id": "01JB7Y6Z3K9M2Q8R4T5V6W7X8Y",
    "type": "backup.create",
    "title": "Back up",
    "library_id": "01JB7Y...",
    "params": { "target": "01JB7Y...", "kind": "scheduled" },
    "state": "running",
    "priority": 50,
    "scheduled_for": "2026-10-18T07:00:00Z",
    "created_at": "2026-10-18T07:00:00Z",
    "started_at": "2026-10-18T07:00:01Z",
    "progress": 40,
    "current_step": "dump",
    "created_by": { "type": "schedule", "id": "01JB...", "name": "schedule" },
    "schedule_id": "01JB...",
    "attempt": 1,
    "result": {},
    "cancellable": true,
    "retryable": true
  },
  "steps": [
    { "seq": 1, "name": "prepare", "state": "succeeded", "started_at": "...", "finished_at": "...", "data": {} },
    { "seq": 2, "name": "dump", "state": "running", "started_at": "...", "data": {} }
  ]
}
```

A finished job adds `finished_at` and, when it did not succeed, `error_code` and `error_detail`.
States: `queued`, `running`, `succeeded`, `failed`, `cancelled`, `interrupted`.

Job types: `library.create`, `library.delete`, `library.untrash`, `library.purge`,
`backup.create`, `backup.verify`, `backup.import`, `backup.export`, `restore.new`,
`restore.replace`, `restore.revert`, `restore.recreate`, `system.retention`,
`system.verify_all`, `system.vacuum`, `system.control_backup`, `system.catalog_check`,
`system.catalog_rebuild`, `system.purge`, `system.housekeeping`, `system.upgrade_window`.

## 4. Event catalog

| Type | Severity | When |
|---|---|---|
| `library.created`, `library.activated`, `library.restored_from_trash` | info | Lifecycle |
| `library.trashed`, `library.purged` | warning | Lifecycle: moved to the trash, purged for good |
| `library.extra_database` | warning | The library role created a database with another name (the name typed in Resolve was not the identifier) |
| `library.init_expired` | warning | The window expired |
| `library.schema_changed` | warning | The fingerprint changed |
| `library.maintenance_on`, `library.maintenance_off` | info | Locks |
| `library.missing` | critical, warning | Critical: reconciliation found no usable database under the library's name (none, or an invalid one). Warning: a database with the name and owner of a `missing` library is on the server again but was not adopted, because it has no tables while the library had some or it could not be inspected; the message says why and that the database is kept (docs 11 §7.7) |
| `library.found` | info | The database of a `missing` library is on the server again with its owner and has tables (or the library never had any); the library is active |
| `library.prev_kept` | warning | A previous state (`__prev`) was not dropped because no verified backup of it exists or could be taken (`SQS-R055`), or because the journal of a job still pending reconciliation names it (`SQS-R090`) or those jobs could not be read (`SQS-R091`) |
| `library.reconcile_pending` | warning | At a start, reconciliation could not settle an interrupted restore, revert, delete or recreate (a check failed, a rename or a catalog write was refused, or no rule covers the state of the swap). The message says why, that the library's state and access are left as they are and which databases its journal names are kept; every start tries again until it is settled (docs 11 §7.3, §8). Without a library: the jobs pending reconciliation could not be read, so every `__restore` database is kept, libraries left restoring stay restoring and no previous state is forgotten |
| `library.credential_rotated`, `library.credential_revealed` | info | Credentials |
| `backup.started`, `backup.succeeded` | info | Backups; a backup cut by a cancel or a graceful stop emits no `backup.failed` |
| `backup.verified` | info | A test restore (L2) matched the manifest |
| `backup.failed`, `backup.verify_failed`, `backup.missed` | error | Backup problems |
| `backup.verify_skipped` | warning | A verification could not run (no space, cancelled, server or tool unavailable); the backup keeps its verification status |
| `backup.import_source_kept` | warning | An import asked to delete its source kept it: the file in `_import/` changed or was replaced during the import |
| `backup.stale` | warning | Library without a verified backup for more than the threshold (48 h by default, Settings) |
| `backup.expired` | info | Retention deleted a backup |
| `backup.catalog_mismatch` | warning | The catalog check found backup files missing from the volume or files not in the catalog |
| `backup.path_invalid` | critical | At a start or in the catalog check, catalog rows whose path is outside the backups volume or not valid; sqsolve does not read, restore or delete them until an administrator reviews them (`data.backup_ids`, `runbooks/invalid-backup-path.md`) |
| `restore.started`, `restore.succeeded`, `restore.reverted` | info | Restore |
| `restore.failed` | error | Failed restore (the active library was not touched or was reverted); critical when a swap stopped halfway (`SQS-R053`) or its reopening failed (`SQS-R054`) |
| `storage.low` | warning at 80 %, error at 90 % | Space; `data.volume` is `db`, `app` or `backups` |
| `storage.volume_missing` | critical | The backups volume marker is missing, or a volume cannot be measured (`data.volume` is `db`, `app` or `backups`); also when a backup finds the backups volume unmounted or foreign (with the library, without `data.volume`) |
| `postgres.down`, `postgres.up` | critical, info | Availability: first failed check after it answered, or no answer in the 5 minutes after sqsolve started; `up` only after a `down`, and it ends the grouping window of `down` on every channel |
| `postgres.cluster_changed` | critical | The cluster's system identifier is not the stored one; sqsolve holds (ADR 0023) |
| `postgres.cluster_accepted` | warning | `SQSOLVE_PG_ACCEPT_SYSTEM_ID` accepted a new cluster |
| `auth.login_failed_burst` | warning | Probable brute force (`data.ip`, `data.username`) |
| `auth.locked` | critical | An account was locked for 15 minutes after 10 wrong passwords in a row (ADR 0025; `data.username`, `data.ip`) |
| `audit.chain_broken` | critical | Audit log integrity (daily housekeeping) |
| `tls.expiring` | warning, error | Certificate expires in less than 30 days; error in the last 14 days, once expired, or when renewing the panel's fails (`data.cert` is `Panel web` or `PostgreSQL`) |
| `job.interrupted` | warning | Job cut off by a restart or by a graceful stop (`data.job_type`) |
| `job.failed` | error | Failed job (`data.code`, `data.job_type`) |
| `system.started`, `system.upgraded` | info | sqsolve started; it started with a version other than the last one (the message names both) |
| `system.internal_error` | critical | A background loop (job dispatcher, scheduler) recovered from an internal error and keeps running (`data.loop`) |

## 5. Outgoing webhooks

- `POST` to the channel URL with `Content-Type: application/json` and `User-Agent: sqsolve`.
- Headers: `X-Sqsolve-Event` (type), `X-Sqsolve-Delivery` (a new ULID on every attempt),
  `X-Sqsolve-Timestamp` (Unix seconds) and, when the channel has a secret,
  `X-Sqsolve-Signature: sha256=<hex>` with HMAC-SHA256 over `timestamp + "." + body` and that
  secret.
- Body: the event, with the installation name as `instance`; `library_id`, `job_id` and `data`
  only when the event has them, and `links` to the panel only when the address workstations use
  is set:

```json
{
  "id": "01JB...",
  "type": "backup.failed",
  "severity": "error",
  "occurred_at": "2026-10-18T07:00:12Z",
  "instance": "main-studio",
  "library_id": "01JB...",
  "job_id": "01JB...",
  "message": "Backup of \"Client X\" failed: SQS-B012 not enough space on the backups volume: 3.1 GB estimated, 1.2 GB free",
  "links": { "job": "https://nas.local:9432/jobs/01JB...", "library": "https://nas.local:9432/libraries/01JB..." }
}
```

- Retries after 10 s, 1 min, 10 min and 1 h; then the error is stored on the channel. Each
  attempt waits at most 10 s for the answer. Redirects are not followed: a `3xx` answer is a
  failure. During a graceful stop a delivery waiting for a retry makes one last attempt at once.
- The error stored for a failing channel keeps the cause and the HTTP status, never the URL (Slack,
  Discord and ntfy URLs carry tokens) nor a secret field: `SQS-N040 could not reach the
  destination: ...` or `SQS-N002 the destination responded 403 Forbidden`. Only users with
  `notify.manage` see it in the panel.
- Grouping (docs 11 §13): a summary delivery is the latest held event with the message
  `N more like this. Latest: ...` and `"suppressed_since_last": N` in the body.
- The panel's "Test" button sends an event of type `system.test` (info) to that channel only; it
  is not recorded.
- "Before" events (`backup.started`, `restore.started`) are sent but do not block the operation;
  nothing can block it from outside (§6).

## 6. Hooks

sqsolve runs no hooks or other user code: there is no `hooks` volume, no `sqsolve-hooks`
container and nothing implements the hook contract of ADR 0011. Isolated hooks are a long-term
direction in `20-roadmap.md` §2.

## 7. CLI

The same binary, for recovery and local automation:

```
sqsolve serve                          # starts the panel and the service
sqsolve init                           # prepares volumes and secrets (sqsolve-init container)
sqsolve healthcheck                    # GET /healthz, for the Docker healthcheck
sqsolve doctor [--json]                # checks volumes, secrets, PostgreSQL and the catalog (13 §7)
sqsolve version
sqsolve admin list
sqsolve admin create <user> [--role admin|operator|viewer]
sqsolve admin reset-password <user>
sqsolve admin disable-totp <user>
sqsolve admin unlock <user>
sqsolve library list
sqsolve backup create <identifier> [--wait]
sqsolve backup list <identifier>
sqsolve restore <backup-id> --new <identifier> [--name <name>] [--wait]
sqsolve restore <backup-id> --replace --confirm <identifier> [--terminate] [--wait]
sqsolve catalog rebuild [--wait]
sqsolve catalog forget <backup-id>     # removes a blocked row (invalid path) after review
sqsolve audit verify
```

`admin create` defaults to the `admin` role; it and `reset-password` read the password from the
terminal without echo. `doctor` exits with an error when a check fails. There are no commands to
rotate the master key or the PostgreSQL superuser password (`08-security.md` §6).

`backup create`, `restore` and `catalog rebuild` queue a job in the same database the server uses;
the running server executes it, with the same job locks, and `--wait` follows it until it ends.
Console actions go through the same services and are recorded in the audit log with actor type
`cli`, shown as "console".

## 8. Internal formats

### 8.1 Backup manifest

File `<name>.manifest.json` next to each backup. JSON schema in
`api/schemas/manifest.v1.json`, checked against the Go type by
`TestManifestSchemaMatchesTheGoType`; `ReadManifest` refuses a manifest whose required fields are
not valid (`SQS-B042`).

```json
{
  "manifest_version": 1,
  "backup_id": "01JB...",
  "library": { "id": "01JB...", "slug": "client_x", "display_name": "Client X" },
  "kind": "scheduled",
  "file": "client_x_20261018T070000Z_01JB.backup",
  "size_bytes": 184467221,
  "sha256": "9f2c...",
  "format": { "tool": "pg_dump", "tool_version": "18.6", "format": "custom", "compression": "gzip:6", "archive_version": "<read from the header>" },
  "server": { "postgres_version": "18.6", "database_size_bytes": 1048576000, "activity_mark": "..." },
  "schema": { "fingerprint": "sha256:51ab...", "tables": 214 },
  "resolve_version_declared": "21.1.1",
  "counts_exact": true,
  "row_counts": { "table_a": 120, "table_b": 98431 },
  "started_at": "2026-10-18T07:00:00Z",
  "finished_at": "2026-10-18T07:00:41Z",
  "sqsolve_version": "1.0.0",
  "instance_id": "01JA..."
}
```

`archive_version` is read from the file header with `pg_restore --list`. The table names in
`row_counts` are catalog metadata; they contain no project data.

### 8.2 Restore step journal

Each `job_steps` row of `restore.replace` stores in `data` the state needed to decide at
startup: database names involved, whether the rename was issued and whether it was confirmed.
See `11-backups-and-automation.md` §7.3.

## 9. Versioning

- The `v1` API only changes in compatible ways: new optional fields, new endpoints. An
  incompatible change creates `v2` and `v1` is kept for at least two minor sqsolve versions.
- The manifest carries `manifest_version`; sqsolve reads all earlier versions.
- Events and webhooks add fields without removing or renaming any.
