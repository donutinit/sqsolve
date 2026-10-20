# PoC-04. Backup format and cross restore

Date: 2026-10-19. Server: mininas, sqsolve 0.1.6, PostgreSQL 18.6. Workstation: Linux, Resolve
Studio 21.1.1.10.

## Literal results

**Step 1, backup made by Resolve.** Not done: the Project Manager was not automated and there are
no `.resolve.backup` files on the workstation or on Diego's server to inspect.

**Step 2, Resolve restores an sqsolve backup.** Not done (Project Manager).

**Step 3, two libraries from the same backup on one workstation.** Partly done. Backup
`01M5BDTRHX...` of `apolo` (`pg_dump` custom format, L1 verified) was restored with sqsolve as
the new library `apolo_poc` (`pg_restore --no-owner --no-privileges --role=apolo_poc`), added to
Resolve through `.dblist` and, after restarting Resolve:

```
postgres project library apolo_poc at 192.168.50.172 version 18.6
Current project library changed to (apolo_poc : Network)
Current project pointer changed to (solo_en_copia) from project library (apolo_poc : Network)
```

Resolve opened the copy, listed its projects and created `solo_en_copia` in it while `apolo`
stayed in its list. The check that `apolo` did not receive that project was never run because of
the API lock described in `poc-01`.

In addition, sqsolve's L2 verification (test restore of each backup into a temporary database and
comparison with the manifest) passed on all of `apolo`'s backups of the day, and the export from
backup (temporary database `sqsolve_export_*`) read those same files.

**Step 4, compression.** Not done (requires restoring from Resolve).

## Decisions it enables

- An sqsolve backup restored by sqsolve is a library that Resolve 21.1.1 opens and uses: the
  chain backup, restore as new, open in Resolve is demonstrated.
- Whether Resolve directly accepts the sqsolve file in its own "Restore", and which format its
  "Backup" produces, remains REPORTED: it will be tested when Diego has an hour with the Project
  Manager. Until then, the README does not claim compatibility with Resolve's own backups.

## Status

VERIFIED: sqsolve's restore produces libraries that Resolve opens. REPORTED: file exchange with
Resolve's "Backup" and "Restore".
