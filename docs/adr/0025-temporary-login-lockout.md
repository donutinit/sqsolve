# ADR 0025. Login lockout is temporary and spares live sessions

**Status:** Accepted (2026-10-20). Amends the "account lock" point of ADR 0021.

**Context.** The 2026-10-20 audit (finding M3, reproduced against the real router) showed that
any machine on the LAN could lock every panel account for good: ten anonymous wrong passwords
for a username locked the account, deleted all its sessions and made `Principal.Can` refuse
its API tokens. Lifting it needed `sqsolve admin unlock` from a container shell. On a NAS that
runs unattended, a single misbehaving script or a curious workstation could take the panel and
every automation that uses tokens away until someone opened a console. The same audit found
that each login ran a 64 MiB Argon2id with no cap and counted the failure only after hashing
(M2), so parallel bursts were neither bounded in memory nor counted by the rate limiter.

**Decision.**

- **The automatic lock expires.** Ten wrong passwords in a row still lock the account
  (`SQS-A004`, event `auth.locked`), now for 15 minutes. When the lock expires, the failure
  count goes back to zero. `sqsolve admin unlock <user>` still lifts it at once, and
  `sqsolve admin reset-password` also clears it.
- **Attempts against a locked account are refused before the rate limiter counts them.** They
  check no password, so the owner retrying during the lock leaves no wait behind and
  `sqsolve admin unlock` takes effect at once. The rate limiter's back-off from the wrong
  passwords themselves lives in the server's memory and runs its course on its own: seconds
  after one lock, at most 15 minutes after repeated ones.
- **Locking only refuses password logins.** It never deletes existing sessions and never
  disables API tokens. Whoever only knows a username can no longer log its owner out or break
  the studio's scripts. An explicit "Disabled" from Settings, Users, still closes every session
  and revokes every token.
- **The count survives parallel bursts.** The database increments the failure count
  (`failed_logins = failed_logins + 1`), and the per-IP and per-account rate limiter counts each
  attempt before the password is hashed: of twenty parallel guesses for one account, only the
  first five are hashed and the other fifteen get `SQS-A003`, as if they had come one after
  another.
- **Password hashing has a process-wide cap of 4 concurrent Argon2id computations.** A request
  that cannot get a slot within 2 seconds gets `SQS-A050` (HTTP 503 with `Retry-After` in the
  panel and the API). Nothing was checked, so it is not counted as a wrong password, although
  the rate limiter counts it as an attempt.
- No migration is needed: the expiry is computed from the existing `locked_at` column.

**Consequences.**

- Operators no longer need the container console after a burst of wrong passwords: waiting 15
  minutes is enough. The console remains the way to unlock sooner.
- An attacker gets at most ten guesses per account every 15 minutes from the account lock,
  on top of the rate limiter's exponential back-off (up to 15 minutes per IP and per account).
  The second factor stays the recommended defence for administrators.
- After a lock, the account owner keeps working in any browser where the panel is already open,
  and API tokens keep working. A user who is locked out and has no open session waits or asks
  for `sqsolve admin unlock`.
- The `auth.locked` event and the Users table say how long the lock lasts. The event stays
  critical: ten wrong passwords in a row still deserve attention.
- At most 4 times 64 MiB go to password hashing at any moment. During a large burst, some
  legitimate logins may get `SQS-A050` and succeed a few seconds later.
- `docs/08-security.md` (T-01 and section 4) and the user guide (lockout and troubleshooting)
  change in the same commit.
