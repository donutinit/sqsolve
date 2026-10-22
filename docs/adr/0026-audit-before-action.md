# ADR 0026. The audit entry is written with or before the action it records

**Status:** Accepted (2026-10-22). Makes the rule of `08-security.md` §9 ("every action that
changes state or exposes secrets is recorded") enforceable.

**Context.** The 2026-10-21 repository audit (finding AUD-004) showed that callers treated the
audit log as best effort. Delete, purge and restore by replacing wrote an `ok` entry before
queueing their job, so a failed queue left a false success in the chain, and a failed entry
did not stop the job. A password was returned, a backup file streamed and the recovery kit
downloaded even when their entry could not be written. Deleting a backup removed its files and
only then wrote the entry. The hash chain itself was sound; what failed was the link between the
entry and the action.

**Decision.** The audit entry of a sensitive action is written in the same SQLite transaction as
the action, or before it. If the entry cannot be written, the action is not done and the caller
gets `SQS-X040` (HTTP 503 in the API). Four cases:

- **Changes inside SQLite** (users, roles, passwords of panel users, API tokens): the change and
  its entry are one transaction (`audit.Log.RecordTx`). Outcome `ok`.
- **Actions that run as a job** (create, delete, purge and untrash a library; back up now;
  restore as new, by replacing, revert, recreate; import; prepare an export from a backup;
  system maintenance jobs; Retry; the Resolve upgrade window): the job row and an entry with the
  new outcome `accepted` and the job's id are one transaction (`jobs.Audited`). The check of
  `jobs.Unique` moves into that transaction too, so two requests queue one job. When the job
  ends, its result is written under the same action and actor: `ok`, or `failed` with the final
  state (`failed`, `cancelled`, `interrupted`) and the error code. A job that a crash leaves
  running gets its `failed` (interrupted) entry at the next start. The job keeps its own final
  state if this second entry cannot be written; the failure is logged.
- **Changes outside SQLite** (PostgreSQL or files: delete a backup, rotate a password, revoke a
  member, network rules, maintenance, end a session, reopen the initialization window, close an
  export workspace, regenerate `pg_hba.conf`): an `accepted` entry is written first and the change
  does not run without it (`audit.Log.Do`); its result follows as `ok` or `failed` with the
  error. A result that cannot be written after a successful change is reported as `SQS-X041`.
- **Disclosures** (reveal a password, download a backup, the recovery kit, the diagnostics, an
  export of a project, a still or an album): the `ok` entry is written before anything is sent,
  and nothing is sent without it (`audit.Log.Require`).

Logins, failed logins, logouts and denials stay best effort: refusing a login because the audit
log is down would lock everyone out of the panel that shows the problem.

**Consequences.** A full or read-only disk under SQLite now stops the actions above instead of
letting them run unrecorded; the rest of the panel keeps working. Actions that run as a job or
outside SQLite now leave two entries (`accepted`, then `ok` or `failed`); the Activity page
shows `accepted` as a neutral badge. The `jobs` table gains an `audit` column (migration 0007)
naming the action whose result the job writes.

**Alternatives.** Writing the entry after the action and retrying: the action would already be
done when the entry fails, which is the gap the audit found. One entry per action with its
outcome updated in place: the chain hashes each entry, so updating one breaks verification.
Failing closed on logins too: it turns a disk problem into a lockout.
