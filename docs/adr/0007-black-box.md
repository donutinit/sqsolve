# ADR 0007. Resolve libraries are a black box

**Status:** Accepted (2026-10-18).

**Context.** Resolve's schema is undocumented, changes between versions and belongs to
Blackmagic. Features like "list the projects of a library" or "release locks" would require
reading or modifying it.

**Decision.** sqsolve neither reads nor writes Resolve tables. It only uses: the system catalog
(`pg_class`, `pg_attribute`, `pg_database`, `pg_stat_activity`), `count(*)` per table to verify
backups, `pg_dump`/`pg_restore`, and database-level and role-level DDL.

**Alternatives.** Informational reading of the schema (fragile, legally greyer); writing to
"repair" locks (risk of corrupting projects).

**Consequences.** sqsolve keeps working even if Resolve changes its schema. The UI shows neither
projects nor bins. Any proposal that breaks this principle needs an ADR that explicitly
supersedes it.
