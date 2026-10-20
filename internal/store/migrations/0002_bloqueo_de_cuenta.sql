-- Bloqueo de cuenta tras contraseñas incorrectas seguidas (ADR 0021).
-- Solo lo levanta `sqsolve admin unlock <usuario>` desde la consola del contenedor.
ALTER TABLE admin_users ADD COLUMN failed_logins INTEGER NOT NULL DEFAULT 0;
ALTER TABLE admin_users ADD COLUMN locked_at TEXT;
