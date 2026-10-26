# PoC-09. Restricted states and forced disconnect

Date: 2026-10-19. Server: mininas, sqsolve 0.1.5, PostgreSQL 18.6. Workstation: Linux, Resolve
Studio 21.1.1.10 with project `poc03_huella` open in the `apolo` library, operated with the
scripting API; what Resolve showed on screen was not observed.

## Procedure and literal results

| Step | Action in sqsolve | Resolve (API) |
|---|---|---|
| 1 | Maintenance ON without closing sessions (`ALLOW_CONNECTIONS false`) | `SaveProject` = `True`: the open session keeps working |
| 2 | Maintenance OFF | `SaveProject` = `True` |
| 3 | Replace `apolo` with its backup from minutes earlier, checking "close sessions" | Job `restore.replace` finished `succeeded` (pre-restore backup, restore aside, compare, swap) |
| 4 | Immediately after the swap | `GetCurrentProject` still returns `poc03_huella`; `SaveProject` = `True`; `GetProjectListInCurrentFolder` lists the projects |
| 5 | `SetCurrentDatabase(apolo)` and `LoadProject` again | `True`, `True`, `SaveProject` = `True` |

Reading: when its sessions are closed, Resolve 21.1.1 **reconnects by itself** to the database of
the same name, which is now the restored one, and keeps saving without warning through the API.
No dialog was seen because the workstation was not in view.

## Decisions it enables

- Maintenance mode without closing sessions does not interrupt whoever is already inside; to get
  someone out you have to check "close sessions". As planned.
- Replacement by swap with session closing works with Resolve open. **New risk:** since Resolve
  reconnects on its own and keeps the project in memory, an editor who had the project open can
  save over what was just restored without noticing. The guide must ask them to close the
  project before replacing and reopen it afterwards; sqsolve already asks for this in the form
  and now also in the README and `docs/11` §7.2. A technical safeguard (for example, a new
  password on replace) is left for 1.1.

## On screen (acceptance 1.2.2, 2026-10-26)

Diego repeated it by hand with Resolve Studio 21.1.1 build 10 on Fedora Linux 44, a project open
in the test library `riatidina` on mininas (sqsolve `sha-2ca757d`), three replacements and one
revert; details in `docs/acceptance/1.2.2.md`. Resolve showed no dialog when its sessions were
closed. Its log shows what happens at its next save: the query fails with `terminating
connection due to administrator command`, Resolve reconnects within milliseconds and saves the
project it has in memory into the database now under the name:

- after one replacement that save wrote three timeline items and a clip duration that were in
  neither the backup nor the database just before the replacement;
- after another one it failed with a foreign key error (`Sm2TiItem_Sm2TiTrack`) and Resolve
  reloaded the project on its own, which is when the restored contents appeared on screen;
- after the revert it saved only view state (playhead positions) and did not reload: the old
  contents stayed on screen until the project was closed and opened again.

## Status

VERIFIED through the scripting API and on screen. The risk above is real, so the replace and
revert pages ask to close the project in Resolve on every workstation first; the technical
safeguard is item 2 of `PLAN.md` §3.
