# Runbook: recover an installation on another server

When: the server died or the `db` and `app` disk is gone, and you have the copy of the backups
volume and the recovery kit.

You need: the whole `backups` folder (including `_system/` and its marker `.sqsolve-volume`) and
the file `sqsolve-recovery-<installation>.txt` with the master key.

1. On the new server, create empty `db` and `app` volumes and mount the backups copy as the
   `backups` volume. Use the same `deploy/compose.yaml` and the same image version, or a newer one
   of the same series. If the new server runs sqsolve with another UID (`SQSOLVE_UID`, `user:`),
   or the copy kept the owners of the old server, add `SQSOLVE_FIX_PERMS: "true"` to the
   `sqsolve-init` service for this first start and remove it afterwards.
2. `docker compose up -d`. Expected in `docker compose logs sqsolve-init`: `adopting identity
   <installation> from the backups volume (recovery of a previous installation)`, then `done`.
   Init marks the new `db` and `app` volumes with that identity (ADR 0023). If it stops with
   `SQS-S013`, the `db` volume is not empty (it already holds a cluster): use an empty one, or
   see "Only the app disk was lost" below.
3. Open `https://<new-ip>:9432`. Expected: the setup wizard offers "Recover an existing
   installation".
4. Enter the installation code from the log and paste the master key from the kit. Expected:
   sqsolve restores its catalog from `_system/` and restarts. The restored catalog forgets the
   PostgreSQL cluster of the old server and learns the new one at that start; no
   `postgres.cluster_changed` event.
5. Log in with your previous user. Each library appears as "No database on the server": on each
   one, type its identifier and "Recreate from the last backup". Expected: one restore job per
   library (clicking twice does not queue a second one) and the Active state when it finishes.
6. Workstations keep their password (the master key is the same). Only the server address
   changes if it changed: Settings, "Address that workstations use", and in Resolve add the
   library again with the new IP.

Only the app disk was lost (the `db` volume still holds the cluster): `sqsolve-init` stops with
`SQS-S013` so it does not create new secrets by mistake. If that is what you want, add
`SQSOLVE_APP_ADOPT: "true"` to `sqsolve-init` for one start, then follow steps 3 to 6; remove the
variable afterwards.

Without a kit: install from scratch, `docker compose exec sqsolve sqsolve catalog rebuild --wait`
to see the backups, and restore them as new libraries; workstations need new passwords.

How to know it went well: `docker compose exec sqsolve sqsolve doctor` with no warnings (the
three volume markers belong to the installation); the Health page in green; a project opens in
Resolve from each library.
