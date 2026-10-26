# Runbook: restore a backup and revert

When: a project, or the whole library, was deleted or broken, and there is a verified backup.

## A. Recover a project without touching the library

1. Backups, open the backup from the date you want. Verification must say "Checksum and index OK"
   or "Test restore OK".
2. "Export .drp and .drx", "Prepare the export". The export is experimental and off by default:
   if the page says so, enable it in Settings, General, Experimental. Wait for the job to finish
   (the job page shows it; a backup of several GB takes minutes).
3. Go back to the export page: download the project's `.drp` or the grades' `.drx`. Import them
   in Resolve. Expected: Resolve opens the project or applies the grade.
4. "Close and delete the temporary database".

If you would rather have that day's whole library next to the current one: "Restore", "As a new
library", another identifier, "Restore as new". Expected: it appears as Active with its own
password; connect it from Resolve with Connect.

## B. Replace the library with a backup

1. Ask everyone to close the project in Resolve (or quit Resolve) on every workstation of that
   library, and not to open it again until the job ends. "Close open sessions" in step 3 does
   not replace this: Resolve shows nothing when its sessions are closed, reconnects on its own,
   and its next save writes its pending changes into the restored library (PoC-09,
   `docs/acceptance/1.2.2.md`); sqsolve does not block that save.
2. Backups, open the backup, "Restore", the card "Replace" followed by the library's name.
3. Type the identifier to confirm and "Replace library". Without "Close open sessions" the job
   waits up to 10 minutes for the sessions to close and then fails with `SQS-R020`. Expected:
   one job with the steps "Check space", "Check", "Pre-restore backup", "Restore" (into a
   separate database), "Compare with the manifest", "Wait for sessions to close", "Lock
   connections", "Swap databases" and "Reopen". Its log ends with "Backed up the previous
   state ..." and it ends as "Succeeded". A library in maintenance stays in maintenance.
4. Ask everyone to open the project again in Resolve; the library entry and its password do not
   change. Expected: the projects as they were in the backup.

If the job fails before or at "Swap databases": the original library is still in place and
usable; check the job log and `sqsolve doctor`. `SQS-L052` means a session sqsolve cannot close
(a superuser's) is connected: close it and run the restore again.

If the job fails with `SQS-R054` (swapped, but reopening failed): the restored data is in place.
Put the library in maintenance and reopen it (Danger zone tab), which applies its access again,
or restart sqsolve. Expected: workstations connect.

If the job fails with `SQS-R053` (the swap stopped halfway and the original could not be put
back): nothing was dropped. The library shows "No database on the server" and the error names
the database holding the original (`<identifier>__prev_<mark>`). Use section C: Revert puts it
back under the library's name.

If Activity shows a "reconciliation pending" warning (`library.reconcile_pending`) after a
restart: sqsolve was stopped during a restore, revert, delete or recreate and could not settle
it at start. Nothing was changed or deleted: the library keeps its state (usually "Restoring")
and the databases the warning lists are kept. Fix what the warning names (for example close the
session that refused the rename) and restart sqsolve; every start tries again. Expected: the
warning stops and the library is Active (or in the trash, for a delete).

If the log says "Could not back up the previous state": the replacement worked, but the
previous version is only covered by the pre-restore backup. It is kept (not deleted when it
expires) until a backup of it succeeds; fix the cause (usually space on the backups volume)
and the next nightly purge job takes that backup before deleting it.

## C. Revert a replacement

Up to 7 days afterwards (Settings, General, "Previous state after restore (days)"):

1. Ask everyone to close the project in Resolve, as in section B step 1: a revert always closes
   the open sessions, and a project left open can save its pending changes into the reverted
   library.
2. Library, Danger zone tab, "Revert the last restore", "Revert". Type the identifier and
   "Revert".
3. Expected: a job that swaps back; the library is left as it was before the replacement (in
   maintenance if it was in maintenance). The version you leave is backed up and kept, so the
   revert can be reverted too.
4. Ask everyone to open the project again in Resolve.

For a library in "No database on the server" that still has a previous version (after an
`SQS-R053`, or if its database was lost), the same Revert renames the previous version back to
the library's name and the library becomes Active. The library page offers it next to
"Recreate from the last backup"; Recreate restores the last verified backup instead.

The previous version is deleted when its period expires only if there is a verified backup of
it; otherwise it is kept and a "previous state kept" warning (`SQS-R055`) appears in Activity.

## From the console

```
docker compose exec sqsolve sqsolve backup list <identifier>
docker compose exec sqsolve sqsolve restore <backup-id> --new <new-identifier> --wait
docker compose exec sqsolve sqsolve restore <backup-id> --replace --confirm <identifier> --wait
```
