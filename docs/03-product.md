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
| **External collaborator** | Freelancer working over a VPN for a few days | Receive access to a library, and have it removable without affecting the others (per-person credentials, not validated with real Resolve; there is no collaborator portal) |

## 4. Scope

### In

- Administration of the PostgreSQL server for Resolve libraries.
- Lifecycle of libraries, credentials and network access.
- Backups, verification, retention and restore. Off-site copies are not implemented
  (`20-roadmap.md`).
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

### Optional modules not implemented

sqsolve 1.2.2 has none of these; they are product directions in `20-roadmap.md`:

- `.resolvedbkey` keys (experimental, depends on PoC-05, which has not been run).
- Encrypted off-site copies with restic.
- Capacity monitor for media volumes (free space only, no file reads).
- Diagnostic tool for workstations (`sqsolve-check`).

## 5. Use cases

Status in 1.2.2. "Available" means the panel, the API or the console does it. Resolve behavior is
validated only as recorded in `acceptance/1.0.md`, `acceptance/1.2.2.md` and `poc/` (Resolve
Studio 21.1.1 on Linux); where a case depends on Resolve beyond those records, the status says so.

| ID | Case | Actor | Status (1.2.2) |
|---|---|---|---|
| CU-01 | Install sqsolve with a Compose file and complete the setup wizard | Administrator | Available |
| CU-02 | Log in and out of the panel, with optional second factor | Administrator, operator | Available |
| CU-03 | See the overall status: services, space, latest backups, alerts | Everyone | Available |
| CU-04 | Create a new library | Administrator | Available |
| CU-05 | Get the connection details and connect a workstation | Administrator, editor | Available |
| CU-06 | Connect a second workstation to the same library | Editor | Available; not validated with two workstations (AC-04, AC-05) |
| CU-07 | See who is connected to each library | Administrator, operator | Available |
| CU-08 | Rotate a library's password | Administrator | Available |
| CU-09 | Put a library into maintenance (block connections) | Administrator | Available |
| CU-10 | Delete a library (to the trash with a final backup) | Administrator | Available |
| CU-11 | Back up a library now | Administrator, operator | Available |
| CU-12 | Schedule backups and retention per library | Administrator | Available |
| CU-13 | Browse the backup catalog and its verification | Everyone | Available |
| CU-14 | Restore a backup as a new library | Administrator, operator | Available |
| CU-15 | Restore a backup replacing the library, with revert | Administrator | Available; the project must be closed in Resolve first (`19-risks.md` T-05) |
| CU-16 | Download a backup to restore it in Resolve | Administrator, operator | Available; restoring the file with Resolve's own Restore is not validated (PoC-04) |
| CU-17 | Import an external backup (from Resolve or another server) | Administrator | Available; importing a file made by Resolve's own Backup is not validated (PoC-04) |
| CU-18 | See queued, running, succeeded and failed jobs, with their log | Everyone | Available |
| CU-19 | Receive notifications by email, webhook or ntfy | Administrator | Available; email and ntfy not tested against real servers |
| CU-20 | Browse the audit log | Administrator | Available |
| CU-21 | Prepare a Resolve upgrade (upgrade window) | Administrator | Available; not validated during a real Resolve upgrade (PoC-03) |
| CU-22 | Upgrade sqsolve or PostgreSQL (minor version) keeping data | Administrator | Available |
| CU-23 | Recover the whole installation on another server | Administrator | Available |
| CU-24 | Generate a diagnostic bundle | Administrator | Available |
| CU-25 | Issue per-person credentials and revoke them | Administrator | Available; not validated with real Resolve (PoC-01 steps 3 and 5) |
| CU-26 | Restrict a library to certain networks | Administrator | Available |
| CU-27 | Connection portal: a collaborator sees and downloads only their own access | Collaborator | Not implemented |
| CU-28 | Copy backups to an encrypted external repository | Administrator | Not implemented |
| CU-29 | Run hooks before and after operations | Administrator | Not implemented (signed webhooks only) |
| CU-30 | Migrate libraries from another PostgreSQL server (official Project Server, Studio Server) | Administrator | Not implemented |
| CU-31 | Download a `.resolvedbkey` key | Administrator, collaborator | Not implemented (PoC-05 not run) |
| CU-32 | Point-in-time recovery | Administrator | Not implemented |
| CU-33 | Migrate to a new PostgreSQL major version | Administrator | Not implemented |

The cases that are not implemented are product directions in `20-roadmap.md`.

## 6. Acceptance criteria for version 1.0 (MVP)

The criteria were written to be demonstrated with the acceptance script of `16-testing.md` §8,
with real Resolve on at least one Windows or macOS workstation and another on any platform.
1.0 was accepted on 2026-10-19 without that two-workstation session: the agent ran what could be
automated against the test installation, with one Linux workstation running Resolve Studio
21.1.1. The Result column summarizes `acceptance/1.0.md`.

| ID | Criterion | How it is verified | Result (`acceptance/1.0.md`) |
|---|---|---|---|
| AC-01 | Install on generic Linux and on a NAS with Docker Compose by pasting the reference YAML, editing nothing but paths and the time zone | Timed clean installation in both environments; no terminal step outside the YAML | Done on TrueNAS only; generic Linux and the timing are not recorded |
| AC-02 | Reach the panel over HTTPS, complete the wizard and create the first administrator | The wizard finishes; the certificate is shown with its fingerprint; the second start does not show the wizard again | Done |
| AC-03 | Create a library compatible with Resolve 21.1 from the panel | The library becomes "active" and Resolve creates and opens a project in it | Done (Linux) |
| AC-04 | Connect a workstation following only the panel's instructions | A third party connects the workstation unaided in under 5 minutes | Not done |
| AC-05 | Connect a second workstation to the same library | Both see the same projects; the panel shows both connections | Not done |
| AC-06 | Use Resolve Studio collaboration on that library | Bin locking and chat work between the two workstations with the documented ports | Not done |
| AC-07 | Automatic scheduled backups with retention | Over 72 hours the scheduled backups are produced, retention deletes the ones it should and no verified backup is lost by mistake | Done with activity-based backups; a 72-hour run is not recorded |
| AC-08 | Restore a library and verify its recovery | Restore as a copy and by replacement; Resolve opens the restored project with the contents as of the backup; the revert returns the previous state | Done; in 1.0 the revert only by tests, then with Resolve in `acceptance/1.2.2.md` |
| AC-09 | Restart and upgrade without loss | Host reboot, `docker compose pull && up -d` to a new version and a simulated power cut during a backup: libraries, catalog and configuration intact | Restart and upgrade done; the power cut during a backup not done (CI kills the process mid-job instead) |
| AC-10 | Operate without proprietary services | With no Internet access, all 1.0 use cases work | Not done |
| AC-11 | Disaster recovery | With only the backups volume and the recovery kit, a fresh installation recovers all libraries | Done |

## 7. Product success metrics

- Zero library loss incidents attributable to sqsolve.
- Installation time until the first connected library: under 15 minutes.
- 100 % of scheduled backups with a recorded result (succeeded, or failed with a notification).
- Weekly automatic test restore succeeding on all active libraries.
