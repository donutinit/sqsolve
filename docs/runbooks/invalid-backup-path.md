# Runbook: a backup with an invalid path

When: a critical `backup.path_invalid` event ("N backups in the catalog have a path outside the
backups volume or not valid"), a backup page that shows `SQS-B043`, or a job that stops with it.

What it means: the catalog row of that backup names a file sqsolve never writes: an absolute
path, a path with `..`, a file that is not a `.backup` under `libraries/`, or a path that leaves
the backups volume through a symlink. sqsolve does not read, verify, restore, download or delete
that backup, retention leaves it alone, and the row is not rewritten. Normal operation does not
create such rows; they come from a modified control plane database, a manifest rebuilt by a
version before 1.2.2, or a symlink placed on the backups volume.

1. Read the IDs in the event (Activity > Events, or the `backup_ids` field of the event).
2. For each one, open `/backups/<id>` in the panel: the page shows the `SQS-B043` notice and the
   path of the row under "File". Then look at what is on disk, without changing anything:

   ```
   ls -lR <backups volume>/libraries/<identifier>/
   ```

   Look for symlinks (`->` in the listing) and for files that are not where the row says.
3. If a symlink on the volume leads elsewhere, find out who created it and why before touching
   it. sqsolve never creates symlinks there.
4. When the backup is real and you want to keep it, copy its `.backup` file and its manifest to
   `<backups volume>/_import/` and import it from the panel (Backups > Import): it gets a new,
   valid row. Do not move files inside `libraries/` by hand.
5. Remove the blocked row from the catalog. Only rows with an invalid path can be removed this
   way; no file is touched and the action is in the audit log (`backup.forget`):

   ```
   docker compose exec sqsolve sqsolve catalog forget <backup-id>
   ```

   Expected: `Backup <id> removed from the catalog; no file was touched.` Any other answer:
   `SQS-B044` means the path of that row is valid (use the panel instead); `SQS-X040` means the
   audit log cannot be written and nothing changed.
6. Run Settings > Maintenance > "Check the catalog". Expected: no new `backup.path_invalid`
   event.
