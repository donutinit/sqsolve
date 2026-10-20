# 14. Compatibility

## 1. Support policy

| Level | Meaning |
|---|---|
| **Supported** | Tested with the compatibility script (§4) on every sqsolve version; failures are bugs to fix |
| **Verified** | Tested at least once with a correct result; may break without notice |
| **Expected** | Should work according to the documentation, without our own testing |
| **Unsupported** | Does not work or is not tested; sqsolve warns |

Rule: the current major Resolve family is supported, plus the latest version of the previous one
that Blackmagic declares compatible at library level.

## 2. Matrix as of 2026-10-18

### 2.1 DaVinci Resolve

| Version | Level | Basis for the decision |
|---|---|---|
| 21.1.x | Supported on Linux (PoC-01 E1, 03, 04 partial, 06, 08 and 09 of 2026-10-19); macOS and Windows expected, untested | Blackmagic declares libraries on PostgreSQL 13+ and ships 18 [01 §4]. Experimental export verified with 21.1.1 on 2026-10-19 (`22` §6). TLS: 21.1.1 on Linux negotiates TLS 1.3 with `sslmode=prefer`, accepts the self-signed certificate and connects with `hostssl` required (2026-10-19, `docs/poc/poc-06-tls.md`); macOS and Windows untested |
| 21.0.x | Expected | Same family; upgrading to 21.1.x is recommended |
| 20.3.2 | Supported if PoC-02 passes with PostgreSQL 18 | Libraries compatible with 21 according to Blackmagic [01 §6] |
| 19.x, 18.x | Expected at most, without tests | They used PostgreSQL 13; no guarantee with 18 |
| 17 and earlier | Unsupported | Require PostgreSQL 11 or earlier, without security support |

### 2.2 PostgreSQL

| Version | Use in sqsolve |
|---|---|
| 18.x | Only version packaged in 1.x |
| 17.x | Alternative profile only if PoC-02 shows that some supported Resolve version fails with 18 |
| 16.x or higher | Minimum for the external server mode (2.0), because of the `CREATEROLE` semantics [01 §12] |
| 15 or earlier | Unsupported as a server; supported as a migration source (CU-30): `pg_dump` 18 reads old servers |

### 2.3 Environment

| Element | Requirement |
|---|---|
| Docker Engine | 24 or higher with Compose v2 |
| Architectures | `linux/amd64`, `linux/arm64` |
| Browsers | Last two major versions of Firefox, Chromium (Chrome, Edge) and Safari |
| Workstations | Those supported by the Resolve version in use (Resolve 21.1: macOS 15+ on Apple Silicon, Windows 10 Creators Update or Windows 11, Rocky Linux 8.6 according to Blackmagic [01 ref. 1]) |

## 3. How sqsolve detects a library's version

1. **Declared version** set by the administrator in the library detail (validated free text, for
   example `21.1.1`).
2. **Schema fingerprint** (`07` §9) compared with the registry of known fingerprints included in
   each sqsolve version (`internal/compat/fingerprints.json`), which also indicates which
   exports were verified with that fingerprint (`verified`).
3. `library.schema_changed` events with the old and new fingerprint.

No internal Resolve version table is read: it is not documented.

## 4. Compatibility script (per new Resolve version)

Owner: whoever maintains the matrix. Target deadline: within 14 days after a Resolve release;
betas are tested only if there is time and are marked as such.

1. Read the release notes looking for "project library", "PostgreSQL", "Project Server",
   "collaboration" and compatibility notices. Record quotes in `docs/compat/<version>.md`.
2. New library with the current strategy; create a project, import media, edit, close and
   reopen.
3. Open a library from the previous version; record whether Resolve asks to upgrade it and the
   fingerprint before and after.
4. sqsolve backup, restore as a copy and by swap; open in Resolve.
5. Restore a sqsolve backup in Resolve and a Resolve backup in sqsolve (PoC-04).
6. Collaboration between two workstations.
7. If the key module is active: import a generated key.
8. If the experimental export is active: run the kit from `22` §6 (export with sqsolve and with
   Resolve, compare, import) and record the fingerprint with what was verified.
9. Update the matrix, the fingerprint registry and the sqsolve release notes.

## 5. Resolve upgrade window

Because opening projects in a new version can make them inaccessible to the previous one
[01 §6], sqsolve guides the process (CU-21, `09-web-ui.md` §4.5):

1. Choose the target version and affected libraries (all by default).
2. `pre_upgrade` backups with L2 verification of each library. The window does not advance if
   any fails.
3. Checklist for the studio: all workstations close Resolve; one workstation is upgraded; it
   opens each library and accepts the upgrade if offered; a project is checked; the others are
   upgraded.
4. Close: fingerprint comparison; the declared version is updated; the `pre_upgrade` backups
   stay protected for 30 days.

Going back: restore the `pre_upgrade` backups (by swap) and reinstall the previous version on the
workstations.

## 6. Mixed versions

- Collaboration requires the same Resolve version on all workstations (Blackmagic requires it;
  the guide repeats it).
- sqsolve shows, per library, the `application_name` of the sessions if Resolve reports them
  (PoC-01 step 6); if they include a version, the interface warns when different versions are
  connected to the same library.

## 7. sqsolve compatibility with itself

- Backups: any sqsolve version reads manifests and files from earlier versions.
- SQLite: forward migrations; going back to an earlier version requires the prior copy.
- API: `v1` stable throughout the whole 1.x series.
