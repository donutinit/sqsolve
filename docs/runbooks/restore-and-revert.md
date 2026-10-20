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
   into a separate database, compare with the manifest, swap. It ends as "Succeeded".
4. In Resolve, open the library again (Connect with the same details). Expected: the backup's
   projects.

If the job fails at "swap": the original library is still in place; check the job log and
`sqsolve doctor`.

## C. Revert a replacement

Up to 7 days afterwards (setting "Previous state after restore"):

1. Library, Danger zone tab, "Revert". Type the identifier.
2. Expected: a job that swaps back; the library is left as it was before the replacement.
3. Reconnect Resolve.

## From the console

```
docker compose exec sqsolve sqsolve backup list <identifier>
docker compose exec sqsolve sqsolve restore <backup-id> --new <new-identifier> --wait
docker compose exec sqsolve sqsolve restore <backup-id> --replace --confirm <identifier> --wait
```
