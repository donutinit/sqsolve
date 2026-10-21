# 13. Observability, logs and diagnostics

## 1. Process logs

- `log/slog` to standard output (collected by Docker and shown by any NAS panel).
- JSON by default; readable text with `SQSOLVE_LOG_FORMAT=text`.
- Fixed fields: `time`, `level`, `msg`, `component`, and when applicable `request_id`, `job_id`,
  `library`, `user`, `code`.
- Redaction: values of type `secret.String` and the keys `password`, `token`, `secret`, `key`,
  `authorization`, `cookie` come out as `[redacted]`. Automatic test: the integration suite is
  run with marked secrets and each one is searched for in the entire output.
- HTTP requests: one line per request with method, path (without query), status, duration and
  user; static resources and the health probes (`/healthz`, `/readyz`,
  `/api/v1/system/health`), which Docker and uptime monitors call every few seconds, are not
  logged.

## 2. Events and audit log

- **Events** (`events` in SQLite): business facts for people; types in `10-api.md` §4;
  retention 180 days.
- **Audit log** (`audit_log`): actions by actors, hash-chained (`08-security.md` §9); retention
  indefinite in SQLite with a monthly export to `backups/_system/`.
- **Job logs** (`job_logs`): what someone watching the terminal would see; retention 90 days.

All three are queried and filtered in the interface and exported as JSON Lines.

## 3. Health

| Endpoint | Authentication | What it responds |
|---|---|---|
| `GET /healthz` | No | `200` if the process is serving (liveness, for the Docker `healthcheck` via `sqsolve healthcheck`) |
| `GET /readyz` | No | `200` if PostgreSQL was reached, reconciled and answers, and the backups volume carries this installation's marker; `503` with a list of failures without sensitive details |
| `GET /api/v1/system/health` | No | JSON for uptime monitors: `503` when anything is critical (see below) |

PostgreSQL being down does not make `healthz` fail: restarting sqsolve does not fix PostgreSQL
and would take the panel away. The monitor starts with sqsolve, before PostgreSQL answers, so a
PostgreSQL that never comes up still raises `postgres.down` (critical) after 5 minutes.

`GET /api/v1/system/health` shows states and counts only: no paths, library names or
identifiers.

```
{
  "ready": true,                 // as /readyz
  "postgres": true,
  "backup_volume": true,         // marker of this installation
  "postgres_cluster": "ok",      // "changed": another cluster, sqsolve holds (ADR 0023)
  "status": "ok",                // ok, warning or critical; critical answers 503
  "backups": {
    "status": "ok",              // critical when any library is stale
    "libraries": 4,              // active libraries with an enabled backup schedule
    "stale": 0,
    "oldest_verified_age_hours": 20.5,
    "stale_after_hours": 48      // Settings, the threshold housekeeping uses
  },
  "volumes": [
    {"name": "db", "used_percent": 41.2, "status": "ok"},
    {"name": "app", "used_percent": 41.2, "status": "ok"},
    {"name": "backups", "used_percent": 91.0, "status": "critical"}
  ]
}
```

A library is stale when its last verified backup is older than the threshold and the library
changed since (an untouched library keeps its old backup, ADR 0022), or when it has none and
exists for longer than the threshold. It is the rule of the `backup.stale` alert, shared in one
function: a verified backup dated in the future (the clock went back) does not count as recent.
A volume is `warning` from 80 % and `critical` from 90 % or when it cannot be read. The backup
part is recomputed at most every 30 seconds, by one reading at a time (concurrent probes share
it), bounded to 10 seconds and independent of the probe's own timeout; a reading that fails
answers `critical` for that request and is not cached.

## 4. Dashboard indicators

| Indicator | Source | Warning | Error |
|---|---|---|---|
| PostgreSQL available | Ping every 5 s, from the start of sqsolve | | First failed check after it answered (`postgres.down`, critical); at startup, no answer in 5 minutes |
| PostgreSQL cluster | `pg_control_system()` at each start against the stored identifier (ADR 0023) | | Different: `postgres.cluster_changed`, critical; sqsolve holds |
| PostgreSQL version | `server_version_num` against the bundled `pg_dump --version` | Older minor version than `pg_dump`: update the postgres image digest | Different major version |
| Total connections | `pg_stat_activity` | 80 % of `max_connections` | 95 % |
| Space on `db`, `app`, `backups` | `statfs` of each volume (`db` is mounted read-write at `/var/lib/postgresql`, where sqsolve also writes `pg_hba.conf`) | 80 % | 90 %; a volume that cannot be measured: `storage.volume_missing`, critical |
| Volume markers | `.sqsolve-volume` of each volume | Missing on `db` or `app` (doctor) | Missing or foreign on `backups`: `storage.volume_missing`, critical |
| Last verified backup per library | Catalog | More than 26 h | More than 48 h |
| Last L2 verification | Catalog | More than 8 days | Failed |
| Failed jobs in 24 h | `jobs` | 1 | 3 |
| Certificates | Files, daily; the self-generated panel certificate is renewed first when fewer than 30 days remain | Less than 30 days | Less than 14 days or expired (`tls.expiring` at Error severity) |
| Audit log integrity | Daily verification | | Broken |
| Collation version | `pg_database.datcollversion` against the actual one | Different | |
| Sessions idle in transaction | `pg_stat_activity.state = 'idle in transaction'` for more than 1 h | Yes | |

## 5. Metrics (1.2)

`GET /metrics` in Prometheus exposition format, protected with its own token. Written by hand
(few series, no dependency):

```
sqsolve_build_info{version,commit,go_version} 1
sqsolve_postgres_up 1
sqsolve_library_size_bytes{library}
sqsolve_library_connections{library}
sqsolve_backup_last_success_timestamp_seconds{library}
sqsolve_backup_last_verified_timestamp_seconds{library}
sqsolve_backup_duration_seconds{library}           (latest)
sqsolve_backup_size_bytes{library}                 (latest)
sqsolve_jobs{state}
sqsolve_volume_free_bytes{volume}
sqsolve_volume_size_bytes{volume}
sqsolve_http_requests_total{code_class}
```

## 6. Diagnostic bundle

Job `system.diagnostics`, generates `sqsolve-diag-<mark>.zip`, downloadable once:

- versions (sqsolve, Go, PostgreSQL, image), `SQSOLVE_*` variables without secrets;
- output of `sqsolve doctor --json`;
- generated `postgresql.conf` and `pg_hba.conf` (secret-free by design);
- relevant `pg_settings`, list of databases with size, owner and connection state, roles with
  attributes (without passwords);
- last 1000 events and logs of the last 20 failed jobs;
- last 2000 lines of the process log available in memory (circular buffer);
- backup catalog without absolute host paths.

It never includes: secrets, encrypted passwords, library contents, the full audit log or project
names. The interface lists the contents before downloading.

## 7. `sqsolve doctor`

Checks with result `ok`, `warn` or `fail` and a suggestion per failure:

1. Volumes mounted, markers of the three volumes, permissions and space.
2. Secrets present and with permissions 0400.
3. Connection over the socket as `sqsolve_admin` and as superuser; PostgreSQL version against the
   bundled `pg_dump`; cluster identity (ADR 0023).
4. The current `pg_hba.conf` matches what the catalog would generate.
5. Roles and databases match the catalog (orphan databases, orphan roles, libraries without a
   database).
6. Catalog matches the files in the backups volume.
7. Audit chain intact.
8. Certificates valid.
9. Interrupted jobs pending cleanup.
