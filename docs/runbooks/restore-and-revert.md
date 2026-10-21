# Runbook: restore a backup and revert

When: a project, or the whole library, was deleted or broken, and there is a verified backup.

## A. Recover a project without touching the library

1. Backups, open the backup from the date you want. Verification must say "Checksum and index OK"
   or "Test restore OK".
2. "Export .drp and .drx", "Prepare the export". Wait for the job to finish (the job page shows
   it; a backup of several GB takes minutes).
3. Go back to the export page: download the project's `.drp` or the grades' `.drx`. Import them
   in Resolve. Expected: Resolve opens the project or applies the grade.
4. "Close and delete the temporary database".

If you would rather have that day's whole library next to the current one: "Restore", "As a new
library", with another identifier. Expected: it appears as Active with its own password; connect
it from Resolve with Connect.

## B. Replace the library with a backup

1. Ask everyone to close Resolve on all of that library's workstations, or check "close sessions"
   in step 3.
2. Backups, open the backup, "Restore", "Replace the current library".
3. Type the identifier to confirm. Expected: one job with the steps pre-restore backup, restore
   into a separate database, compare with the manifest, swap, reopen. Its log ends with "Backed
   up the previous state ..." and it ends as "Succeeded". A library in maintenance stays in
   maintenance.
4. In Resolve, open the library again (Connect with the same details). Expected: the backup's
   projects.

If the job fails before or at "swap": the original library is still in place and usable; check
the job log and `sqsolve doctor`. `SQS-L052` means a session sqsolve cannot close (a superuser's)
is connected: close it and run the restore again.

If the job fails with `SQS-R054` (swapped, but reopening failed): the restored data is in place.
Put the library in maintenance and reopen it (Danger zone tab), which applies its access again,
or restart sqsolve. Expected: workstations connect.

If the job fails with `SQS-R053` (the swap stopped halfway and the original could not be put
back): nothing was dropped. The library shows "No database on the server" and the error names
the database holding the original (`<identifier>__prev_<mark>`). Use section C: Revert puts it
back under the library's name.

If the log says "Could not back up the previous state": the replacement worked, but the
previous version is only covered by the pre-restore backup. It is kept (not deleted when it
expires) until a backup of it succeeds; fix the cause (usually space on the backups volume)
and the next nightly purge job takes that backup before deleting it.

## C. Revert a replacement

Up to 7 days afterwards (setting "Previous state after restore"):

1. Library, Danger zone tab, "Revert". Type the identifier.
2. Expected: a job that swaps back; the library is left as it was before the replacement (in
   maintenance if it was in maintenance). The version you leave is backed up and kept, so the
   revert can be reverted too.
3. Reconnect Resolve.

For a library in "No database on the server" that still has a previous version (after an
`SQS-R053`, or if its database was lost), the same Revert renames the previous version back to
the library's name and the library becomes Active. The library page offers it next to
"Recreate from the last backup"; Recreate restores the latest backup instead.

The previous version is deleted when its period expires only if there is a verified backup of
it; otherwise it is kept and a "previous state kept" warning (`SQS-R055`) appears in Activity.

## From the console

```
docker compose exec sqsolve sqsolve backup list <identifier>
docker compose exec sqsolve sqsolve restore <backup-id> --new <new-identifier> --wait
docker compose exec sqsolve sqsolve restore <backup-id> --replace --confirm <identifier> --wait
```
