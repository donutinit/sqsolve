# ADR 0010. Restore by swap

**Status:** Accepted. PoC-09 (2026-10-19, `docs/poc/poc-09-forced-disconnect.md`) and the 1.2.2
acceptance (2026-10-26, `docs/acceptance/1.2.2.md`) showed that Resolve reconnects on its own
after the swap and that a project left open saves its pending changes into the restored library,
so the replace and revert pages ask to close the project in Resolve on every workstation first.

**Context.** Restoring over the active database (as Studio Server does, with a warning that
everyone must leave Resolve) destroys the current state before knowing whether the restore
succeeds.

**Decision.** Always restore into a new database; verify against the manifest; take a backup
first; lock and disconnect the active one; rename it to `__prev`; rename the restored one to
the original name; reopen. Every step goes into a journal to recover after an outage. Keep
`__prev` for 7 days with one-click revert. The default mode in the UI is "as a new library".

**Alternatives.** `pg_restore --clean` over the active database; restore under another name and
ask every workstation to reconnect (more steps for editors).

**Consequences.** Needs free space for two copies of the library during the operation. If PoC-09
shows that Resolve keeps caches that get confused after the swap, closing Resolve on the
workstations becomes mandatory and the UI shows it as a required step.
