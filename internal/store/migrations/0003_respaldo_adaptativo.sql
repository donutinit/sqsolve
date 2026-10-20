-- Respaldo según actividad (ADR 0022): la política puede revisar cada N minutos y respaldar
-- solo cuando la biblioteca cambió desde el último respaldo. Cada respaldo guarda la marca de
-- actividad de pg_stat_database con la que se tomó.
ALTER TABLE backup_policies ADD COLUMN mode TEXT NOT NULL DEFAULT 'cron';
ALTER TABLE backup_policies ADD COLUMN check_minutes INTEGER NOT NULL DEFAULT 15;
ALTER TABLE backup_policies ADD COLUMN last_check_at TEXT;
ALTER TABLE backup_policies ADD COLUMN last_check_same INTEGER NOT NULL DEFAULT 0;
ALTER TABLE backups ADD COLUMN activity_mark TEXT NOT NULL DEFAULT '';
