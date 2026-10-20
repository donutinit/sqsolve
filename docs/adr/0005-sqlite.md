# ADR 0005. Control plane state

**Status:** Accepted (2026-10-18).

**Context.** Panel users, backup catalog, jobs and audit log must stay available while
PostgreSQL is down or being restored.

**Decision.** SQLite (`modernc.org/sqlite`, no CGO) in the `state` volume, WAL mode, daily copy
with `VACUUM INTO` to the backups volume, and a catalog that can be rebuilt from manifests.

**Alternatives.** An `sqsolve` database inside the same PostgreSQL; JSON files (`06-stack.md`
§3).

**Consequences.** Two database engines, but one is an embedded library without a server.
Serialized writes, enough for the expected load.
