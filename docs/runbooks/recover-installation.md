# Runbook: recover an installation on another server

When: the server died or the `db` and `app` disk is gone, and you have the copy of the backups
volume and the recovery kit.

You need: the whole `backups` folder (including `_system/`) and the file
`sqsolve-recovery-<installation>.txt` with the master key.

1. On the new server, create empty `db` and `app` volumes and mount the backups copy as the
   `backups` volume. Use the same `deploy/compose.yaml` and the same image version, or a newer one
   of the same series.
2. `docker compose up -d`. Expected in `docker compose logs sqsolve-init`: it recognizes the
   volume marker and adopts the identity of the previous installation.
3. Open `https://<new-ip>:9432`. Expected: the setup wizard offers "Recover an existing
   installation".
4. Enter the installation code from the log and paste the master key from the kit. Expected:
   sqsolve restores its catalog from `_system/` and restarts.
5. Log in with your previous user. Each library appears as "No database on the server": on each
   one, "Recreate from the latest backup". Expected: one restore job per library and the Active
   state when it finishes.
6. Workstations keep their password (the master key is the same). Only the server address
   changes if it changed: Settings, "Address that workstations use", and in Resolve add the
   library again with the new IP.

Without a kit: install from scratch, `docker compose exec sqsolve sqsolve catalog rebuild --wait`
to see the backups, and restore them as new libraries; workstations need new passwords.

How to know it went well: `docker compose exec sqsolve sqsolve doctor` with no warnings; the
Health page in green; a project opens in Resolve from each library.
