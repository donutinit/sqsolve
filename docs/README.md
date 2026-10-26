# sqsolve master plan

Research cutoff date: **2026-10-18**. Everything that depends on DaVinci Resolve, PostgreSQL or
Docker versions is reviewed on every major Resolve release (see `14-compatibility.md`).

## How to read these documents

| # | Document | What it is for |
|---|---|---|
| 01 | [Research](01-research.md) | How Resolve works with PostgreSQL, what Studio Server does, what is verified and what is not |
| 02 | [Proofs of concept](02-proofs-of-concept.md) | Experiments that must pass before committing parts of the architecture |
| 03 | [Product](03-product.md) | Vision, scope, personas, use cases and acceptance criteria |
| 04 | [Requirements](04-requirements.md) | Functional and non-functional requirements with IDs |
| 05 | [Architecture](05-architecture.md) | Overview, components, flows and principles |
| 06 | [Stack](06-stack.md) | Evaluation of alternatives and chosen technologies |
| 07 | [Libraries and PostgreSQL](07-libraries-and-postgresql.md) | Roles, lifecycle, server configuration and data model |
| 08 | [Security](08-security.md) | Threat model, authentication, authorization and secrets |
| 09 | [Web UI](09-web-ui.md) | Screens, flows and interaction patterns |
| 10 | [API and contracts](10-api.md) | REST API, events, webhooks, CLI and internal formats |
| 11 | [Backups and automation](11-backups-and-automation.md) | Backup, verification, retention, restore, jobs and notifications |
| 12 | [Deployment](12-deployment.md) | Docker Compose, volumes, configuration, upgrades |
| 13 | [Observability](13-observability.md) | Logs, metrics, health and diagnostics |
| 14 | [Compatibility](14-compatibility.md) | Version matrix and procedure for new Resolve versions |
| 15 | [Media storage](15-media-storage.md) | Relationship with shared storage, paths and proxies |
| 16 | [Testing](16-testing.md) | Test strategy by level |
| 17 | [CI/CD and releases](17-ci-cd-and-releases.md) | Version control, build, signing and publishing |
| 18 | [Documentation and license](18-documentation-and-license.md) | Manuals plan, license and trademarks |
| 19 | [Risks](19-risks.md) | Risk register with mitigations |
| 20 | [Roadmap](20-roadmap.md) | MVP, versions, phases, milestones and dependencies |
| 21 | [Tasks](21-tasks.md) | Breakdown executable by agents with done criteria |
| 22 | [Project and grade export](22-project-and-grade-export.md) | `.drp` and `.drx` format, rules, verification kit (experimental, ADR 0019) |
| POC | PoC reports: [PoC-01 lifecycle](poc/poc-01-lifecycle.md), [PoC-03 fingerprint](poc/poc-03-fingerprint.md), [PoC-04 backup format](poc/poc-04-backup-format.md), [PoC-06 TLS](poc/poc-06-tls.md), [PoC-08 network](poc/poc-08-network.md), [PoC-09 forced disconnect](poc/poc-09-forced-disconnect.md) | Recorded results of the proofs of concept |
| ACC | [1.0 acceptance](acceptance/1.0.md) | Acceptance run for release 1.0 |
| GUIDE | [User guide](manual/user-guide.md) | Install, first start, libraries, backups, restore, access, API, console, updating, disaster recovery, troubleshooting |
| MAN | [Manual: error codes](manual/errors.md) | Every `SQS-` code with its message, generated with `make errors` |
| RB | Runbooks: [restore and revert](runbooks/restore-and-revert.md), [recover an installation](runbooks/recover-installation.md), [upgrade](runbooks/upgrade.md), [invalid backup path](runbooks/invalid-backup-path.md) | Numbered steps with the expected output |
| BR | [Brand](brand/README.md) | "Node" brand: files, palette, typography, icons and usage rules (ADR 0020) |
| ADR | [Decisions](adr/README.md) | Architecture decision log |

## Evidence state convention

Every statement that constrains the architecture carries one of these states:

| State | Meaning |
|---|---|
| **VERIFIED** | Documented by Blackmagic Design, PostgreSQL or another primary source, or checked by us |
| **REPORTED** | Stated by Blackmagic support in forums or by a trusted community source; still to be checked |
| **FEASIBLE-PENDING** | Technically feasible according to the PostgreSQL or Docker documentation, but not tested with Resolve |
| **INVESTIGATE** | Not enough information; needs a proof of concept before designing on it |
| **DISCARDED** | Impossible, not advisable or outside the project principles |

When a PoC confirms or refutes something, the state is updated here, in `01-research.md` and in
`14-compatibility.md`, with the date and the tested version.

## Glossary

| Term | Definition |
|---|---|
| Project library | Resolve container of projects. On a network it is a PostgreSQL database. Before Resolve 18 it was called "database" |
| Workstation | Computer running DaVinci Resolve that connects to a library |
| Owner role | PostgreSQL role Resolve uses to connect to a library and that owns its tables |
| Control role | PostgreSQL role sqsolve uses to administer the server (`sqsolve_admin`) |
| Control plane | sqsolve and its SQLite database: web users, catalog, jobs, audit log |
| Data plane | The PostgreSQL server holding the libraries |
| Schema fingerprint | Hash of the list of tables, columns and types of a library; detects version changes without reading data |
| Logical backup | `pg_dump` file in custom format of one library, with a manifest |
| Swap | Restore that replaces a library by renaming databases, without writing over the active one |
| Access key | `.resolvedbkey` file that Resolve imports to add a network library |
