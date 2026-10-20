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
  user; `debug` level for static resources.

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
| `GET /healthz` | No | `200` if the process is serving (for the Docker `healthcheck` via `sqsolve healthcheck`) |
| `GET /readyz` | No | `200` if SQLite is open and the volumes have a marker; `503` with a list of failures without sensitive details |
| `GET /api/v1/system/health` | No | Same as `readyz` plus PostgreSQL state |

PostgreSQL being down does not make `healthz` fail: restarting sqsolve does not fix PostgreSQL
and would take the panel away.

## 4. Dashboard indicators

| Indicator | Source | Warning | Error |
|---|---|---|---|
| PostgreSQL available | `SELECT 1` every 15 s | Latency above 500 ms | No response 3 times in a row |
| Total connections | `pg_stat_activity` | 80 % of `max_connections` | 95 % |
| Space on `pgdata`, `state`, `backups` | `statfs` of each volume (`pgdata` mounted read-only at `/srv/pgdata`, only to measure) | 80 % | 90 % |
| Last verified backup per library | Catalog | More than 26 h | More than 48 h |
| Last L2 verification | Catalog | More than 8 days | Failed |
| Failed jobs in 24 h | `jobs` | 1 | 3 |
| Certificates | Files | Less than 30 days | Expired |
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

1. Volumes mounted, markers, permissions and space.
2. Secrets present and with permissions 0400.
3. Connection over the socket as `sqsolve_admin` and as superuser.
4. The current `pg_hba.conf` matches what the catalog would generate.
5. Roles and databases match the catalog (orphan databases, orphan roles, libraries without a
   database).
6. Catalog matches the files in the backups volume.
7. Audit chain intact.
8. Certificates valid.
9. Interrupted jobs pending cleanup.
