# 13. Observability, logs and diagnostics

## 1. Process logs

- `log/slog` to standard error (collected by Docker and shown by any NAS panel).
- JSON by default; readable text with `SQSOLVE_LOG_FORMAT=text`. Level with
  `SQSOLVE_LOG_LEVEL` (`debug`, `info`, `warn` or `error`; `info` by default); console commands
  only log warnings and errors.
- Fields: `time`, `level` and `msg`, plus the attributes of each line: an event carries `event`,
  `library` and `job` (at `INFO`, `WARN` or `ERROR` by its severity), an error `err`. There is no
  request identifier.
- Redaction: values of type `secrets.String` come out as `[redacted]`, and so does any attribute
  whose key contains `password`, `token`, `secret`, `key`, `authorization` or `cookie`, whatever
  its case. The error stored for a notification channel hides its URL and its secret fields
  (`10-api.md` §5). No test searches the whole log output for secrets;
  `TestFailedStatementsKeepTheVerifierSecret` checks that a password change PostgreSQL refuses
  does not carry the SCRAM verifier.
- HTTP requests, panel and API: one `http` line per request with `method`, `path` (without
  query), `status`, `ms` and `ip`; static resources and the health probes (`/healthz`,
  `/readyz`, `/api/v1/system/health`), which Docker and uptime monitors call every few seconds,
  are not logged (`TestProbesAreNotLogged`).

## 2. Events and audit log

- **Events** (`events` in SQLite): business facts for people; types in `10-api.md` §4;
  deleted after 180 days by the daily housekeeping.
- **Audit log** (`audit_log`): actions by actors, hash-chained (`08-security.md` §9); kept
  indefinitely in SQLite. The daily control plane copy also writes it to
  `backups/_system/audit-<YYYYMM>.jsonl`, one file per month (the current one, and the previous
  one during the first three days of a month).
- **Job logs** (`job_logs`): what someone watching the terminal would see; deleted with their job
  90 days after it ended (jobs pending reconciliation are kept).

The panel shows the events (Activity, filtered by minimum severity), the audit log (Activity,
Audit log tab, exported as JSON Lines at `/activity/audit.jsonl`) and the log of each job on its
page. The API lists the three (`10-api.md` §2).

## 3. Health

| Endpoint | Authentication | What it responds |
|---|---|---|
| `GET /healthz` | No | `200` and `ok` if the process is serving (liveness, for the Docker `healthcheck` via `sqsolve healthcheck`) |
| `GET /readyz` | No | `200` and `ready` if PostgreSQL was reached, reconciled and answers, and the backups volume carries this installation's marker; otherwise `503` and `not ready:` with the failures (`postgresql`, `backup volume`), without sensitive details |
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

The "Attention" block of the dashboard (and `attention` in `GET /api/v1/system/status`) lists:

| Indicator | Source | Warning | Error |
|---|---|---|---|
| PostgreSQL available | Ping every 5 s, from the start of sqsolve | | Not answering (critical). The event `postgres.down` (critical) comes at the first failed check after it answered or, at startup, after 5 minutes without an answer |
| PostgreSQL cluster | `pg_control_system()` at each start against the stored identifier (ADR 0023) | | Different: `postgres.cluster_changed`, critical; sqsolve holds |
| PostgreSQL version | `server_version_num` against the bundled `pg_dump --version` | Older minor version than `pg_dump`: update the postgres image digest | Different major version (critical) |
| Total connections | `pg_stat_activity` (client backends) | 80 % of `max_connections` | |
| Space on `db`, `app`, `backups` | `statfs` of each volume (`db` is mounted read-write at `/var/lib/postgresql`, where sqsolve also writes `pg_hba.conf`) | 80 % | 90 %, or a volume that cannot be measured (`storage.volume_missing`, critical) |
| Volume markers | `.sqsolve-volume` of each volume | Missing on `db` or `app` (doctor) | Missing or foreign on `backups`: `storage.volume_missing`, critical; foreign on `db` or `app`: the doctor fails |
| Recent verified backup per library | Catalog, for active libraries and libraries in maintenance older than the threshold (48 h by default, Settings) | No verified backup within the threshold. Unlike `backup.stale` and §3, this line does not spare a library unchanged since its last verified backup (ADR 0022) | |
| Failed jobs in 24 h | `jobs` | | 1 or more |
| Certificates | Files, daily; the self-generated panel certificate is renewed first when fewer than 30 days remain | Less than 30 days | Less than 14 days or expired (`tls.expiring` at Error severity) |
| Library states | Catalog | Initialization expired | No database on the server (critical); a library waiting for Resolve shows as information |
| Collation version | `pg_database.datcollversion` against the actual one | Different | |

The audit log integrity is not on the dashboard: the daily housekeeping verifies it
(`audit.chain_broken`, critical) and Activity, Audit log shows the state of the chain. sqsolve
does not watch the age of the last L2 verification (the `system.verify_all` job tests the latest
verified backup of each library every Sunday at 05:00) nor sessions idle in transaction.

## 5. Metrics

sqsolve does not expose metrics: there is no `/metrics` endpoint and no Prometheus format.
Prometheus metrics are a long-term direction in `20-roadmap.md` §2. To watch an installation from
outside, use `GET /api/v1/system/health` (§3) and the notification channels (`10-api.md` §5).

## 6. Diagnostic bundle

In Settings, Maintenance, "Download diagnostic bundle" (`GET /settings/diagnostics`, permission
`diagnostics`, administrators) answers a zip `sqsolve-diag-<UTC timestamp>.zip`, generated on each
download and recorded in the audit log (`diagnostics.download`). It contains:

- `version.json`: sqsolve version, commit, installation identifier and start time;
- `config.json`: listen address, volume paths inside the container, PostgreSQL socket and port,
  UID and GID, workers, time zone and whether the panel serves plain HTTP; `settings.json`: the
  runtime settings;
- `doctor.json`: the doctor checks (§7);
- `postgresql/`: the generated `postgresql.conf` and `pg_hba.conf` (secret-free by design), the
  databases with size, owner and connection state, the server state and the roles with their
  attributes (without passwords);
- `libraries.json`, `backups.json` (up to 2000, deleted ones included, paths relative to the
  backups volume), `schedules.json` and the last 500 events (`events.json`);
- `jobs/<id>.json`: the last 20 failed or interrupted jobs with their steps and logs.

It never includes: secrets, encrypted passwords, library contents, the audit log or project
names. The Maintenance tab summarizes the contents next to the download.

## 7. `sqsolve doctor`

Checks with result `ok`, `warn` or `fail` and a suggestion per failure; the Health page shows the
same list:

1. The application, backups and database configuration volumes can be written, and their space
   (`warn` from 80 %, `fail` from 90 %).
2. Volume markers: the backups volume belongs to this installation (`fail` otherwise); the `db`
   and `app` markers (`warn` when missing, `fail` when foreign or swapped).
3. Secrets present with permissions 0400: the master key and the `sqsolve_admin` password.
4. PostgreSQL answers over the socket (version, connections; `warn` without page checksums); its
   version against the bundled `pg_dump`; cluster identity (ADR 0023); the superuser over the
   local socket. These checks are skipped when PostgreSQL does not answer.
5. `pg_hba.conf` has no errors (`pg_hba_file_rules`).
6. Databases on the server that are not in the catalog (`warn`).
7. Audit chain intact.
8. Integrity of the SQLite database.

`sqsolve doctor` exits with an error when a check fails; with `--json` it prints the list and
exits without error.
