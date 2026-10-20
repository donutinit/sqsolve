# PoC-01. Lifecycle and minimum privileges

Date: 2026-10-19. Server: mininas, sqsolve 0.1.5 and 0.1.6, PostgreSQL 18.6. Workstation: Linux,
Resolve Studio 21.1.1.10. Operated through Resolve's scripting API and, to add libraries without
going through the Project Manager, by editing `~/.local/share/DaVinciResolve/configs/.dblist`
(format `key:ip:user:password:database:QPSQL`, read when Resolve starts). What the Project
Manager's "Create" or "Connect" button requires was not automated.

## Literal results

**Step 1, what Resolve creates with "Create" (E1).** Diego created `apolo` from the Project
Manager inside sqsolve's initialization window. Final state in the catalog:

| Item | Value |
|---|---|
| Database owner | `apolo` (the library role) |
| Encoding, provider, locale | `UTF8`, builtin, `C.UTF-8` (inherits the cluster template) |
| Extensions | only `plpgsql` |
| `public` schema | 136 tables, 2 sequences, 444 indexes, 11 `plpgsql` functions, a single owner (`apolo`) |
| Functions | `check_if_upgrade_allowed`, `del_orphan_still_on_ref_die`, `delete_stills_if_unreferenced`, `dropemptytable`, `find_if_still_is_referenced`, `increment_coversiontable_timestamp_on_update`, `insert_update_render_blob`, `print_notice`, `query_emptytables`, `query_vertabledata`, `unlock_vertable` |

sqsolve detected the database, closed the window, removed `CREATEDB` from the role
(`rolcreatedb = f`) and activated it.

**Step 2, "Connect" to an empty database (E0).** Library `labvacia` created by sqsolve with the
"Empty database" strategy and added to Resolve with "Connect" (entry in `.dblist`). Resolve log
at startup:

```
Project library labvacia192.168.50.172 is incompatible with the running application (expecting db version 18.1.0.004)
```

Resolve does not initialize an empty database on connect: it marks it incompatible and does not
use it. E0 does not work with 21.1.1; a precreated database would need sqsolve to load Resolve's
structure (E2, templates, 1.1). The "Empty database" option in the panel is marked as not
compatible.

**Step 3, database owned by `sqsolve_admin`.** Not done: it requires "Create" in the Project
Manager.

**Step 4, daily use without `CREATEDB`.** VERIFIED all day with `apolo`: creating, opening,
saving, closing and deleting projects and timelines with the role without `CREATEDB`.

**Step 5, member role (`apolo_poc__ana`).** The credential was created from the panel and the
`.dblist` entry was ready, but the battery stopped before that test (the scripting API stopped
responding after creating a project in another library). Not done.

**Step 6, connections per workstation.** `pg_stat_activity` sampled every 2 s for 40 s with a
project open: 1 connection. With no project open: 1. When switching library or loading a
project: a burst of 6 connections (`application_name` `Resolve_0007433F5FC0`) that close
afterwards. During a render: not measured.

**Step 7, accent ordering.** Inconclusive: the API returns the list from a cache and does not
reflect newly created projects.

**Step 8, Resolve 20.3.2.** Not available on the workstation.

## Decisions it enables

- Creation strategy: **E1 (Resolve creates the database) is the only one supported in 1.0.** The
  database owner is the library role; sqsolve administers with `sqsolve_admin` through
  membership, as implemented.
- Minimum privileges in daily use: `LOGIN` without `CREATEDB`, confirmed.
- `max_connections`: an idle workstation uses 1 connection and up to 6 in bursts; sqsolve's
  default value is more than enough for a small studio.
- Per-person credentials (SQS-700): pending a test with Resolve.

## Status

VERIFIED: steps 1 (final result), 2 and 4, and the idle case of step 6. NOT DONE: steps 3, 5, 7,
8 and the render of 6; they need the Project Manager or a second version of Resolve.
