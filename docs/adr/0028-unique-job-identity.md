# ADR 0028. A unique job is its type, its library and its parameters

**Status:** Accepted (2026-10-25), Diego's decision. Refines `jobs.Unique` (ADR 0026 moved its
check into the transaction that queues the job).

**Context.** `jobs.Unique` returned the queued or running job of the same type and library,
whatever its parameters. For a recreate that meant a second request with another backup got,
without a word, the job already queued with the first backup: the person believed the library
was coming back from the backup they chose. The 2026-10-25 audit asked for the identity of a
unique job to be decided before more code relies on it (PLAN 1.2.3). Three options were weighed:
the first request wins (documented and tested), the canonical parameters are part of the
identity (both requests queue and run one after the other), or a request whose parameters
differ is refused.

**Decision.**

- **`jobs.Unique`**: a request for a type and library that already has a queued or running job
  returns that job when the canonical parameters (the JSON of the parameters with its object
  keys sorted) are the same, and is refused with `SQS-J005` (409 in the API) when they differ.
  The refusal names the job that holds the library and its parameters stay as they were; nothing
  is queued and no audit entry is written. Used by revert, recreate and the purge of expired
  trash.
- **`jobs.UniqueAny`**: the first request wins whatever its parameters. Used where any job of
  the type serves the purpose: a backup of the library (the one sqsolve takes after activating a
  library or before a restore, and the scheduled one), because any backup protects it.
- A running row that this process is not running (its final update failed) does not count, as
  before.

**Consequences.** A second recreate with another backup gets `SQS-J005` and must wait for or
cancel the first one; the same request twice still queues one job. Queuing two destructive jobs
for one library, which the second option would allow, cannot happen. `TestUniqueJobIdentity`
pins the three cases (same parameters, different parameters, `UniqueAny`).

**Alternatives.** The first request wins: keeps a silent surprise on the most delicate operation.
Parameters in the identity: two recreates or reverts would run one after the other on the same
library, the second undoing the first.
