# Changelog

Format: one section per version, newest on top. The notes of each version on GitHub are taken
from here (`.github/workflows/release.yml`).

## [Unreleased]

## [1.1.2] 2026-10-20

- The export catalog shows Projects and Stills as separate views, each with a search box, and
  every project links to its own stills: a library with hundreds of projects no longer needs a
  long scroll to download a `.drx`.

Upgrade: same as 1.1.0. No migrations. Rollback: image `1.1.1`.

## [1.1.1] 2026-10-20

- Fix: the role name in the panel sidebar (Administrator, Operator, Viewer) and the notes of a
  library whose creation failed were still in Spanish.
- README with screenshots of the panel.

Upgrade: same as 1.1.0. No migrations. Rollback: image `1.1.0`.

## [1.1.0] 2026-10-20

- The whole project moves to English, the only supported language: web panel, URLs, CLI, error
  messages, documentation and brand assets. Panel URLs changed (for example `/bibliotecas` is now
  `/libraries`); update your bookmarks. API paths under `/api/v1` are unchanged.
- New concise README; the full manual lives in `docs/manual/user-guide.md`.
- The example paths in `deploy/compose.yaml` are now `/path/to/sqsolve/...`.

Upgrade: put the `1.1` tag in `x-sqsolve` (`docker compose pull` and `up -d`, or on TrueNAS edit
the app's YAML and Update). No catalog migrations compared with 1.0.0. Rollback: image `1.0.0`,
with the old URLs.

## [1.0.0] 2026-10-19

- First stable version. Scope and evidence in `docs/acceptance/1.0.md` and `docs/poc`.
- The "Empty database" strategy (E0) is marked as not compatible with Resolve 21.1: on connect,
  Resolve declares it incompatible instead of initializing it (PoC-01).
- The README says what is tested and what is not.
- A library in "No database on the server" (after recovering an installation) can now be deleted
  and purged; before, it could only be recreated (`SQS-L041`).

Upgrade: same as 0.1.x, with the `1.0` tag in `x-sqsolve` (`docker compose pull` and `up -d`, or
Update in TrueNAS). No migrations compared with 0.1.6. Rollback: image `0.1.6`.

## [0.1.6] 2026-10-19

- Image signing with keyless cosign and publication of each version on GitHub with the compose
  file and these notes.
- `SECURITY.md`, `CONTRIBUTING.md`, runbooks and `docs/manual/errors.md`.
- `Strict-Transport-Security` (with HTTPS) and `Cross-Origin-Resource-Policy` headers in the
  panel, after the ZAP review.
- PoC-03, 06, 08 and 09 reports in `docs/poc`; ADR 0019 accepted as experimental.

Upgrade: same as 0.1.5. No migrations.

## [0.1.5] 2026-10-19

- Security proportionate to LAN and VPN (ADR 0021): no reauthentication, 8-character passwords,
  30-day sessions, mandatory HTTPS, lock after ten wrong passwords that is lifted with
  `sqsolve admin unlock`.
- Activity-based backups (ADR 0022): check every 15 minutes and back up only if the library
  changed; a current backup does not count as overdue. Default mode in new installations.
- Export `.drp` and `.drx` from any backup, without restoring the library.
- "TLS only" mode for Resolve connections, with the "Encrypted" column in the sessions.
- Catalog migrations 0002 to 0004; they apply on their own at startup.

Upgrade: with the `0.1` tag, `docker compose pull` and `docker compose up -d`, or the Update
button in TrueNAS. Rollback: image `0.1.4` starts with the migrated catalog (the new columns are
ignored).

## [0.1.4] 2026-10-19

- "Node" visual identity (ADR 0020): logo and icons in the panel, favicon, new README.

## [0.1.3] 2026-10-19

- Fix: the Export tab failed with `SQS-X998` on libraries with stills.

## [0.1.2] 2026-10-19

- Experimental export of projects (`.drp`) and grades (`.drx`) (ADR 0019), off by default.

## [0.1.1] 2026-10-19

- PostgreSQL runs as user 999 and `sqsolve-init` prepares its data folder; detection of volumes
  with NFSv4 ACLs (`SQS-C054`).
- The reference compose file uses the `0.1` tag.

## [0.1.0] 2026-10-18

- First version: libraries, access, verified backups, restore, panel, API, notifications,
  recovery kit.
