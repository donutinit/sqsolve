# ADR 0009. Logical backups per library

**Status:** Accepted, pending PoC-04 (compatibility with Resolve's restore).

**Context.** The restore that is useful to a studio is per library, sometimes just to pull out a
project. Resolve's backups appear to be `pg_dump` files in custom format [01 §7].

**Decision.** `pg_dump --format=custom --quote-all-identifiers` with an exported snapshot,
per-table counts in the same snapshot, JSON manifest, SHA-256, L1 verification always and L2
weekly. PITR with pgBackRest is added in 2.0 as an extra layer, not as a replacement.

**Alternatives.** Only physical backups of the cluster (restore everything or nothing); backup
tools in a separate container (no verification, no catalog).

**Consequences.** RPO limited by the scheduled frequency until 2.0. The format is standard
PostgreSQL and does not depend on sqsolve to be read.
