# ADR 0006. PostgreSQL version and configuration

**Status:** Accepted, pending PoC-01 (collation) and PoC-02 (Resolve versions).

**Context.** Resolve 18 to 20 were used with PostgreSQL 13, now out of support. Resolve 21.1
ships PostgreSQL 18 in Project Server and declares compatibility with libraries on 13 or later
[01 §4]. PostgreSQL 18 is supported until 2030-11-14.

**Decision.** Official image `postgres:18.x-trixie` pinned by digest. Cluster with
`--encoding=UTF8 --locale-provider=builtin --builtin-locale=C.UTF-8`, checksums (default in
18), SCRAM only.

**Alternatives.**
- PostgreSQL 13: compatible with everything before, but without security patches.
- PostgreSQL 17: valid; reserved as an alternative profile if PoC-02 requires it for 20.3.x.
- glibc collation (`en_US.UTF-8`): a more "natural" alphabetical order, but an update of the
  base image can invalidate text indexes.

**Consequences.** If PoC-01 shows that Resolve sets the collation when creating the database, or
that the `C.UTF-8` order bothers editors, ICU (`--locale-provider=icu`) is evaluated through a
new ADR, before 1.0, because the collation cannot be changed afterwards without rebuilding the
cluster. ICU gives a more natural order, but its version also comes with the image; it would
require watching `datcollversion` and reindexing when it changes.
