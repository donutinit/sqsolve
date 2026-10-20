-- Account lockout after consecutive wrong passwords (ADR 0021).
-- Only `sqsolve admin unlock <user>` from the container console lifts it.
ALTER TABLE admin_users ADD COLUMN failed_logins INTEGER NOT NULL DEFAULT 0;
ALTER TABLE admin_users ADD COLUMN locked_at TEXT;
