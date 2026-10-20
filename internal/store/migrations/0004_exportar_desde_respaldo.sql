-- Export of .drp and .drx from a backup (docs 22 §7): the file is restored into a temporary
-- database "sqsolve_export_<id>" that lives for a while and is deleted on close or expiry.
CREATE TABLE export_workspaces (
  id         TEXT PRIMARY KEY,
  backup_id  TEXT NOT NULL REFERENCES backups(id),
  db_name    TEXT NOT NULL,
  job_id     TEXT NOT NULL DEFAULT '',
  state      TEXT NOT NULL,
  error      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX export_workspaces_backup ON export_workspaces(backup_id, created_at);
