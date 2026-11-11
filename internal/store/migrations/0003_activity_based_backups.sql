-- Activity-based backups (ADR 0022): the policy can check every N minutes and back up
-- only when the library changed since the last backup. Each backup stores the activity
-- mark of pg_stat_database it was taken with.
ALTER TABLE backup_policies ADD COLUMN mode TEXT NOT NULL DEFAULT 'cron';
ALTER TABLE backup_policies ADD COLUMN check_minutes INTEGER NOT NULL DEFAULT 15;
ALTER TABLE backup_policies ADD COLUMN last_check_at TEXT;
ALTER TABLE backup_policies ADD COLUMN last_check_same INTEGER NOT NULL DEFAULT 0;
ALTER TABLE backups ADD COLUMN activity_mark TEXT NOT NULL DEFAULT '';
