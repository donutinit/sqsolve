# ADR 0016. sqsolve base image

**Status:** Accepted (2026-10-18).

**Context.** `pg_dump` must be the same major version as the server or newer; the shared socket
needs the same permissions the PostgreSQL image creates.

**Decision.** The sqsolve image starts from the same `postgres:18.x-trixie@digest` as the server
and adds the `sqsolve` binary. Its `pg_dump`, `pg_restore` and `vacuumdb` are used.

**Alternatives.** Minimal image with PostgreSQL clients installed from packages (another source
of versions to keep in sync); running `pg_dump` inside the PostgreSQL container (would require
the Docker socket).

**Consequences.** Larger image than a minimal one, but it shares layers with the server, so on
disk it adds almost nothing. Each sqsolve release pins both digests together.
