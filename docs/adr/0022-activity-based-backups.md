# ADR 0022. Activity-based backups

**Status:** Accepted (2026-10-19). Requested by Diego.

**Context.** A cron schedule backs up at fixed hours: at night when nobody worked and nothing
changed, and only once a day when a colorist spent eight hours touching the project. Diego wants
the opposite: every 10 or 15 minutes while there are changes in the library, and nothing when
it is flat. "Differential" in his words: if there is no difference since the last backup, no
new one goes out.

**Decision.**

- The backup policy has a **mode** (`mode`): `cron`, the usual one, or `activity`. In `activity`
  mode the scheduler wakes up every `check_minutes` (5, 10, 15, 30 or 60; 15 by default) and,
  before queuing the job, a **gate** compares the library's **activity mark** with the one of
  the last successful backup. If they match, there is no job and no backup; otherwise, it backs
  up.
- The activity mark is `tup_inserted + tup_updated + tup_deleted` from `pg_stat_database` for
  that database, plus `stats_reset`, so that a statistics reset is never read as "no changes".
  It is a query of the server catalog, not of Resolve tables (ADR 0007). Each backup stores the
  mark of its snapshot (`backups.activity_mark` and the manifest).
- Each check is recorded in the policy (`last_check_at`, `last_check_same`) and shown in the
  Schedule tab. It generates neither events nor jobs: 96 daily checks per library are not news.
- **A current backup is not an overdue backup.** The protection timeline and the "no verified
  backup in N hours" warning compare the current mark with the one of the last verified backup:
  if nothing changed, there is no amber band and no warning, and the badge says "3 days ago, no
  changes". Only the final stretch (from the last verified backup to now) is evaluated; the gaps
  between old backups are still drawn by time.
- New installations start in `activity` mode every 15 minutes; existing ones keep their cron
  until someone changes the policy.
- Each backup is still a full `pg_dump`. A true incremental backup is PITR (ADR in 2.0); here
  "differential" means not repeating what is already backed up.

**Consequences.** Migration 0003; `docs/07` §7 documents the query; `docs/11` §4 and §10,
`docs/09` and `docs/10` change in the same commit. PoC-10 decides whether Resolve writes while a
project is open without anyone editing; if it does, the activity-based mode backs up on every
check while a project is open, which is still the requested behaviour.
