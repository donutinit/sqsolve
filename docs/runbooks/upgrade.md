# Runbook: upgrade sqsolve

When: a new version is released. With the compose `1` tag, every 1.x release arrives on its own
(TrueNAS: "Update available"). An installation still on a series tag (`1.1`, `1.2`) moves to
`1` once by editing the `x-sqsolve` line; on TrueNAS the image appears three times in the stored
YAML and all three change (user guide, "Updating").

1. Confirm in the panel that each library has a recent verified backup (protection line in green)
   or run "Back up now", and take a control-plane backup (Settings, Maintenance, "Back up the
   control plane", "Run"). Expected: no amber, and the job succeeded.
2. Read the version's section in `CHANGELOG.md`: it says whether there is a catalog migration and
   how to go back.
3. Docker Compose:

   ```
   docker compose pull
   docker compose up -d
   docker compose logs -f sqsolve
   ```

   TrueNAS: the app shows "Update available" (up to 24 hours after the image is published,
   because TrueNAS checks once a day) and the Update button does the same. From the NAS console:
   `sudo midclt call -j app.pull_images sqsolve '{"redeploy": true}'`.

4. Expected in the log: `sqsolve-init` finishes with `done`, `sqsolve ready` with the new version
   and, if there was a migration, a prior copy of the catalog in `app/state-backups/`
   (`sqsolve-before-<migration>-<time>.db`). The first start of a version with ADR 0023 marks the
   `db` and `app` volumes silently; if `sqsolve-init` stops with `SQS-S011`, `SQS-S012` or
   `SQS-S013`, a disk is not mounted: mount it and run `docker compose up -d` again; with
   `SQS-S019` two host paths are swapped: fix them.
5. Log in to the panel: footer with the new version; Health in green; `sqsolve doctor` with no
   warnings.

Going back: first read the version's rollback note in `CHANGELOG.md`. Going from a version with
ADR 0024 to 1.1.2 or older: while the new version still runs, reopen every library in maintenance
(Danger zone, Reopen). Older versions do not give the library roles `LOGIN` back when
maintenance ends.

Then put the exact previous version in `x-sqsolve` (for example `1.1.0`; on TrueNAS, in the
three places of the stored YAML) and run `docker compose up -d`. If the new version migrated the
catalog and the previous one does not start, restore the copy with the service stopped:

```
docker compose stop sqsolve
# in the app folder:
mv sqsolve.db sqsolve.db.new
[ ! -f sqsolve.db-wal ] || mv sqsolve.db-wal sqsolve.db.new-wal
[ ! -f sqsolve.db-shm ] || mv sqsolve.db-shm sqsolve.db.new-shm
cp -p state-backups/sqsolve-before-<migration>-<time>.db sqsolve.db
docker compose up -d
```

Move `sqsolve.db-wal` and `sqsolve.db-shm` together with `sqsolve.db`: they belong to the
database that was replaced (the `.new` file keeps them), and SQLite would apply them to the copy.
`cp -p` keeps the owner of the copy (the sqsolve user); if it changed, add
`SQSOLVE_FIX_PERMS: "true"` to `sqsolve-init` for that start.

Then rename the copy you restored, for example to `sqsolve-before-<migration>-<time>.db.used`.
sqsolve takes only one copy per migration, so the next upgrade would otherwise keep this old copy
instead of taking a new one. Changes made since the upgrade are lost with the copy; Settings,
Maintenance, "Rebuild the catalog" brings back the backups taken since.
