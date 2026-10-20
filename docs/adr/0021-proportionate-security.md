# ADR 0021. Security proportionate to the local network and the VPN

**Status:** Accepted (2026-10-19). Diego's decision.

**Context.** sqsolve is meant for a studio LAN or a VPN (ADR 0015: port 5432 and the panel never
reach the Internet). With that model, the panel also asked to confirm the password every 5
minutes to reveal a library password, rotate it, add people, change network rules, delete,
purge, revert or replace; it required 12-character passwords and closed the session after 8
hours without use. That friction buys no security the network does not already give: whoever
reaches the panel is already inside the network, and the NAS's SSH is an easier target than the
UI. Diego summed it up on 2026-10-19: a user with a normal password and that's it.

**Decision.**

- **No reauthentication.** The "Confirm identity" screen, the `/confirm` route, `FreshAuth` and
  the `SQS-A007` error disappear. Whoever has the permission runs the action; each one is still
  recorded in the audit log with user and IP.
- **The confirmations against data loss stay:** typing the identifier to delete, purge, revert
  or replace, and `data-confirm` on the rest of the destructive actions. They are not security,
  they are a brake against mistakes.
- **Minimum password of 8 characters**, with at least 4 distinct ones, not containing the user
  name and not in the list of common passwords. The meter stays informational.
- **Long sessions:** 30 days without use, 180 days in total. They are closed from Account.
- **HTTPS mandatory in the panel.** `SQSOLVE_INSECURE_HTTP` still exists only for development
  and CI tests; it is not documented as a usage option.
- **Account lock after failed attempts.** On top of the per-IP and per-account throttling, ten
  wrong passwords in a row lock the account (`SQS-A004`), close its sessions and emit the
  `auth.locked` event. The panel does not unlock it: only `sqsolve admin unlock <user>` from the
  container console, or `admin reset-password`, which also clears the counter. Whoever can enter
  the container already owns the server; the panel has no reason to offer that way out.
- Kept: single-use installation code, login attempt limit, optional TOTP, roles, scoped tokens,
  CSRF, headers, credential encryption with the master key and the audit chain. None of this
  gets in the way of daily use.

**Consequences.** `docs/08` (T-10, T-12, permission matrix), `docs/09`, `docs/10` and `docs/12`
change in the same commit. The rule in `AGENTS.md` about secrets goes from "never without
reauthentication" to "only to whoever has the permission, with an audit entry". If one day the
panel is exposed outside the studio's network, this ADR is reviewed first.
