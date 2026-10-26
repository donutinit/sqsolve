# 14. Compatibility

## 1. Support policy

| Level | Meaning |
|---|---|
| **Supported** | Tested with the compatibility script (§4) on every sqsolve version; failures are bugs to fix |
| **Verified** | Tested at least once with a correct result; may break without notice |
| **Expected** | Should work according to the documentation, without our own testing |
| **Unsupported** | Does not work or is not tested |

Target of the policy: the current major Resolve family is supported, plus the latest version of
the previous one that Blackmagic declares compatible at library level. As of 1.2.2 no Resolve
version reaches "Supported": the script of §4 has not run in full on any version (steps 3, 5 and
6 need what PoC-03, PoC-04 and PoC-07 did not cover), so the matrix uses "Verified" for what was
tested. sqsolve does not check a library's declared Resolve version against this matrix; showing
the matrix in the panel is a roadmap item (`docs/20-roadmap.md`).

## 2. Matrix as of 2026-10-26 (sqsolve 1.2.2)

### 2.1 DaVinci Resolve

| Version | Level | Basis for the decision |
|---|---|---|
| Studio 21.1.1 on Linux | Verified | Build 10 with PostgreSQL 18.6, in the PoCs of 2026-10-19 (`docs/poc/`) and the acceptance records (`docs/acceptance/1.0.md`; `docs/acceptance/1.2.2.md`, on Fedora Linux 44): "Create" in the initialization window (E1) and daily use without `CREATEDB`; "Connect" to an empty database (E0) fails (PoC-01). Fingerprint stable under use (PoC-03). A library restored by sqsolve opens in Resolve (PoC-04 step 3). TLS 1.3 negotiated on its own with `sslmode=prefer`, the self-signed certificate accepted without verification, connects with `hostssl` required (PoC-06). Real client IP on the LAN (PoC-08). After replace and revert Resolve reconnects on its own and its next save writes into the restored library (PoC-09, acceptance 1.2.2). Experimental export verified on 2026-10-19 (`22` §6, §3 below) |
| 21.1.x on macOS and Windows, other 21.1.x builds | Expected | Blackmagic declares libraries on PostgreSQL 13+ and ships 18 [01 §4]; no record. TLS behaviour on macOS and Windows is unknown (PoC-06) |
| 21.0.x | Expected | Same family; upgrading to 21.1.x is recommended |
| 20.3.2 | Expected | Libraries compatible with 21 according to Blackmagic [01 §6]; PoC-02 has not run |
| 19.x, 18.x | Expected at most, without tests | They used PostgreSQL 13; no guarantee with 18 |
| 17 and earlier | Unsupported | Require PostgreSQL 11 or earlier, without security support |

Not validated with real Resolve, on any version or platform:

- Resolve on macOS and Windows.
- The free edition of Resolve: every record used Studio.
- Collaboration between two workstations (PoC-07; AC-04 to AC-06 of `docs/acceptance/1.0.md`).
- Resolve's own Backup and Restore against sqsolve's files (PoC-04 steps 1, 2 and 4).
- Per-person credentials and a database owned by `sqsolve_admin` (PoC-01 steps 3 and 5).
- A schema fingerprint change during a real Resolve upgrade, and with it the upgrade window of §5
  (PoC-03).
- Resolve 20.3.2 and the full Resolve by PostgreSQL matrix (PoC-02).

### 2.2 PostgreSQL

| Version | Use in sqsolve |
|---|---|
| 18.x | Only version packaged (`postgres:18.6-trixie`, pinned by digest in `deploy/compose.yaml`); every record ran on 18.6 |
| 17.x | Not packaged; there is no 17 profile. PoC-02, which would show whether a Resolve version needs one, has not run |
| 16.x | Not used: sqsolve runs only its own PostgreSQL 18 container and has no mode for an external server. The role model of `07` §3 assumes the `CREATEROLE` semantics of 16 or later [01 §12] |
| 15 or earlier | Unsupported as a server. sqsolve does not migrate libraries from another server (CU-30, `docs/20-roadmap.md`); a custom-format `pg_dump` file made there can be imported as a backup (CU-17, `11` §7.6) |

### 2.3 Environment

| Element | Requirement |
|---|---|
| Docker Engine | 24 or higher with Compose v2 |
| Architectures | `linux/amd64`, `linux/arm64` |
| Browsers | Last two major versions of Firefox, Chromium (Chrome, Edge) and Safari |
| Workstations | Those supported by the Resolve version in use (Resolve 21.1: macOS 15+ on Apple Silicon, Windows 10 Creators Update or Windows 11, Rocky Linux 8.6 according to Blackmagic [01 ref. 1]) |

## 3. How sqsolve detects a library's version

1. **Declared version** set by the administrator in the library detail (free text up to 20
   characters, for example `21.1.1`). It is copied into each backup's manifest.
2. **Schema fingerprint** (`07` §9) compared with the registry of known fingerprints included in
   each sqsolve version (`internal/compat/fingerprints.json`), which also indicates which
   exports were verified with that fingerprint (`verified`). In 1.2.2 the registry holds one
   fingerprint: Resolve 21.1.1 (`21.1.1.0010`, `DbPrjVer` 17), `drx` and `drp` verified on
   2026-10-19. The export pages show "Not verified with Resolve" for any other fingerprint; the
   registry is used only by the experimental export.
3. `library.schema_changed` events with the old and new fingerprint, when the fingerprint taken at
   a backup or at the periodic refresh (every 5 minutes for active libraries and libraries in
   maintenance) differs from the stored one.

No internal Resolve version table is read: it is not documented.

## 4. Compatibility script (per new Resolve version)

Owner: whoever maintains the matrix. Target deadline: within 14 days after a Resolve release;
betas are tested only if there is time and are marked as such. The script has not run in full on
any version: the records for 21.1.1 are the PoC reports (`docs/poc/`) and the acceptance records
(`docs/acceptance/`).

1. Read the release notes looking for "project library", "PostgreSQL", "Project Server",
   "collaboration" and compatibility notices. Record quotes in `docs/compat/<version>.md`.
2. New library with the current strategy; create a project, import media, edit, close and
   reopen.
3. Open a library from the previous version; record whether Resolve asks to upgrade it and the
   fingerprint before and after.
4. sqsolve backup, restore as a copy and by swap; open in Resolve.
5. Restore a sqsolve backup in Resolve and a Resolve backup in sqsolve (PoC-04).
6. Collaboration between two workstations.
7. If the experimental export is active: run the kit from `22` §6 (export with sqsolve and with
   Resolve, compare, import) and record the fingerprint with what was verified.
8. Update the matrix, the fingerprint registry and the sqsolve release notes.

sqsolve does not generate `.resolvedbkey` keys (PoC-05 has not run), so there is no key step.

## 5. Resolve upgrade window

Because opening projects in a new version can make them inaccessible to the previous one
[01 §6], sqsolve guides the process (CU-21, `09-web-ui.md` §4.5):

1. In Settings, Maintenance, "Upgrade DaVinci Resolve": type the target version (up to 20
   characters). The window covers every active library and every library in maintenance; it
   runs as the `system.upgrade_window` job.
2. `pre_upgrade` backups with L2 verification of each library. The job stops with `SQS-B060` if
   any fails. Its result records each library's fingerprint before the upgrade.
3. Checklist for the studio: all workstations close Resolve; one workstation is upgraded; it
   opens each library and accepts the upgrade if offered; a project is checked; the others are
   upgraded.
4. Afterwards: a changed fingerprint shows up as a `library.schema_changed` event at the next
   backup or periodic refresh (§3); the declared version is updated by hand in each library's
   detail; the `pre_upgrade` backups stay protected for 30 days. There is no closing step.

The window has not been exercised with a real Resolve upgrade (PoC-03).

Going back: restore the `pre_upgrade` backups (by swap) and reinstall the previous version on the
workstations.

## 6. Mixed versions

- Collaboration requires the same Resolve version on all workstations (Blackmagic requires it;
  the guide repeats it).
- sqsolve shows, per library, the `application_name` of the sessions. Resolve Studio 21.1.1 on
  Linux reports names such as `Resolve_0007433F5FC0`, without a version (PoC-01 step 6), and
  sqsolve does not warn about different versions connected to the same library.

## 7. sqsolve compatibility with itself

- Backups: any sqsolve version reads manifests and files from earlier versions.
- SQLite: forward migrations; going back to an earlier version requires the prior copy.
- API: `v1` stable throughout the whole 1.x series.
