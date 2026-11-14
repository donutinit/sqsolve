# ADR 0029. Replace, delete, recover and a purge by hand are unique jobs

**Status:** Accepted (2026-11-13), Diego's decision. Extends the list of operations of ADR 0028; the definition
of `jobs.Unique` does not change.

**Context.** ADR 0028 applied the unique job identity to revert, recreate and the purge of
expired trash, and left restore by replacing, delete and a purge asked by hand out; it did not
name recover from the trash. Known issue K-03 of 1.2.3: two identical requests queued two jobs.
The second delete, recover or purge ended as a failed job (`SQS-L041`, `SQS-L042`, `SQS-L044`),
and a second replacement ran on the result of the first: a second backup before the restore and
a second swap.

**Decision.**

1. `restore.replace`, `library.delete`, `library.untrash` and a `library.purge` asked by hand
   are queued with `jobs.Unique` as ADR 0028 defines it, from the panel, the API and the
   console alike.
2. The identity of a replacement is its backup and its choice for the open sessions
   (`backup_id`, `terminate_sessions`): the same request returns the queued or running job;
   another backup or the other choice is refused with `SQS-J005` (409 in the API), which names
   the job that holds the library.
3. Delete, recover and purge have no parameters. A purge by hand and the purge of expired trash
   are one job, whichever was queued first.
4. The checks of the request stay before the queue: once the running job has changed the state
   of the library, a new request gets the refusal of that state (a library being swapped is
   `restoring` and a replacement gets `SQS-R003`), exactly as after the job ends.
5. The third point of ADR 0028 stands: a console process runs no jobs, so it sees the queued
   jobs and not the ones the server is running.

**Consequences.** A request that joins a job writes no audit entry (ADR 0028), so a purge by
hand that joins the job of the automatic purge leaves no entry of the administrator; the
automatic purge is queued with its own entry (actor `system`), so the job has one. A
replacement that is waiting up to 10 minutes for open sessions cannot be followed by the same
replacement with the sessions closed until it ends, because a running replacement cannot be
cancelled. `POST /backups/{id}/restore` can answer `409` where it answered `202` and queued a
second job. The run-time re-validation of each job stays. From the console, a
`sqsolve restore --replace` repeated while the server already runs the same replacement still
queues a second job (point 5; K-03 in `19-risks.md` §5 keeps that case). The tests are
`TestReplaceTwiceIsOneJobAndOtherParametersAreRefused`, `TestDeleteRecoverAndPurgeTwiceAreOneJob`
and `TestPurgeByHandSharesTheJobOfTheAutomaticPurge` (core), `TestUniqueReturnsTheRunningJob`
(jobs), `TestAPILibraryDeleteTwiceIsOneJob` and `TestAPIRestoreReplaceTwice` (API),
`TestDangerZoneSecondRequestOpensTheSameJob` and `TestReplaceFormSecondRequest` (panel), and
`TestSecondReplaceOrDeleteJoinsTheRunningJob` (with PostgreSQL, while the job runs).

**Alternatives.** `UniqueAny` for a replacement: the silent surprise ADR 0028 removed from
recreate. Leaving `terminate_sessions` out of the identity: a second identity rule, and the
second request would get a job that does not do what it asked. Keeping only the run-time
re-validation: that is K-03. Counting every `running` row as held in a process that runs no
jobs: a row left `running` by a server that stopped would hold the identity until the next
start.
