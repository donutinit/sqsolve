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

## Status

VERIFIED through the scripting API. Pending observation on screen during acceptance (AC-08).
