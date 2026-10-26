# 01. Research

Cutoff date: 2026-10-18. The numbered references [n] are at the end. Items that a proof of
concept (`02-proofs-of-concept.md`, reports in `docs/poc/`) later answered, or left open because it
did not run, say so; everything else is as of the cutoff.

## 1. Method

- Primary sources: the official DaVinci Resolve 21.x release notes on the Blackmagic Design
  support site [1][2][3][4], the Resolve reference manual (database and collaboration
  chapters) [5], the PostgreSQL documentation [12][13][14][15][16] and the documentation of
  the official PostgreSQL image on Docker Hub [17].
- Secondary sources: Blackmagic support answers on its forum [6][7][8], free projects that
  already package PostgreSQL for Resolve [9][10][11] and the public Studio Server
  documentation [18][19][20][21].
- The Blackmagic forum blocks automated downloads; its quotes come from the indexed snippets
  and are marked REPORTED until reproduced.
- No code, binaries or schemas from Blackmagic Design or WireBear were used, nor will they be.

## 2. What a project library is

Resolve stores projects in libraries. There are three kinds:

| Kind | Where it lives | Relevance to sqsolve |
|---|---|---|
| Local (formerly "Disk database") | A folder on the workstation | None: it is not shared over the network [5] |
| Network | A PostgreSQL database on a server | The object sqsolve manages |
| Blackmagic Cloud | Service hosted by Blackmagic | Out of scope: proprietary service [22] |

Since Resolve 18 the interface and the Project Server call "project libraries" what used to be
"databases"; each network library is an independent PostgreSQL database [11].

## 3. Blackmagic's official Project Server

According to the manual [5], "DaVinci Resolve Project Server" is a standalone application that
is installed alongside Resolve on macOS and Windows and lets you create, manage, back up,
restore and share libraries without opening Resolve. Its documented functions:

- **Enable Sharing / Disable Sharing**: configures the local PostgreSQL to accept network
  connections and "authorizes" that configuration.
- **New Database**: creates libraries without opening Resolve.
- **Optimize**: PostgreSQL only; runs "vacuum" and reindexes.
- **Backup / Restore**: exports and imports `.resolve.backup` files.
- **Create Access Key**: exports a `.resolvedbkey` file to connect another workstation.

Server requirements according to the same manual: local network, fast connections, the server
does not need a fast GPU and "does not need a dongle or serial number, nor the Studio version"
[5].

For Linux, the manual describes a manual `pg_hba.conf` configuration (for Blackmagic's CentOS
systems) with a line `host all all <subnet> md5` [5]. WireBear states that Blackmagic does not
offer a Linux version of Project Server, and that this is why Studio Server exists [18]
(REPORTED).

## 4. PostgreSQL versions by Resolve version

| Resolve | PostgreSQL | Status | Source |
|---|---|---|---|
| up to 12.5.3 (macOS) | 8.4 | VERIFIED | Manual [5] |
| 12.5.4 to 17 | 9.5 | VERIFIED | Manual [5]; community [11] |
| 17 and earlier with PostgreSQL 12 or later | Do not work | REPORTED | [9] says they use a function removed in PostgreSQL 12; the removal of `WITH OIDS` in 12 is the likely explanation [16] |
| 18, 19, 20 | 13 (the Project Server 20 installer installs 13.4) | REPORTED | Blackmagic support on the forum [6] |
| 21.1 | Project Server ships PostgreSQL 18 | VERIFIED | 21.1 release notes [1] |
| 21.1 | "Resolve and Project Server can keep working with network libraries based on PostgreSQL 13 and later" | VERIFIED | 21.1 release notes [1] |

Other relevant facts:

- Project Server installers do not upgrade or modify an already installed PostgreSQL; they only
  update the application (REPORTED, Blackmagic support [6]). That is, there are production
  servers on PostgreSQL 13 that were never upgraded.
- PostgreSQL 13 reached end of support on 2025-11-13 and PostgreSQL 14 loses it on 2026-11-12.
  PostgreSQL 18 is supported until 2030-11-14 [12] (VERIFIED).

## 5. Creating and connecting libraries from Resolve

- Resolve's Project Manager offers "Create" (creates a new library on the server) and
  "Connect" (connects to an existing one). Community projects indicate that the first
  workstation uses "Create" so that Resolve creates the structure, and the others use
  "Connect" [10] (REPORTED).
- Blackmagic's default credentials: user `postgres`, password `DaVinci` [9][10][11]
  (REPORTED, widely spread convention). That is, the official model uses the superuser.
- Resolve creates the database and its schema. The schema is not publicly documented.
- If "Create" is attempted on a name that already exists, Resolve replies that the database
  already exists [23] (REPORTED). There is no public evidence on whether "Connect" on an empty
  database initializes the schema: **INVESTIGATE (PoC-01)**. PoC-01 (2026-10-19) answered it for
  Resolve Studio 21.1.1 on Linux: it does not; Resolve marks the empty database incompatible
  (`docs/poc/poc-01-lifecycle.md`, step 2).
- Only the server IP is typed into the location field, without a port [10] (REPORTED).
  WireBear indicates that port 5432 cannot be changed without clients failing [18]
  (REPORTED). **INVESTIGATE (PoC-08)** whether Resolve accepts another port. PoC-08 ran only
  with port 5432 (`docs/poc/poc-08-network.md`), so this remains open.
- No documentation was found on the minimum privileges Resolve needs (superuser, `CREATEDB`,
  database ownership). **INVESTIGATE (PoC-01)**. PoC-01 (2026-10-19), with Resolve Studio 21.1.1
  on Linux: "Create" worked for a non-superuser role with `CREATEDB` and left the database owned
  by that role; daily use works after `CREATEDB` is removed. A database owned by another role was
  not tested (`docs/poc/poc-01-lifecycle.md`, steps 1, 3 and 4).

## 6. Upgrading libraries between Resolve versions

- The release notes for Resolve 21, 21.0.3, 21.1 and 21.1.1 say, with identical wording, that
  library compatibility with Resolve 20.3.2 was kept, but that "individual projects created or
  opened" in 21.x "will no longer be accessible" in 20.3.2, and they recommend "a full library
  backup and backups of individual projects" before opening projects in 21 [1][2][3][4]
  (VERIFIED).
- Consequence: a single workstation on a newer version can leave projects inaccessible to the
  others. The change may be at the project (data) level and not necessarily in the schema.
- The community documents that, after restoring a backup from an earlier version, the library
  is connected and Resolve offers to upgrade it [11] (REPORTED).

## 7. Native backups

- The manual describes `.resolve.backup` files created by Project Server and by the Project
  Manager [5] (VERIFIED).
- Free tools produce backups with `pg_dump --format=custom --blobs` and the `.backup`
  extension, and claim they can be restored from the Resolve interface [9][11] (REPORTED). The
  exact internal format of the official file is not documented: **INVESTIGATE (PoC-04)**. PoC-04
  did not reach it: the steps that use Resolve's own Backup and Restore were not run
  (`docs/poc/poc-04-backup-format.md`).
- The community procedure to restore on a new server uses `pg_restore
  --single-transaction --clean --if-exists` on a blank database [11] (REPORTED).
- "Optimize" is equivalent to `vacuumdb` and `reindexdb` [5][11].

## 8. `.resolvedbkey` access keys

- VERIFIED [5]: Project Server exports a `.resolvedbkey` file; dragging it onto the Project
  Manager makes the shared library appear in the sidebar. If the server changes network,
  sharing has to be disabled and re-enabled before generating new keys.
- REPORTED: the Resolve 18 manual (online copy, now down) describes the key as "an .xml file
  with the .resolvedbkey extension". A forum user states that it stores the password in plain
  text.
- INVESTIGATE (PoC-05): the names of the XML elements, whether it includes user and password,
  whether it accepts a port or host name, and whether Resolve on Windows, macOS and Linux
  accepts a key generated by another program. There is no public documentation and no free
  implementations (GitHub search with no results on 2026-10-18). PoC-05 has not run.

## 9. Collaboration

| Claim | Status | Source |
|---|---|---|
| Everyone taking part in collaboration must use DaVinci Resolve Studio | VERIFIED for Resolve 15 [5]; to be confirmed for 21 (PoC-07, not run) | Manual [5] |
| Collaboration uses bin, timeline and clip locking, and a built-in chat ("Collaboration Chat") | VERIFIED [5] | Manual |
| Each collaborator's name is set by each workstation ("Customizing Your Collaborator Identification"); it is not an authenticated identity | VERIFIED [5] | Manual |
| Since Resolve 15.2 the link between clients uses a port between 50059 and 50314 (it increments if 50059 is busy); before 15.2 it was 56789 | REPORTED | Blackmagic support on the forum [7] |
| The connection between collaborators is client to client; a successful login to the library does not prove that the direct link works | REPORTED | [7][8] and a third-party guide for 21.1 [24] |
| Clients must be on the same subnet | REPORTED by support on the forum [7]; the Resolve 15 manual says they can also be on different subnets [5] | Contradiction: PoC-08 did not test collaboration and PoC-07 has not run |
| Resolve 21.1.1 "fixed multi-user collaboration connectivity issues" | VERIFIED | Release notes [2] |
| The library must be on a server that is never shut down or suspended | VERIFIED [5] | Manual |

Architecture conclusion: the server only needs to serve PostgreSQL on 5432. Collaboration
traffic on 50059-50314 travels between workstations. Studio Server publishes 50059 on its
container and describes it as "collaboration chat" [18]; with the current evidence that has no
effect, because the chat does not go through the server. To be confirmed with a traffic capture
in PoC-07, which has not run.

## 10. WireBear's Studio Server

### 10.1 Version 1 (documented)

Sources: product page [18], CLI client page [19], Docker Hub [20].

- A single Docker container (`allebb/studio-server`) that includes PostgreSQL, a web panel,
  cron jobs and hooks; internally it uses its CLI tool "Studio Server Client".
- Ports: 5432 (PostgreSQL), 50059 ("collaboration chat"), 8543 (panel). Only 8543 can be
  remapped.
- Volumes: `database`, `backups`, `jobs` (mounted at `/etc/cron.d/`) and `hooks` (BASH scripts
  before and after the backup).
- Functions: web panel; create one or more libraries; creates database users and roles (the
  user is named after the database); scheduled backups per database (00:00 by default) with
  cron expressions; purge of old backups (30 days by default); manual backup and restore;
  password resets; hooks to upload backups to the cloud.
- Its own warning: before restoring, all users must quit Resolve or switch libraries.
- The `latest` tag on Docker Hub dates from 2023-07-24 and is amd64 only [20].

### 10.2 Version 3 (announced, unreleased as of 2026-10-18)

Announcement of 2026-09-06 [21], which mentioned September; on the forum, a message of
2026-10-08 thanks the author for an update and says it expects the release in late October
2026 (date not confirmed by WireBear in a public source):

- Full rewrite with lower resource usage.
- Three installation modes: without containers, Docker Compose with a separate database and
  Compose with an external database server.
- Optional backup replication to S3, webhooks, ARM64 images, view of users and machines
  connected per library.
- Paid "Professional" edition (lifetime license) with user and library auditing, per-user and
  per-library access control, centralized revocation and a restricted portal where each user
  downloads the connection files for their libraries.

### 10.3 License

We found no public repository or license for Studio Server or its CLI. The page says
"Copyright © 2023 WireBear - All rights reserved" [18][19]. A public image on Docker Hub is not
the same as open source. sqsolve is designed only from these public functional descriptions.

### 10.4 What sqsolve takes as a reference and what it improves

| Need | Studio Server | sqsolve |
|---|---|---|
| Separation of components | v1 everything in one container; v3 announces Compose | Two containers from the start: official PostgreSQL and sqsolve |
| PostgreSQL privileges | Not documented | Control role without superuser; a role of its own per library; network rules per library |
| Restore | Onto the active database, with a warning to quit Resolve | Always into a new database, verification and then swap; the previous state is kept to revert |
| Backup verification | Not documented | SHA-256 checksum, index read and periodic test restore with row counts |
| Hooks | BASH scripts inside the server container | Webhooks; no hooks (isolated hooks are a roadmap item, `docs/20-roadmap.md`) |
| Audit log and per-person access | Paid edition (v3) | Included and free (per-person credentials not validated with Resolve, PoC-01 step 5) |
| Port 50059 | Published | Not published (collaboration goes between workstations) |
| License | Closed | AGPL-3.0-only |

## 11. Other free projects

| Project | License | What it does | What we learn |
|---|---|---|---|
| elliotmatson/Docker-Davinci-Resolve-Project-Server [9] | MIT | Compose with `postgres:13`, backups with `prodrigestivill/postgres-backup-local` (`--blobs --format=custom --quote-all-identifiers`, `.backup` suffix), pgAdmin | Format compatibility with Resolve's restore is a valued feature; Resolve is not very tolerant of different PostgreSQL versions |
| Luprintech/davinci-resolve-server [10] | MIT | Compose with PostgreSQL 13 and backups; recommends a VPN and never exposing 5432 | "Create the first time, Connect for the rest" flow; on Synology, 5432 clashes with the system PostgreSQL |
| sethgoldin/davinci-resolve-postgresql-workflow-tools [11] | No license declared according to the GitHub API | Backup and "optimize" scripts with launchd and systemd | Restore procedure on a new server; "Optimize" equivalence |

None of them offers per-library roles, safe restore, backup verification or complete web
administration.

## 12. PostgreSQL facts that shape the design

| Fact | Status | Source |
|---|---|---|
| End of support: 13 on 2025-11-13, 14 on 2026-11-12, 15 on 2027-11-11, 16 on 2028-11-09, 17 on 2029-11-08, 18 on 2030-11-14 | VERIFIED | [12] |
| Since PostgreSQL 16, a role with `CREATEROLE` only modifies roles over which it has `ADMIN OPTION`, and only grants `CREATEDB` if it has it | VERIFIED | [13] |
| PostgreSQL 18 enables page checksums (data checksums) by default in `initdb` and deprecates MD5 passwords | VERIFIED | [14] |
| The `builtin` collation provider (PostgreSQL 17+) supports `C`, `C.UTF-8` and `PG_UNICODE_FAST` and does not depend on glibc | VERIFIED | [15] |
| `WITH OIDS` was removed in PostgreSQL 12 | VERIFIED | [16] |
| Predefined role `pg_read_all_data` (14+) to read all tables | VERIFIED | [25] |
| In the official `postgres:18` image, `PGDATA` is `/var/lib/postgresql/18/docker` and the volume is mounted at `/var/lib/postgresql` | VERIFIED | [17] |

## 13. Docker and NAS

- Any platform that accepts a Docker Compose file can install sqsolve. TrueNAS runs apps with
  Docker since 24.10 and lets you paste a Compose file with "Install via YAML" [26]; Portainer
  and Unraid accept Compose in a similar way. Nothing NAS-specific is designed.
- The TrueNAS app catalog uses the official `postgres` image (18.6 as of 2026-10-18) [27],
  which confirms that the official image is the standard choice.
- On Synology, the system PostgreSQL occupies 5432 on localhost; the community's solution is
  to publish the container port only on the LAN IP [10].

## 14. Findings matrix

| ID | Finding | Status | Affects |
|---|---|---|---|
| H-01 | A network library is an independent PostgreSQL database | VERIFIED | Data model |
| H-02 | Resolve 21.1 works with libraries on PostgreSQL 13 or later and Project Server ships 18 | VERIFIED | ADR 0006 |
| H-03 | Resolve 18 to 20 were used with PostgreSQL 13; their compatibility with 18 is not documented | INVESTIGATE (PoC-02, not run) | Compatibility matrix |
| H-04 | Resolve 17 and earlier do not work with PostgreSQL 12+ | REPORTED | Out of support |
| H-05 | Resolve creates the schema with "Create"; minimum privileges unknown | VERIFIED for Studio 21.1.1 on Linux (PoC-01): "Create" with a `CREATEDB` role, daily use without `CREATEDB`; "Connect" does not initialize an empty database | Library creation, roles |
| H-06 | `.resolve.backup` format compatible with `pg_dump -Fc` | REPORTED, INVESTIGATE (PoC-04 steps 1 and 2, not run) | Backup format |
| H-07 | `.resolvedbkey` is XML with connection data | REPORTED, INVESTIGATE (PoC-05, not run) | Keys module |
| H-08 | Client-to-client collaboration on 50059-50314 | REPORTED, INVESTIGATE (PoC-07, not run) | Ports, documentation |
| H-09 | Collaboration requires Studio | VERIFIED on 15, to be confirmed on 21 (PoC-07, not run) | Documentation |
| H-10 | Projects opened in 21.x do not open in 20.3.2 | VERIFIED | Upgrade window |
| H-11 | The collaborator identity in Resolve is not authenticated | VERIFIED | Authorization: by connection only |
| H-12 | Resolve does not allow choosing the port | REPORTED, INVESTIGATE (PoC-08 tried only 5432) | Deployment |
| H-13 | Studio Server is not free software | VERIFIED (no public source or license) | Independent implementation |

## 15. Implications for the architecture

1. The default engine is PostgreSQL 18 (supported until 2030 and the version Blackmagic already
   ships). Resolve 20.3.x compatibility with 18 is not declared: PoC-02, which would demonstrate
   it, has not run.
2. sqsolve treats libraries as black boxes: it does not depend on the Resolve schema, which is
   not documented and changes between versions.
3. Library creation must work even if Resolve insists on creating the database itself
   ("initialization window" strategy, `07-libraries-and-postgresql.md`).
4. Real authorization only exists at the PostgreSQL connection level (which credential gets
   into which library and from which network). No permissions inside a project are offered
   because Resolve does not enforce them.
5. `.resolvedbkey` keys depend on PoC-05, which has not run; sqsolve does not generate them.
6. The server does not publish 50059; the documentation for editors explains the workstation
   ports.
7. Before any Resolve upgrade, sqsolve offers verified backups of all libraries, because the
   change can be irreversible for earlier versions.

## References

1. Blackmagic Design, "About DaVinci Resolve 21" (21.1), release notes.
   https://www.blackmagicdesign.com/support/content/readme/59dd4eef1f4941c29fb8dc48b33f5c87
2. Blackmagic Design, 21.1.1 release notes.
   https://www.blackmagicdesign.com/support/content/readme/62817b75bdf744718e93909ca6d58f0d
3. Blackmagic Design, 21 release notes.
   https://www.blackmagicdesign.com/support/content/readme/6aefbffe870f4ba686e61ecb83b19ec0
4. Blackmagic Design, 21.0.3 release notes.
   https://www.blackmagicdesign.com/support/content/readme/f549d6da20df4cc98f57d4777d8c8a03
5. Blackmagic Design, DaVinci Resolve 15 Reference Manual, chapters 147 "Managing Databases and
   Project Servers" and 148 "Collaborative Workflow" (public copy).
   https://ltbits.github.io/davinci-resolve-manuals/DR15/DR15-RM-13-ProjectDatabasesCollaborativeAndRemoteWorkflows.pdf
6. Blackmagic forum, "Resolve Project Server v20 is out".
   https://forum.blackmagicdesign.com/viewtopic.php?f=21&t=222297
7. Blackmagic forum, support answers about the collaboration port.
   https://forum.blackmagicdesign.com/viewtopic.php?p=547147 and
   https://forum.blackmagicdesign.com/viewtopic.php?p=747507
8. Blackmagic forum, "Trying to setup collaboration and failing".
   https://forum.blackmagicdesign.com/viewtopic.php?p=747246
9. elliotmatson/Docker-Davinci-Resolve-Project-Server.
   https://github.com/elliotmatson/Docker-Davinci-Resolve-Project-Server
10. Luprintech/davinci-resolve-server. https://github.com/Luprintech/davinci-resolve-server
11. sethgoldin/davinci-resolve-postgresql-workflow-tools.
    https://github.com/sethgoldin/davinci-resolve-postgresql-workflow-tools
12. PostgreSQL, Versioning Policy. https://www.postgresql.org/support/versioning/
13. PostgreSQL 16.0 Release Notes. https://www.postgresql.org/docs/release/16.0/
14. PostgreSQL 18.0 Release Notes. https://www.postgresql.org/docs/release/18.0/
15. PostgreSQL 18, Locale Support. https://www.postgresql.org/docs/18/locale.html
16. PostgreSQL 12.0 Release Notes. https://www.postgresql.org/docs/release/12.0/
17. Official `postgres` image on Docker Hub. https://hub.docker.com/_/postgres
18. WireBear, Studio Server. https://wirebear.co.uk/software/studio-server
19. WireBear, Studio Server Client. https://wirebear.co.uk/software/studio-server-client
20. Docker Hub, `allebb/studio-server`. https://hub.docker.com/r/allebb/studio-server
21. Studio Server forum, "Studio Server v3 is coming soon" (2026-09-06).
    https://forums.studioserver.net/d/2-studio-server-v3-is-coming-soon
22. Blackmagic Cloud, "Accessing Blackmagic Cloud from DaVinci Resolve".
    https://help.cloud.blackmagicdesign.com/getting-started/bmcloud-and-davinci-resolve/
23. Blackmagic forum, "Reconnect to database that already exists".
    https://forum.blackmagicdesign.com/viewtopic.php?p=743090
24. Third-party guide for 21.1 on communication between collaborators (unofficial source).
    https://davinciresolve21.com/blog/davinci-resolve-21-1-cannot-communicate-with-collaborators
25. PostgreSQL 18, Predefined Roles. https://www.postgresql.org/docs/18/predefined-roles.html
26. TrueNAS, Install Custom App Screens (26). https://www.truenas.com/docs/scale/26/apps/installcustomappscreens/
27. TrueNAS app catalog, `postgres` app. https://github.com/truenas/apps/tree/master/ix-dev/community/postgres
