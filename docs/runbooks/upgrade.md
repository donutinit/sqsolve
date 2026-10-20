# Runbook: upgrade sqsolve

When: a new version is released. With the compose `0.1` tag, the fixes of the series arrive on
their own; to change series (`0.2`) edit the `x-sqsolve` line.

1. Confirm in the panel that each library has a recent verified backup (protection line in green)
   or run "Back up now". Expected: no amber.
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

4. Expected in the log: `sqsolve-init` finishes, `sqsolve ready` with the new version and, if
   there was a migration, a prior copy of the catalog in `app/state-backups/`.
5. Log in to the panel: footer with the new version; Health in green; `sqsolve doctor` with no
   warnings.

Going back: put the exact previous version in `x-sqsolve` (for example `0.1.4`) and `docker
compose up -d`. If the new version migrated the catalog and the previous one does not start,
restore the copy from `app/state-backups/` with the service stopped.
