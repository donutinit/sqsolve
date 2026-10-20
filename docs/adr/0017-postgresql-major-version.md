# ADR 0017. PostgreSQL major versions

**Status:** Accepted (2026-10-18). Implementation in 2.0.

**Context.** PostgreSQL 18 is supported until 2030; at some point a move to another major
version will be needed. `pg_upgrade` needs binaries of both versions and, if it fails halfway,
leaves the data directory in a delicate state.

**Decision.** Logical migration: new cluster in a new volume, every library backed up and
restored with L2 verification, service switch in the YAML, and the previous volume left intact
to go back until the administrator deletes it.

**Alternatives.** `pg_upgrade --link` (fast, but without a simple way back); third-party image
for `pg_upgrade` (another dependency).

**Consequences.** Requires space for two clusters during the migration and a window with the
libraries in maintenance. Resolve libraries are of moderate size, so the time is acceptable; it
is measured before implementing.
