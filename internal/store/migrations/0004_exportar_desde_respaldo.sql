-- Exportación de .drp y .drx desde un respaldo (docs 22 §7): el archivo se restaura en una base
-- temporal "sqsolve_export_<id>" que vive un rato y se borra al cerrar o al vencer.
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
