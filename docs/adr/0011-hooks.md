# ADR 0011. Hooks and external automation

**Status:** Accepted (2026-10-18).

**Context.** Actions before and after operations are requested. Studio Server runs BASH scripts
inside the same container as the database, with access to everything.

**Decision.** 1.0: signed webhooks and notifications; no user code runs inside sqsolve. 1.1:
executable hooks in the optional `sqsolve-hooks` container, which mounts neither secrets nor the
PostgreSQL socket, sees backups read-only, receives the event on standard input and has a time
limit. Hooks are only added from the host file system, never from the UI.

**Alternatives.** Hooks in the sqsolve container under a separate user (requires capabilities to
switch users); scripts uploaded through the UI (turns a stolen session into code execution).

**Consequences.** Uploading backups to the cloud with a custom script arrives in 1.1; in 1.0 it
is covered with restic (also 1.1) or with the host's storage replication.
