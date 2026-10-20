# User guide

Everything an administrator needs to install, run and recover sqsolve. The README is the short
version; this is the long one. Error codes are listed in [errors.md](errors.md); step-by-step
procedures with expected output are in the [runbooks](../runbooks).

## Contents

1. [How it is built](#how-it-is-built)
2. [Requirements](#requirements)
3. [Installation](#installation)
4. [First start](#first-start)
5. [Libraries and Resolve workstations](#libraries-and-resolve-workstations)
6. [Backups](#backups)
7. [Restore](#restore)
8. [Access and security per library](#access-and-security-per-library)
9. [Panel users, notifications and API](#panel-users-notifications-and-api)
10. [Export projects and grades](#export-projects-and-grades)
11. [Console](#console)
12. [Updating](#updating)
13. [Moving to another server or recovering from a disaster](#moving-to-another-server-or-recovering-from-a-disaster)
14. [Troubleshooting](#troubleshooting)

## How it is built

```
 Resolve workstations ──TCP 5432──▶ postgres   (official PostgreSQL 18 image)
                                        ▲ internal socket
 Browser ───────────────HTTPS 9432──▶ sqsolve    (panel, API, backups, jobs)
                                        │
             volumes: db (SSD) · app (SSD) · backups (another disk)
```

- **postgres**: the official `postgres:18.6-trixie` image, pinned by digest. Workstations connect
  here.
- **sqsolve**: a single Go binary on that same image, so `pg_dump` and `pg_restore` always match
  the server version. Runs unprivileged, with a read-only file system and no Linux capabilities.
  Talks to PostgreSQL only through the internal socket.
- **sqsolve-init**: runs once at every start, without network. Prepares folders, permissions,
  secrets and the PostgreSQL configuration, then exits.

Three folders of yours (ADR 0018):

| Volume | Inside the containers | What it holds | Where to put it |
|---|---|---|---|
| db | `/var/lib/postgresql` | PostgreSQL data and the configuration sqsolve generates | SSD |
| app | `/var/lib/sqsolve` | Catalog, panel users, secrets and the panel certificate | SSD |
| backups | `/backups` | Backups, manifests and catalog copies | **Another disk**, so data and backups are not lost together |

## Requirements

- Linux with Docker Engine and Docker Compose v2, on `amd64` or `arm64`. A NAS that accepts
  Compose works (TrueNAS with "Install via YAML", Portainer, Unraid), as does any Linux server.
- Ports 5432 (workstations) and 9432 (panel) free on that machine.
- Workstations on the same LAN or connected through the studio VPN. **Never publish 5432 or
  9432 to the Internet.**
- Space: your libraries in `db`, plus several times their compressed size in `backups`,
  depending on how many backups you keep.

Tested in CI with Docker Compose on Ubuntu, with user 1000 and with 568 (the TrueNAS apps
user). On TrueNAS itself: tested on TrueNAS 25.10 as a Custom App.

## Installation

### 1. Create the folders

Three empty folders: two on SSD and the backups one preferably on another disk. For example:

```
/mnt/ssd/sqsolve/db
/mnt/ssd/sqsolve/app
/mnt/disks/sqsolve-backups
```

On TrueNAS use one dataset per folder. The tested preset is **Generic** (POSIX permissions).
With other presets the dataset uses NFSv4 ACLs: `sqsolve-init` checks that the PostgreSQL folder
ends up with the permissions PostgreSQL demands and, if not, stops with error `SQS-C054`.

### 2. Adjust the compose file

Copy [`deploy/compose.yaml`](../../deploy/compose.yaml) and change only this:

1. The three paths marked `Volumes:` (each appears in several services; change them all the
   same way).
2. `TZ`, your time zone (for example `America/Mexico_City`). Backup schedules use this zone.
3. The image version in `x-sqsolve`. With `1.1` you receive every fix of the 1.1 series and
   TrueNAS tells you with "Update available"; with an exact version (`1.1.0`) nothing changes
   until you change it. Do not use `edge`: it is the latest of `main`, unreleased.
4. If port 5432 is already taken on the host (it happens on Synology), publish it only on the
   LAN IP: `"192.168.1.10:5432:5432"`.

If your NAS requires another user for apps (568 on TrueNAS), change **all three** together:
`SQSOLVE_UID`, `SQSOLVE_GID` and the `user:` line of the `sqsolve` service. If they do not match,
sqsolve refuses to start and tells you which one to change (error `SQS-C053`).

### 3. Bring it up

```
docker compose -f compose.yaml up -d
docker compose -f compose.yaml ps
```

On TrueNAS: Apps, "Install via YAML", paste the edited file.

### 4. Find the installation code

```
docker compose -f compose.yaml logs sqsolve
```

You will see a box with a code like `ABCDE-FGHJK`. It only works until you create the first
administrator; if you restart the container before that, a new one is printed.

## First start

1. Open `https://<server-ip>:9432`. The browser will warn that the certificate is not trusted:
   that is normal, sqsolve signed it itself. Accept the warning. Later you can upload your own
   certificate in Settings.
2. The wizard asks for:
   - the installation code;
   - your username and a password of at least 8 characters;
   - **the address workstations use** to reach the server (its LAN IP or a host name). It is
     the one shown in the connection instructions;
   - the installation name (shown in the panel and in notifications);
   - the default backup schedule for new libraries: activity-based (recommended), daily at
     02:00, every 4 hours from 09:00 to 21:00 Monday to Saturday, or every 4 hours every day.
     Each library can be changed later.
3. **Download the recovery kit** and keep it off the server (password manager, a USB stick in a
   drawer). It holds the master key that encrypts the library passwords. Without it, on another
   server you recover the projects but the workstations need new passwords. You can download it
   again in Settings.

Optional: second factor (TOTP) in "Account". The panel is always HTTPS.

## Libraries and Resolve workstations

### Create a library

1. Dashboard, "New library". Type a name (for example "Client X, season 2"). sqsolve proposes an
   identifier (`client_x_season_2`): lowercase, digits and underscore, 3 to 40 characters. The
   identifier is the database and user name in PostgreSQL and does not change later; the visible
   name does.
2. An **initialization window** of 60 minutes opens. On **one workstation only**:
   - open Resolve's Project Manager and add a network (PostgreSQL) project library with
     **Create**;
   - as the library name type **exactly the identifier**;
   - as location, the server IP or host name (no port);
   - the user and password the panel shows ("Reveal password" is recorded in the audit log).
3. Resolve creates the database and its tables. sqsolve detects it within seconds, closes the
   window, removes the permission to create databases and takes the first backup. The library
   becomes "Active".

If a different name was typed in Resolve, the panel tells you which database was created and
which one it expected. If the window expires, "Open another window".

The exact names of buttons and fields change between Resolve versions. The flow "Create the
first time, Connect on the others" was verified with Resolve Studio 21.1.1 on Linux.

### Connect the other workstations

On each workstation: add the network library with **Connect** and the same data. The
"Instructions" page of each library has everything ready to print or send. Resolve does not ask
for the port: it uses 5432. For live collaboration every workstation needs Resolve Studio on the
same version and TCP ports 50059 to 50314 open **between workstations** (that traffic does not
go through sqsolve).

### Maintenance, trash and purge

- **Maintenance**: the library stops accepting new connections; you can terminate the open ones.
- **Delete**: takes a final backup, moves the library to the trash for 30 days and takes it off
  the network. It can be brought back from the trash.
- **Purge**: drops the database and its users for good (backups stay according to retention).
  Asks you to type the identifier.

## Backups

- **Activity-based by default**: every 15 minutes sqsolve checks whether the library changed
  since the last backup and only then backs up. While someone works you get one per check; if
  nobody touches it, none, and the last one still counts as current. If you prefer fixed hours,
  each library accepts a cron expression (Schedule tab). Small random offset per library so they
  do not all start at once. Also "Back up now".
- Each backup is a `pg_dump` in custom format taken from a consistent snapshot of the database,
  with exact row counts in that same snapshot, a SHA-256 checksum and a JSON manifest next to it.
  It is written as `.partial` first and only renamed when complete.
- **Verification**: every backup is checked when it finishes (checksum and readable index). Every
  Sunday at 05:00 a **test restore** of the latest backup of each library runs in a temporary
  database and compares structure and rows with the manifest. You can request it by hand on any
  backup.
- **Retention** per library: by default the last 10, one per day for 14 days, one per week for 8
  weeks and one per month for 12 months, and the last verified one is never deleted. Pinned
  backups and those taken before a restore or delete are protected. The Schedule tab shows how
  many would be kept before saving.
- **Protection timeline**: every library is a track and every backup a marker (tall if the test
  restore passed, medium if checksum and index passed, dotted if unverified, a red cross if it
  failed). The amber band marks time without a verified backup (48 hours by default), unless the
  library has not changed since the last one: then it is still protected.
- **On disk**: `backups/libraries/<identifier>/<year>/<month>/<identifier>_<date>_<id>.backup`
  plus its `.manifest.json`. They are standard files: `pg_restore` can read them even if sqsolve
  is gone.
- **Export from a backup**: on any backup's page, "Export .drp and .drx" restores it into a
  temporary database and lets you download projects and grades exactly as they were that day,
  without touching the library (experimental; enable it in Settings).
- **Download** a backup from its page. **Import** one from elsewhere (another server, the
  official Project Server, a download): upload it from "Import", or copy it to `backups/_import/`
  and pick it from the list (better for multi-GB files; if you tick "delete the original" it is
  moved instead of copied).

If the backups disk gets unmounted, sqsolve notices (every volume carries a marker) and does not
write backups into the wrong empty folder.

## Restore

From a backup's page, "Restore":

- **As a new library** (recommended): creates another library with its own user and password.
  The original is untouched. Use it to recover a deleted project without affecting anyone.
- **Replace the current library**: first backs up the library as it is now, restores into a
  separate database, compares it with the manifest, waits for Resolve to close (or terminates the
  sessions if you tick it) and swaps the databases. The previous version is kept for 7 days to
  **revert** from the Danger zone tab. Asks you to type the identifier. Important: ask anyone
  with a project open to close it first and reopen it afterwards; Resolve reconnects on its own
  after the swap and, if it saves, writes over what was restored (PoC-09).

If sqsolve or the server die mid-restore, at start the job journal is read and the library is
left in a consistent state.

## Access and security per library

- **One password per person** (Access tab): creates users like `client_x_season_2__maria`. What
  that person creates belongs to the library, and you can revoke them without changing
  everybody's password.
- **Rotate password**: the workstations using it will have to type the new one.
- **Network rules**: limit the networks that can reach that library (for example
  `192.168.10.0/24`). Without rules, any address that reaches the port can connect. On the LAN
  PostgreSQL sees each workstation's real IP; with the TrueNAS Tailscale app everything that
  comes through the VPN arrives with the Tailscale container's IP (`172.16.10.1` in the test
  installation), so that is the address to allow.
- **Sessions**: who is connected now, from where and whether the connection is encrypted; you
  can terminate a session.
- **TLS only** (Settings, Security): rejects unencrypted Resolve connections. Resolve uses TLS on
  its own when the server offers it, and sqsolve always does; verified with Resolve 21.1.1 on
  Linux, also with the mode on. It is the recommended option, but it is not known how every
  Resolve version on macOS and Windows implements it: before enabling it, check that your
  workstations show as encrypted in Sessions and test a new connection afterwards. If a
  workstation can no longer connect, turn it off.
- The administrative PostgreSQL users (`postgres` and `sqsolve_admin`) do not accept network
  connections, only the internal socket.
- Library passwords are stored encrypted with the master key and shown only to administrators;
  every reveal is recorded in the audit log.

## Panel users, notifications and API

- **Roles**: administrator (everything), operator (backups, restore as new, maintenance) and
  viewer (read only). Settings, Users.
- **Second factor** TOTP with 10 recovery codes, in Account.
- **Lockout**: ten wrong passwords in a row lock the account; only `sqsolve admin unlock
  <user>` from the container console lifts it.
- **Notifications**: Settings, Notifications: webhook, ntfy or email (SMTP). sqsolve sends nothing
  off the server until you add a channel.
- **Activity**: events and a hash-chained audit log, exportable as JSONL.
- **Health**: PostgreSQL, space of every volume, certificates and the server's databases.
- **REST API** at `/api/v1`, with tokens you create in Account (scopes: read, backup, restore,
  export, admin). Errors in RFC 9457 format. Examples:

```
TOKEN=sqs_...
curl -k -H "Authorization: Bearer $TOKEN" https://server:9432/api/v1/libraries
curl -k -X POST -H "Authorization: Bearer $TOKEN" https://server:9432/api/v1/libraries/<id>/backups
curl -k -H "Authorization: Bearer $TOKEN" https://server:9432/api/v1/jobs/<id>
```

`GET /api/v1/system/health` needs no token and answers 503 if PostgreSQL or the backups volume
are not healthy: useful for an external monitor.

## Export projects and grades

Experimental and off by default (Settings, Experimental). The Export tab of a library and the
"Export .drp and .drx" page of a backup produce `.drp` files (a complete project, without stills)
and `.drx` files (a grade: node tree and thumbnail of a still) by reading the library, without
opening Resolve. It reads tables Blackmagic does not document: **no guarantee**. Import every
file into Resolve before trusting it. Details in
[22-project-and-grade-export.md](../22-project-and-grade-export.md).

The catalog has two views, Projects and Stills, each with a search box. In Projects, the still
count of a row takes you straight to that project's stills, expanded; in Stills, search by
project, album or still label and the matching albums open on their own.

## Console

Inside the container (actions are recorded in the audit log with actor "console"):

```
docker compose exec sqsolve sqsolve doctor                     # checks everything and says what to do
docker compose exec sqsolve sqsolve library list
docker compose exec sqsolve sqsolve backup create <identifier> --wait
docker compose exec sqsolve sqsolve backup list <identifier>
docker compose exec sqsolve sqsolve restore <backup-id> --new <new-identifier> --wait
docker compose exec sqsolve sqsolve admin list
docker compose exec sqsolve sqsolve admin reset-password <user>
docker compose exec sqsolve sqsolve admin disable-totp <user>
docker compose exec sqsolve sqsolve admin unlock <user>         # after ten wrong passwords in a row
docker compose exec sqsolve sqsolve catalog rebuild --wait     # rebuilds the catalog from the manifests
docker compose exec sqsolve sqsolve audit verify
```

`sqsolve help` lists every command.

## Updating

1. First, back everything up (or confirm in the panel that everything has a recent verified
   backup).
2. With the `1.1` tag, pulling the new image is enough; with an exact version, change it in the
   compose file first. Then:

```
docker compose pull
docker compose up -d
```

On TrueNAS, with `1.1`, the app shows "Update available" when a fix is released and the Update
button does the same as those two commands. TrueNAS compares images with the registry once a
day, so the notice can take up to 24 hours; to skip the wait, from the NAS console:
`sudo midclt call app.image.op.check_update` forces the check, or directly
`sudo midclt call -j app.pull_images sqsolve '{"redeploy": true}'`, which is the Update button.
To move to another series (`1.2`) change the line by hand.

`sqsolve-init` runs again and regenerates the configuration. If the new version changes the
catalog, sqsolve saves a copy first in `app/state-backups/`. To go back, return to the previous
version; if it already migrated, restore that copy.

The PostgreSQL image is pinned by digest in the compose file; minor updates arrive with new
sqsolve versions. Changing the PostgreSQL major version is not automated yet (ADR 0017).

## Moving to another server or recovering from a disaster

You need **the copy of the backups volume** and **the recovery kit**.

1. On the new server, install as in [Installation](#installation), but mount the copy of the
   backups as the `backups` volume and leave `db` and `app` empty.
2. `sqsolve-init` recognizes the volume and adopts the identity of the previous installation.
3. In the wizard choose "Recover an existing installation", type the installation code and paste
   the master key from the kit. sqsolve restores its catalog from `_system` and restarts.
4. Every library shows "No database on the server": "Recreate from the latest backup". With the
   same master key, workstations keep their password.

Without the kit, install from scratch, run `sqsolve catalog rebuild` to see the backups and
restore them as new libraries (with new passwords). Verified end to end on 2026-10-19
([acceptance record](../acceptance/1.0.md)).

## Troubleshooting

First: `docker compose exec sqsolve sqsolve doctor` and the Health page. Errors carry an
`SQS-...` code that also appears in the API; all of them are listed in [errors.md](errors.md).

| What happens | Why | What to do |
|---|---|---|
| The installation code does not appear | There is already an administrator, or you looked at another container's log | `docker compose logs sqsolve`. If there is an administrator and you cannot log in: `sqsolve admin reset-password` |
| `SQS-C053` at start | `user:` of the `sqsolve` service does not match `SQSOLVE_UID`/`SQSOLVE_GID` | Put the same numbers in the three places and bring it up again |
| `SQS-S004` in `sqsolve-init` | The backups folder has files that are not sqsolve's | Mount an empty folder. If it really is the right one, `SQSOLVE_BACKUPS_ADOPT=1` |
| `SQS-S003` | That backups volume belongs to another installation | Check that you mounted the right folder |
| Permission errors after changing user or copying folders | Owners do not match | Add `SQSOLVE_FIX_PERMS: "1"` to the `sqsolve-init` service, bring it up once and remove it |
| `SQS-C054` in `sqsolve-init`, or PostgreSQL restarts with `mkdir: cannot create directory '/var/lib/postgresql/18': Permission denied` or `data directory ... has invalid permissions` | The `db` folder uses NFSv4 ACLs and does not keep the permissions PostgreSQL demands | Create the `db` folder on a dataset with POSIX permissions (on TrueNAS, the Generic preset) |
| `port is already allocated` on 5432 | Another PostgreSQL on the host (Synology) | Publish `"LAN-IP:5432:5432"` |
| Resolve does not connect | Network, firewall, address or network rule | From the workstation, reach `IP:5432`; check the advertised address in Settings and the library's network rules; the `postgres` log says why it refused (`pg_hba.conf rejects connection for host ...`) |
| Refusals with a `172.x` IP in the `postgres` log | Your Docker rewrites the source IP | Network rules do not see the workstation's real IP: remove them or allow that network |
| The library does not activate | The name in Resolve was not the identifier, or the window expired | The panel shows the database that was created; reopen the window and create with the exact name |
| `SQS-R020` when replacing | Resolve is still open on that library | Ask them to close it or tick "terminate sessions" |
| `SQS-B012` | No space on the backups disk | Free space or adjust retention |
| `SQS-L046` when purging | The library user still owns another database (for example, one created with the wrong name) | Check that database and drop it by hand if it is useless |
| `SQS-A003` | Too many login attempts | Wait the time shown |
| `SQS-A004` | Ten wrong passwords in a row: the account is locked | `sqsolve admin unlock <user>` from the container console; the panel does not unlock it |
| Lost second factor | | `sqsolve admin disable-totp <user>` |
| The panel says "PostgreSQL not responding" | The `postgres` container is stopped or restarting | `docker compose ps` and `docker compose logs postgres`; sqsolve reconnects on its own |

If you need help, Settings, Maintenance has a diagnostic bundle without secrets.
