# 18. Documentation, license and trademarks

## 1. Documents

| Document | Audience | Content |
|---|---|---|
| `README.md` | Everyone | What it is, quick installation, links |
| `docs/manual/user-guide.md` | Administrators and editors | Requirements, installation with the compose file, first start, libraries and Resolve workstations, backups, restore, access per library, panel users, notifications and API, export, console, updating, moving to another server, troubleshooting |
| `docs/manual/errors.md` | Everyone | Every `SQS-` code with its message, generated with `make errors` (230 codes in 1.2.2) |
| `docs/runbooks/*.md` | Administrators | Restore a backup and revert, recover an installation on another server, upgrade sqsolve, a backup with an invalid path |
| `docs/` technical | Developers, agents | Architecture, requirements, contracts, ADRs (`docs/adr`), PoC records (`docs/poc`), acceptance records (`docs/acceptance`), interface checklist (`docs/ui`), visual identity (`docs/brand`); index in `docs/README.md` |
| `api/schemas/manifest.v1.json` | Integrators | JSON schema of the backup manifest. The API is described in `10-api.md`; there is no `api/openapi.yaml` |
| `CONTRIBUTING.md` | Contributors | Environment, style, tests, publishing a version, how to propose changes |
| `SECURITY.md` | Researchers | How to report vulnerabilities (GitHub private reporting) |
| `CHANGELOG.md` | Everyone | Changes per version; source of the notes of each release on GitHub |

The user guide is the only manual: there are no separate manuals for installation,
administration, editors or backups, and none for remote access or media storage
(`15-media-storage.md` is the technical document). There are no runbooks for upgrading Resolve,
rotating credentials, a leaked credential, the loss of the master key, a full disk or a
PostgreSQL that does not start.

Rules:

- English is the only language: code, interface and documentation. No second language is
  maintained.
- User documentation does not mention features that do not exist yet.
- Panel screens do not link to the manual; the notes of the compose file (`x-notes`, shown by
  TrueNAS) link to the user guide.
- Runbooks are numbered steps, with the expected output of each command and how to know it
  finished well.
- They are published as Markdown in the repository; there is no generated static site.
- Screenshots of sqsolve itself. Of Resolve, only the essential ones, made by the project and
  used to illustrate interoperability.

## 2. Code license: AGPL-3.0-only

| Option | For | Against |
|---|---|---|
| **AGPL-3.0-only** | Guarantees that modified versions offered over a network (a NAS vendor, a hosted service) publish their code; consistent with "100 % free" | Some companies avoid the AGPL |
| AGPL-3.0-or-later | The same, and admits future versions of the AGPL published by the FSF | Discarded by Diego's decision (ADR 0001) |
| GPL-3.0-or-later | Well-known copyleft | Does not cover use over a network, which is precisely the case of a server with a web panel |
| Apache-2.0 or MIT | Maximum adoption | Allows closed derived versions, which is what the project wants to avoid |
| MPL-2.0 | Per-file copyleft | Weak for a complete application |

Decision in ADR 0001. Consequences:

- `LICENSE` contains the official text of the GNU AGPL version 3 (gnu.org, SHA-256
  `0d96a4ff68ad6d4b6f1f30f713b18d5184912ba8dd389f86aa7710db079abcb0`).
- `SPDX-License-Identifier: AGPL-3.0-only` heads every Go file, the Python scripts in `test/`,
  the workflows, the compose file, the `Makefile`, the `Dockerfile`, `app.js` and the
  stylesheet. The HTML templates, three of the seven SQL migrations and the two Python tools in
  `docs/brand/tools` do not carry it.
- The panel shows in "About" the running version and its commit, with a link to the source
  repository (section 13 of the AGPL); the source of the running version is that commit.
- Allowed dependencies: licenses compatible with AGPLv3 (MIT, BSD, ISC, Apache-2.0, MPL-2.0,
  LGPL, GPLv3). CI does not check them: there is no license listing from the SBOM, and every new
  dependency needs an ADR (`06-stack.md`).
- Contributions: no CLA. Contributions come in under the same license as the project
  ("inbound = outbound"), explained in `CONTRIBUTING.md`.

## 3. Documentation license

CC BY-SA 4.0 for `docs/` (except configuration fragments and code examples, which follow the
code license so they can be copied into installations).

## 4. Trademarks and relationship with third parties

- "DaVinci Resolve", "Blackmagic Design", "Blackmagic Cloud" and "Fusion" are trademarks of
  Blackmagic Design Pty. Ltd. They are used only to describe interoperability.
- The name "sqsolve", the logo (`docs/brand`, ADR 0020) and the interface do not imitate
  Blackmagic or WireBear. The symbol evokes a node of the color page as a generic shape, without
  reproducing any element of the Resolve interface.
- Notice in the README (independent project, not affiliated with or endorsed by Blackmagic
  Design) and in "About" (independent project, unrelated to Blackmagic Design or WireBear).
- No Blackmagic binaries, schemas, templates, manuals or screenshots are distributed.
- Interoperability: the project only observes the external behaviour of software the user holds
  legally (connections to their own server, files generated by their own software). Before
  publishing the `.resolvedbkey` module, have legal counsel review the description of the format
  published in the documentation (risk J-02 in `19-risks.md`).

## 5. Project governance

- Main maintainer: Diego (`donutinit`).
- Technical decisions: ADRs in `docs/adr/`, with status Proposed, Accepted, Superseded.
- Public roadmap in `docs/20-roadmap.md`.
- Support policy: the latest minor version of the latest major receives fixes; security fixes
  are also applied to the previous major for 6 months.
