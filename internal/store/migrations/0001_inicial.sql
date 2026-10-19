-- SPDX-License-Identifier: AGPL-3.0-only
-- Esquema inicial del plano de control de sqsolve.

CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  updated_by TEXT NOT NULL DEFAULT ''
);

CREATE TABLE admin_users (
  id                  TEXT PRIMARY KEY,
  username            TEXT NOT NULL UNIQUE COLLATE NOCASE,
  display_name        TEXT NOT NULL DEFAULT '',
  password_hash       TEXT NOT NULL,
  role                TEXT NOT NULL CHECK (role IN ('admin', 'operator', 'viewer')),
  totp_secret_enc     TEXT NOT NULL DEFAULT '',
  totp_enabled        INTEGER NOT NULL DEFAULT 0,
  totp_last_counter   INTEGER NOT NULL DEFAULT 0,
  created_at          TEXT NOT NULL,
  password_changed_at TEXT NOT NULL,
  last_login_at       TEXT,
  disabled_at         TEXT
);

CREATE TABLE recovery_codes (
  user_id   TEXT NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  code_hash TEXT NOT NULL,
  used_at   TEXT,
  PRIMARY KEY (user_id, code_hash)
);

CREATE TABLE sessions (
  id_hash      TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  last_seen_at TEXT NOT NULL,
  reauth_at    TEXT NOT NULL,
  mfa_pending  INTEGER NOT NULL DEFAULT 0,
  ip           TEXT NOT NULL DEFAULT '',
  user_agent   TEXT NOT NULL DEFAULT ''
);

CREATE TABLE api_tokens (
  id           TEXT PRIMARY KEY,
  user_id      TEXT NOT NULL REFERENCES admin_users(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  token_hash   TEXT NOT NULL UNIQUE,
  scopes       TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  expires_at   TEXT,
  last_used_at TEXT,
  revoked_at   TEXT
);

CREATE TABLE libraries (
  id                     TEXT PRIMARY KEY,
  slug                   TEXT NOT NULL,
  display_name           TEXT NOT NULL,
  state                  TEXT NOT NULL,
  db_name                TEXT NOT NULL,
  owner_role             TEXT NOT NULL,
  creation_strategy      TEXT NOT NULL,
  init_window_expires_at TEXT,
  init_table_count       INTEGER NOT NULL DEFAULT -1,
  resolve_version        TEXT NOT NULL DEFAULT '',
  schema_fingerprint     TEXT NOT NULL DEFAULT '',
  schema_table_count     INTEGER NOT NULL DEFAULT 0,
  fingerprint_checked_at TEXT,
  connection_limit       INTEGER NOT NULL DEFAULT -1,
  notes                  TEXT NOT NULL DEFAULT '',
  trash_db_name          TEXT NOT NULL DEFAULT '',
  prev_db_name           TEXT NOT NULL DEFAULT '',
  prev_expires_at        TEXT,
  prev_backup_id         TEXT NOT NULL DEFAULT '',
  created_at             TEXT NOT NULL,
  updated_at             TEXT NOT NULL,
  trashed_at             TEXT,
  purge_after            TEXT,
  purged_at              TEXT
);
CREATE UNIQUE INDEX libraries_slug_live ON libraries(slug) WHERE state <> 'purged';

CREATE TABLE library_credentials (
  id           TEXT PRIMARY KEY,
  library_id   TEXT NOT NULL REFERENCES libraries(id),
  kind         TEXT NOT NULL CHECK (kind IN ('owner', 'member')),
  pg_role      TEXT NOT NULL,
  person_label TEXT NOT NULL DEFAULT '',
  password_enc TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  rotated_at   TEXT,
  revoked_at   TEXT
);
CREATE INDEX library_credentials_lib ON library_credentials(library_id);

CREATE TABLE network_rules (
  id          TEXT PRIMARY KEY,
  library_id  TEXT NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
  cidr        TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  created_at  TEXT NOT NULL
);

CREATE TABLE backup_policies (
  library_id    TEXT PRIMARY KEY REFERENCES libraries(id),
  enabled       INTEGER NOT NULL DEFAULT 1,
  schedule_cron TEXT NOT NULL,
  timezone      TEXT NOT NULL,
  keep_last     INTEGER NOT NULL,
  keep_daily    INTEGER NOT NULL,
  keep_weekly   INTEGER NOT NULL,
  keep_monthly  INTEGER NOT NULL,
  min_verified  INTEGER NOT NULL,
  updated_at    TEXT NOT NULL
);

CREATE TABLE backups (
  id                 TEXT PRIMARY KEY,
  library_id         TEXT NOT NULL REFERENCES libraries(id),
  job_id             TEXT NOT NULL DEFAULT '',
  kind               TEXT NOT NULL,
  status             TEXT NOT NULL,
  verify_level       INTEGER NOT NULL DEFAULT 0,
  verify_status      TEXT NOT NULL DEFAULT 'none',
  verified_at        TEXT,
  verify_detail      TEXT NOT NULL DEFAULT '',
  file_path          TEXT NOT NULL DEFAULT '',
  size_bytes         INTEGER NOT NULL DEFAULT 0,
  sha256             TEXT NOT NULL DEFAULT '',
  pg_server_version  TEXT NOT NULL DEFAULT '',
  pg_dump_version    TEXT NOT NULL DEFAULT '',
  archive_version    TEXT NOT NULL DEFAULT '',
  compression        TEXT NOT NULL DEFAULT '',
  schema_fingerprint TEXT NOT NULL DEFAULT '',
  table_count        INTEGER NOT NULL DEFAULT 0,
  counts_exact       INTEGER NOT NULL DEFAULT 1,
  resolve_version    TEXT NOT NULL DEFAULT '',
  pinned             INTEGER NOT NULL DEFAULT 0,
  source_db          TEXT NOT NULL DEFAULT '',
  started_at         TEXT NOT NULL,
  finished_at        TEXT,
  error              TEXT NOT NULL DEFAULT '',
  expired_reason     TEXT NOT NULL DEFAULT '',
  deleted_at         TEXT
);
CREATE INDEX backups_library ON backups(library_id, started_at);

CREATE TABLE jobs (
  id              TEXT PRIMARY KEY,
  type            TEXT NOT NULL,
  library_id      TEXT NOT NULL DEFAULT '',
  params          TEXT NOT NULL DEFAULT '{}',
  state           TEXT NOT NULL,
  priority        INTEGER NOT NULL DEFAULT 50,
  scheduled_for   TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  started_at      TEXT,
  finished_at     TEXT,
  progress        INTEGER NOT NULL DEFAULT 0,
  current_step    TEXT NOT NULL DEFAULT '',
  error_code      TEXT NOT NULL DEFAULT '',
  error_detail    TEXT NOT NULL DEFAULT '',
  created_by_type TEXT NOT NULL,
  created_by_id   TEXT NOT NULL DEFAULT '',
  created_by_name TEXT NOT NULL DEFAULT '',
  schedule_id     TEXT NOT NULL DEFAULT '',
  attempt         INTEGER NOT NULL DEFAULT 1,
  result          TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX jobs_queue ON jobs(state, priority DESC, scheduled_for);
CREATE INDEX jobs_library ON jobs(library_id, created_at);

CREATE TABLE job_steps (
  job_id      TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  name        TEXT NOT NULL,
  state       TEXT NOT NULL,
  started_at  TEXT NOT NULL,
  finished_at TEXT,
  data        TEXT NOT NULL DEFAULT '{}',
  PRIMARY KEY (job_id, seq)
);

CREATE TABLE job_logs (
  job_id  TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  seq     INTEGER NOT NULL,
  ts      TEXT NOT NULL,
  level   TEXT NOT NULL,
  message TEXT NOT NULL,
  PRIMARY KEY (job_id, seq)
);

CREATE TABLE schedules (
  id                     TEXT PRIMARY KEY,
  kind                   TEXT NOT NULL,
  target_id              TEXT NOT NULL DEFAULT '',
  cron                   TEXT NOT NULL,
  timezone               TEXT NOT NULL,
  enabled                INTEGER NOT NULL DEFAULT 1,
  misfire_window_minutes INTEGER NOT NULL DEFAULT 720,
  next_run_at            TEXT,
  last_run_at            TEXT,
  last_job_id            TEXT NOT NULL DEFAULT '',
  created_at             TEXT NOT NULL,
  UNIQUE (kind, target_id)
);

CREATE TABLE notification_channels (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL CHECK (kind IN ('webhook', 'ntfy', 'smtp')),
  name          TEXT NOT NULL,
  config_enc    TEXT NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1,
  min_severity  TEXT NOT NULL DEFAULT 'error',
  event_pattern TEXT NOT NULL DEFAULT '*',
  last_ok_at    TEXT,
  last_error    TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL
);

CREATE TABLE events (
  id         TEXT PRIMARY KEY,
  ts         TEXT NOT NULL,
  type       TEXT NOT NULL,
  severity   TEXT NOT NULL,
  library_id TEXT NOT NULL DEFAULT '',
  job_id     TEXT NOT NULL DEFAULT '',
  message    TEXT NOT NULL,
  data       TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX events_ts ON events(ts);
CREATE INDEX events_library ON events(library_id, ts);

CREATE TABLE audit_log (
  seq         INTEGER PRIMARY KEY AUTOINCREMENT,
  ts          TEXT NOT NULL,
  actor_type  TEXT NOT NULL,
  actor_id    TEXT NOT NULL,
  actor_name  TEXT NOT NULL,
  actor_ip    TEXT NOT NULL,
  action      TEXT NOT NULL,
  target_type TEXT NOT NULL,
  target_id   TEXT NOT NULL,
  outcome     TEXT NOT NULL,
  details     TEXT NOT NULL,
  prev_hash   TEXT NOT NULL,
  hash        TEXT NOT NULL
);
