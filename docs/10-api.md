# 10. API and contracts

## 1. REST API conventions

- Base: `https://<server>:9432/api/v1`. JSON UTF-8. The OpenAPI 3.1 contract
  (`api/openapi.yaml`, validated in tests, SQS-410) is not written yet; until it is, this
  document and the handler tests in `internal/api` are the reference.
- Authentication: `Authorization: Bearer sqs_...` (tokens with scope `read`, `backup`,
  `restore`, `export` or `admin`) or a session cookie plus the `X-CSRF-Token` header (browser
  use). With a session cookie, an administrator who has no second factor while the
  installation requires one gets `403` with `SQS-A051` until they enable it in the panel;
  tokens are not affected. A login lock (`SQS-A004`) does not stop tokens or open sessions
  (ADR 0025). `SQS-A050` (too many passwords being checked at once) answers `503` with
  `Retry-After`.
- Errors: RFC 9457 `application/problem+json`:

```json
{
  "type": "https://sqsolve.example/errors/SQS-B012",
  "title": "Insufficient space on the backups volume",
  "status": 507,
  "detail": "3.1 GB are needed and 1.2 GB are free on /backups.",
  "code": "SQS-B012",
  "instance": "/api/v1/libraries/01JB.../backups"
}
```

  The `type` domain is to be confirmed; meanwhile the relative path
  `/docs/errors/SQS-B012` is used.
- Long operations: `202 Accepted` with `Location: /api/v1/jobs/{id}` and the job object.
- Idempotency (planned, not implemented): `POST`s that create or launch would accept
  `Idempotency-Key`, and the same key within 24 h would return the same response. Today a repeated
  revert or recreate of the same library returns the job already queued.
- Optimistic concurrency (planned, not implemented): editable resources would return `ETag` and
  `PATCH` would require `If-Match`.
- Cursor pagination: `?limit=50&cursor=...`, response with `next_cursor`.
- Dates RFC 3339 in UTC. Sizes in bytes. Identifiers are ULIDs.
- Dangerous operations require `"confirm": "<slug>"` in the body and, with a token, the scope
  `admin` or `restore`. There is no reauthentication (ADR 0021).
- Request bodies hold exactly one JSON object: an empty body, unknown fields, or anything after
  the object (a second value, stray text) are refused with `400` and `SQS-C061`, so an empty
  request to `POST /libraries/{id}/maintenance` never means `"enabled": false`. Bodies are
  limited to 1 MiB.
- An action whose audit entry cannot be written is not done: `503` with `SQS-X040` (ADR 0026).
  Actions that queue a job record it as `accepted` with the job's id, and the job's result when
  it ends.

## 2. Resources

| Method and path | Minimum scope | Description |
|---|---|---|
| `GET /system/status` | `read` | Summary status (what the dashboard shows) |
| `GET /system/health` | no authentication | `200` or `503` with states and counts only: PostgreSQL, volumes, backup freshness (for uptime monitors; fields in `13-observability.md` §3) |
| `GET /system/version` | `read` | sqsolve and PostgreSQL versions, compatibility matrix |
| `GET /libraries` | `read` | List with `state` filters |
| `POST /libraries` | `admin` | Create; returns a job |
| `GET /libraries/{id}` | `read` | Detail |
| `PATCH /libraries/{id}` | `admin` | Display name, notes, limit, declared version |
| `POST /libraries/{id}/init-window` | `admin` | Reopen the initialization window |
| `POST /libraries/{id}/maintenance` | `admin` | `{ "enabled": true, "terminate_sessions": false }` |
| `DELETE /libraries/{id}` | `admin` + confirmation | To the trash; returns a job |
| `POST /libraries/{id}/restore-from-trash` | `admin` | Untrash |
| `GET /libraries/{id}/connection` | `read` | Location, database, user; no password |
| `POST /libraries/{id}/connection/reveal` | `admin` | Password; audited |
| `GET` and `PUT /libraries/{id}/backup-policy` | `read` and `admin` | Policy: `mode` (`cron` or `activity`), `check_minutes` (5, 10, 15, 30, 60), `cron`, retention; `last_check_at` and `last_check_same` read-only (ADR 0022) |
| `GET /libraries/{id}/resolvedbkey` | `admin` | Key file (if the module is active) |
| `GET /libraries/{id}/credentials` | `admin` | List without passwords |
| `POST /libraries/{id}/credentials` | `admin` | Create a per-person credential (1.1) |
| `POST /libraries/{id}/credentials/{cid}/rotate` | `admin` | Rotate |
| `POST /libraries/{id}/credentials/{cid}/revoke` | `admin` | Revoke (1.1) |
| `GET/PUT /libraries/{id}/network-rules` | `admin` | CIDR rules (1.1) |
| `GET /libraries/{id}/sessions` | `read` | Active sessions |
| `DELETE /libraries/{id}/sessions/{pid}` | `admin` | Terminate a session |
| `GET/PUT /libraries/{id}/backup-policy` | `read` / `admin` | Schedule and retention |
| `GET /libraries/{id}/backup-policy/preview` | `read` | Next runs and effect of retention |
| `GET /libraries/{id}/backups` | `read` | The library's catalog |
| `GET /libraries/{id}/projects` | `read` | Projects with folder and date (experimental, `22`) |
| `GET /libraries/{id}/projects/{pid}/export` | `export` | Download of the `.drp` (experimental) |
| `GET /libraries/{id}/grades` | `read` | Gallery albums with their stills (experimental) |
| `GET /libraries/{id}/grades/{sid}/export` | `export` | Download of a still's `.drx` (experimental) |
| `GET /libraries/{id}/grades/albums/{vid}/export` | `export` | Zip with an album's `.drx` files (experimental) |
| `POST /libraries/{id}/backups` | `backup` | Back up now; returns a job |
| `GET /backups` | `read` | Global catalog |
| `GET /backups/{id}` | `read` | Detail with manifest |
| `GET /backups/{id}/download` | `restore` | Download of the file (`Content-Disposition`) |
| `POST /backups/{id}/verify` | `backup` | `{ "level": 2 }`; returns a job |
| `POST /backups/{id}/pin` / `unpin` | `admin` | Pin or unpin |
| `POST /backups/{id}/restore` | `restore` or `admin` | `{ "mode": "new", "slug": "...", "display_name": "..." }` or `{ "mode": "replace", "confirm": "<slug>", "terminate_sessions": false }` |
| `POST /backups/import` | `admin` | `multipart/form-data` upload or `{ "path": "_import/file.backup", "library_slug": "..." }` |
| `POST /libraries/{id}/revert` | `admin` + confirmation | Revert the last swap within the grace period |
| `GET /jobs` | `read` | Filters `state`, `type`, `library_id` |
| `GET /jobs/{id}` | `read` | Detail with steps |
| `GET /jobs/{id}/logs` | `read` | Paginated log |
| `GET /jobs/{id}/events` | `read` | Progress SSE |
| `POST /jobs/{id}/cancel` | by type | Cancel. Needs `job.cancel` and the permission that starts the job's type (the one of its page: `restore.replace` for a restore by replacing, `admin` for system jobs); otherwise `403` with `SQS-A030` and a `denied` entry in the audit log |
| `GET /events` | `read` | Events with filters |
| `GET /audit` | `admin` | Audit log; `?format=jsonl` to export |
| `GET /audit/verify` | `admin` | Verify the chain |
| `GET/POST/PATCH/DELETE /users[...]` | `admin` | Panel users |
| `GET/POST/DELETE /tokens[...]` | own or `admin` | Tokens |
| `GET/POST/PATCH/DELETE /notifications/channels[...]` | `admin` | Channels |
| `POST /notifications/channels/{id}/test` | `admin` | Test send |
| `GET/PUT /notifications/rules` | `admin` | Rules |
| `GET/PATCH /settings` | `admin` | Runtime settings |
| `POST /maintenance/upgrade-window` | `admin` | Open a Resolve upgrade window |
| `POST /maintenance/catalog/rebuild` | `admin` | Rebuild the catalog from manifests |
| `POST /maintenance/diagnostics` | `admin` | Generate a diagnostic bundle; returns a job |
| `GET /metrics` | metrics token | Prometheus exposition format (1.2) |

## 3. Job object

```json
{
  "id": "01JB7Y6Z3K9M2Q8R4T5V6W7X8Y",
  "type": "backup.create",
  "state": "running",
  "library_id": "01JB7Y...",
  "progress": 40,
  "current_step": "dump",
  "steps": [
    { "name": "preflight", "state": "succeeded", "started_at": "...", "finished_at": "..." },
    { "name": "dump", "state": "running", "started_at": "..." }
  ],
  "created_by": { "type": "schedule", "id": "01JB..." },
  "created_at": "2026-10-18T07:00:00Z",
  "error": null
}
```

Job types: `library.create`, `library.init_watch`, `library.delete`, `library.purge`,
`library.maintenance`, `library.fingerprint`, `backup.create`, `backup.verify`,
`backup.retention`, `backup.import`, `backup.offsite_sync` (1.1), `restore.new`,
`restore.replace`, `restore.revert`, `system.vacuum`, `system.control_backup`,
`system.catalog_check`, `system.diagnostics`, `system.upgrade_window`.

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
| `backup.stale` | warning | Library without a verified backup for more than the threshold (48 h) |
| `backup.expired` | info | Retention deleted a backup |
| `backup.catalog_mismatch` | warning | The catalog check found backup files missing from the volume or files not in the catalog |
| `restore.started`, `restore.succeeded`, `restore.reverted` | info | Restore |
| `restore.failed` | error | Failed restore (the active library was not touched or was reverted); critical when a swap stopped halfway (`SQS-R053`) or its reopening failed (`SQS-R054`) |
| `storage.low` | warning at 80 %, error at 90 % | Space; `data.volume` is `db`, `app` or `backups` |
| `storage.volume_missing` | critical | The backups volume marker is missing, or a volume cannot be measured; `data.volume` is `db`, `app` or `backups` |
| `postgres.down`, `postgres.up` | critical, info | Availability: first failed check after it answered, or no answer in the 5 minutes after sqsolve started; `up` only after a `down`, and it ends the grouping window of `down` on every channel |
| `postgres.cluster_changed` | critical | The cluster's system identifier is not the stored one; sqsolve holds (ADR 0023) |
| `postgres.cluster_accepted` | warning | `SQSOLVE_PG_ACCEPT_SYSTEM_ID` accepted a new cluster |
| `auth.login_failed_burst` | warning | Probable brute force (`data.ip`, `data.username`) |
| `auth.locked` | critical | An account was locked for 15 minutes after 10 wrong passwords in a row (ADR 0025; `data.username`, `data.ip`) |
| `audit.chain_broken` | critical | Audit log integrity |
| `tls.expiring` | warning, error | Certificate expires in less than 30 days; error in the last 14 days, once expired, or when renewing the panel's fails (`data.cert` is `Panel web` or `PostgreSQL`) |
| `job.interrupted` | warning | Job cut off by a restart or by a graceful stop (`data.job_type`) |
| `job.failed` | error | Failed job (`data.code`, `data.job_type`) |
| `system.started`, `system.upgraded` | info | sqsolve started; it started with a version other than the last one (the message names both) |
| `system.internal_error` | critical | A background loop (job dispatcher, scheduler) recovered from an internal error and keeps running |

## 5. Outgoing webhooks

- `POST` to the channel URL with `Content-Type: application/json`.
- Headers: `X-Sqsolve-Event` (type), `X-Sqsolve-Delivery` (ULID), `X-Sqsolve-Timestamp`
  (Unix seconds), `X-Sqsolve-Signature: sha256=<hex>` with HMAC-SHA256 over
  `timestamp + "." + body` and the channel key.
- Body:

```json
{
  "id": "01JB...",
  "type": "backup.failed",
  "severity": "error",
  "occurred_at": "2026-10-18T07:00:12Z",
  "instance": "main-studio",
  "library": { "id": "01JB...", "slug": "client_x", "display_name": "Client X" },
  "job": { "id": "01JB...", "type": "backup.create" },
  "message": "Insufficient space on the backups volume",
  "code": "SQS-B012",
  "links": { "job": "https://nas.local:9432/jobs/01JB..." }
}
```

- Retries after 10 s, 1 min, 10 min and 1 h; then the channel is marked as failing. Maximum time
  per delivery 10 s. Redirects are not followed.
- The error stored for a failing channel keeps the cause and the HTTP status, never the URL (Slack,
  Discord and ntfy URLs carry tokens) nor a secret field: `SQS-N040 could not reach the
  destination: ...` or `SQS-N002 the destination responded 403 Forbidden`. Only users with
  `notify.manage` see it in the panel.
- Grouping (docs 11 §13): a summary delivery is the latest held event with the message
  `N more like this. Latest: ...` and `"suppressed_since_last": N` in the body.
- "Before" events (`backup.started`, `restore.started`) are sent but do not block the
  operation. Prior blocking belongs to hooks (1.1).

## 6. Hook contract (1.1)

- The administrator places executables in the `hooks` volume: `hooks/<event>.d/<order>-<name>`
  (for example `hooks/backup.succeeded.d/10-upload`).
- sqsolve leaves a JSON request in the shared `hookspool` volume and the `sqsolve-hooks`
  container runs it: the event JSON arrives on standard input; environment variables are only
  `SQSOLVE_EVENT`, `SQSOLVE_DELIVERY` and `SQSOLVE_BACKUP_PATH` (read-only path inside the hooks
  container) when applicable.
- Maximum time 300 s, configurable; standard output and error are saved in the job log
  (truncated to 64 KiB).
- `pre` hooks (`backup.pre.d`, `restore.pre.d`) can be marked as blocking: an exit code other
  than 0 cancels the operation before touching anything.

## 7. CLI

The same binary, for recovery and local automation:

```
sqsolve serve
sqsolve init                         # used by sqsolve-init
sqsolve doctor [--json]              # checks volumes, permissions, PostgreSQL, catalog
sqsolve admin create --username U --role admin
sqsolve admin reset-password U
sqsolve admin disable-totp U
sqsolve admin rotate-master-key      # not implemented (08 §6)
sqsolve admin rotate-superuser       # not implemented (08 §6)
sqsolve library list [--json]
sqsolve backup create <slug>
sqsolve backup list <slug> [--json]
sqsolve backup verify <id> [--level 2]
sqsolve restore <id> --new <new-slug>
sqsolve restore <id> --replace --confirm <slug>
sqsolve catalog rebuild
sqsolve catalog forget <backup-id>
sqsolve audit verify
sqsolve version
```

CLI actions go through the same services, are recorded in the audit log with actor `cli` and
respect job locks.

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
