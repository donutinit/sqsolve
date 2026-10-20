# 03. Product definition

## 1. Vision

That any editor or small studio can have a reliable, free DaVinci Resolve library server of
their own, installed by pasting a YAML file, managed from the browser, and protecting projects
with verified backups, without depending on anyone else.

## 2. Product principles

1. **Never lose projects.** No sqsolve action destroys data without a prior verifiable copy.
   When in doubt, keep it.
2. **Honest with Resolve.** Only what Resolve really respects is offered. If a feature depends
   on something undocumented, the interface says so.
3. **Works without Internet.** No essential feature requires an external connection.
4. **Simple to operate.** Two containers, one YAML file, clear volumes. No terminal in daily
   use.
5. **Truly free.** AGPL, buildable from source, no telemetry.

## 3. Personas

| Persona | Context | What they need |
|---|---|---|
| **Individual editor** | One person with a desktop and a laptop, NAS at home | Open the same project on both machines; automatic backups without thinking about them |
| **Small studio** | 2 to 10 workstations, editors, colorist, assistant | Libraries per client or production, Resolve Studio collaboration, per-person access, fast restores |
| **Technical administrator** | Whoever installs and maintains the NAS (may be the editor) | Clear installation, alerts, upgrades without surprises, documented recovery |
| **External collaborator** (1.1) | Freelancer working over a VPN for a few days | Receive access to a library, and have it removable without affecting the others |

## 4. Scope

### In

- Administration of the PostgreSQL server for Resolve libraries.
- Lifecycle of libraries, credentials and network access.
- Backups, verification, retention, restore and off-site copies.
- Job automation, notifications and webhooks.
- Audit log, logs, health and diagnostics.
- Guides to connect workstations, collaborate and prepare media storage.

### Out

- Storing, serving or transcoding media; generating proxies.
- Reading or modifying project contents (bins, timelines). See ADR 0007, except for the
  limited read used to export `.drp` and `.drx` (ADR 0019, experimental).
- Replacing Blackmagic Cloud or hosting libraries for third parties as a service.
- Providing the VPN (how to use an existing one is documented).
- Mandatory desktop clients: everything is done from the browser and from Resolve.

### Planned optional modules

- `.resolvedbkey` keys (experimental, PoC-05).
- Encrypted off-site copies with restic.
- Capacity monitor for media volumes (free space only, no file reads).
- Diagnostic tool for workstations (`sqsolve-check`).

## 5. Use cases

| ID | Case | Actor | Version |
|---|---|---|---|
| CU-01 | Install sqsolve with a Compose file and complete the setup wizard | Administrator | 1.0 |
| CU-02 | Log in and out of the panel, with optional second factor | Administrator, operator | 1.0 |
| CU-03 | See the overall status: services, space, latest backups, alerts | Everyone | 1.0 |
| CU-04 | Create a new library | Administrator | 1.0 |
| CU-05 | Get the connection details and connect a workstation | Administrator, editor | 1.0 |
| CU-06 | Connect a second workstation to the same library | Editor | 1.0 |
| CU-07 | See who is connected to each library | Administrator, operator | 1.0 |
| CU-08 | Rotate a library's password | Administrator | 1.0 |
| CU-09 | Put a library into maintenance (block connections) | Administrator | 1.0 |
| CU-10 | Delete a library (to the trash with a final backup) | Administrator | 1.0 |
| CU-11 | Back up a library now | Administrator, operator | 1.0 |
| CU-12 | Schedule backups and retention per library | Administrator | 1.0 |
| CU-13 | Browse the backup catalog and its verification | Everyone | 1.0 |
| CU-14 | Restore a backup as a new library | Administrator, operator | 1.0 |
| CU-15 | Restore a backup replacing the library, with revert | Administrator | 1.0 |
| CU-16 | Download a backup to restore it in Resolve | Administrator, operator | 1.0 |
| CU-17 | Import an external backup (from Resolve or another server) | Administrator | 1.0 |
| CU-18 | See queued, running, succeeded and failed jobs, with their log | Everyone | 1.0 |
| CU-19 | Receive notifications by email, webhook or ntfy | Administrator | 1.0 (webhook), 1.1 (email, ntfy) |
| CU-20 | Browse the audit log | Administrator | 1.0 |
| CU-21 | Prepare a Resolve upgrade (upgrade window) | Administrator | 1.0 |
| CU-22 | Upgrade sqsolve or PostgreSQL (minor version) keeping data | Administrator | 1.0 |
| CU-23 | Recover the whole installation on another server | Administrator | 1.0 |
| CU-24 | Generate a diagnostic bundle | Administrator | 1.0 |
| CU-25 | Issue per-person credentials and revoke them | Administrator | 1.1 |
| CU-26 | Restrict a library to certain networks | Administrator | 1.1 |
| CU-27 | Connection portal: a collaborator sees and downloads only their own access | Collaborator | 1.1 |
| CU-28 | Copy backups to an encrypted external repository | Administrator | 1.1 |
| CU-29 | Run hooks before and after operations | Administrator | 1.1 |
| CU-30 | Migrate libraries from another PostgreSQL server (official Project Server, Studio Server) | Administrator | 1.1 |
| CU-31 | Download a `.resolvedbkey` key | Administrator, collaborator | 1.1 if PoC-05 passes |
| CU-32 | Point-in-time recovery | Administrator | 2.0 |
| CU-33 | Migrate to a new PostgreSQL major version | Administrator | 2.0 |

## 6. Acceptance criteria for version 1.0 (MVP)

Each criterion is demonstrated with the acceptance script of `16-testing.md` §8, with real
Resolve on at least one Windows or macOS workstation and another on any platform.

| ID | Criterion | How it is verified |
|---|---|---|
| AC-01 | Install on generic Linux and on a NAS with Docker Compose by pasting the reference YAML, editing nothing but paths and the time zone | Timed clean installation in both environments; no terminal step outside the YAML |
| AC-02 | Reach the panel over HTTPS, complete the wizard and create the first administrator | The wizard finishes; the certificate is shown with its fingerprint; the second start does not show the wizard again |
| AC-03 | Create a library compatible with Resolve 21.1 from the panel | The library becomes "active" and Resolve creates and opens a project in it |
| AC-04 | Connect a workstation following only the panel's instructions | A third party connects the workstation unaided in under 5 minutes |
| AC-05 | Connect a second workstation to the same library | Both see the same projects; the panel shows both connections |
| AC-06 | Use Resolve Studio collaboration on that library | Bin locking and chat work between the two workstations with the documented ports |
| AC-07 | Automatic scheduled backups with retention | Over 72 hours the scheduled backups are produced, retention deletes the ones it should and no verified backup is lost by mistake |
| AC-08 | Restore a library and verify its recovery | Restore as a copy and by replacement; Resolve opens the restored project with the contents as of the backup; the revert returns the previous state |
| AC-09 | Restart and upgrade without loss | Host reboot, `docker compose pull && up -d` to a new version and a simulated power cut during a backup: libraries, catalog and configuration intact |
| AC-10 | Operate without proprietary services | With no Internet access, all 1.0 use cases work |
| AC-11 | Disaster recovery | With only the backups volume and the recovery kit, a fresh installation recovers all libraries |

## 7. Product success metrics

- Zero library loss incidents attributable to sqsolve.
- Installation time until the first connected library: under 15 minutes.
- 100 % of scheduled backups with a recorded result (succeeded, or failed with a notification).
- Weekly automatic test restore succeeding on all active libraries.
