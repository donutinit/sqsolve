# The plan against what exists

Date: 2026-10-19, with 1.0.0 published. It compares the original plan (`docs/03` product,
`docs/04` requirements, `docs/20` roadmap, written before the first line of code) with what is in
the repository, and ends with what, in the agent's judgment, the project still needs and in what
order.

Legend: **Done**, **Partial**, **No**, and "Validated" when it was also tested with real Resolve
(`docs/poc`, `docs/acceptance/1.0.md`).

## 1. What the plan promised for 1.0

A Resolve library server that installs with a YAML, is administered from the browser, backs up
and verifies on its own, restores without losing anything and recovers on another server with the
backups and a kit. That exists and is deployed. What the plan left for 1.1, 1.2 and 2.0 is mostly
still undone, except three pieces that came in ahead of time (per-person credentials, network
rules, and SMTP and ntfy) and one that was not in the plan and Diego asked for (exporting `.drp`
and `.drx`, also from a backup).

## 2. Functional requirements, one by one

### Installation (RF-INS)

| Requirement | Status | Note |
|---|---|---|
| RF-INS-01 a single Compose | Done, validated on TrueNAS | |
| RF-INS-02 secrets and certificates on first start | Done | |
| RF-INS-03 wizard: language, time zone, administrator, address, policy, kit | Partial | No language choice: English is the only supported language |
| RF-INS-04 single-use installation code | Done, validated | |
| RF-INS-05 refuse to start without volumes or permissions | Done | `SQS-C053`, `SQS-C054` |

### Libraries (RF-BIB)

| Requirement | Status | Note |
|---|---|---|
| RF-BIB-01 create with name, identifier and notes | Done, validated (E1) | The E0 "empty database" strategy does not work with Resolve 21.1 (PoC-01) |
| RF-BIB-02 create from a local template (E2) | No | It would also be the way out for E0 |
| RF-BIB-03 list with state, size, connections, last backup, version | Done | Resolve version is the one declared by hand |
| RF-BIB-04 detail with tabs | Done | Plus the Export tab, outside the plan |
| RF-BIB-05 edit name, notes, limit, version | Done | |
| RF-BIB-06 maintenance | Done, validated (PoC-09) | |
| RF-BIB-07 trash with final backup and purge | Done | Since 1.0.0 also for libraries without a database |
| RF-BIB-08 sessions and terminate session | Done | With an encryption column since 1.0 |
| RF-BIB-09 schema fingerprint | Done, partly validated (PoC-03) | Still need to see it change with a real upgrade |
| RF-BIB-10 adopt an existing database | No | |
| RF-BIB-11 read-only (archived) | No | It depended on PoC-09; nobody asked for it again |
| RF-BIB-12 weekly VACUUM | Done | |

### Credentials and access (RF-ACC)

| Requirement | Status | Note |
|---|---|---|
| RF-ACC-01 owner role with a strong password | Done, validated | |
| RF-ACC-02 reveal password with reauthentication | Changed | ADR 0021: no reauthentication, with audit log |
| RF-ACC-03 rotate password | Done | |
| RF-ACC-04 per-person credentials | Done in code, not validated with Resolve | It was a 1.1 item |
| RF-ACC-05 network rules in `pg_hba.conf` | Done, partly validated (PoC-08) | Through the Tailscale app only one IP arrives |
| RF-ACC-06 connection instructions | Done | |
| RF-ACC-07 `.resolvedbkey` key | No | Only the setting exists, off; depends on PoC-05 |
| RF-ACC-08 collaborator portal | No | |

### Backups (RF-RES)

| Requirement | Status | Note |
|---|---|---|
| RF-RES-01 to 12: manual, schedule, retention, catalog, L1, L2, restore as new and by replacement, download, import, pin, retention preview | Done, validated | Plus activity-based mode (ADR 0022), outside the plan |
| RF-RES-13 off-site copy with restic | No | |
| RF-RES-14 daily backup of the control plane | Done, validated (recovery on mininas) | |
| RF-RES-15 rebuild catalog from manifests | Done | |
| RF-RES-16 PITR with pgBackRest | No | 2.0 |

### Jobs, notifications, users (RF-TAR, RF-NOT, RF-USR)

| Requirement | Status | Note |
|---|---|---|
| RF-TAR-01 to 07: persistent jobs, view, cancel, interrupted, missed runs, maintenance, HMAC webhooks | Done | Live progress over SSE |
| RF-TAR-08 hooks in an isolated container | No | |
| RF-NOT-01 webhook | Done | |
| RF-NOT-02 SMTP and ntfy | Done in code | It was a 1.1 item; not tested against a real server |
| RF-NOT-03 rules by type and severity, test button | Done | |
| RF-NOT-04 grouping of repeated notifications | No | |
| RF-USR-01 administrator, operator, viewer roles | Done | |
| RF-USR-02 optional TOTP, mandatory for administrators | Done | |
| RF-USR-03 API tokens with scope | Done | |
| RF-USR-04 access recovery by CLI | Done | Plus `admin unlock` (ADR 0021) |
| RF-USR-05 collaborator role | No | |
| RF-USR-06 OIDC, RF-USR-07 WebAuthn | No | 2.0 |

### Observation and upgrades (RF-OBS, RF-ACT)

| Requirement | Status | Note |
|---|---|---|
| RF-OBS-01 to 05: health, events, hash-chained audit log, JSONL export, diagnostic bundle | Done | |
| RF-OBS-06 Prometheus metrics | No | |
| RF-ACT-01 SQLite migrations with a prior copy | Done | Four migrations today |
| RF-ACT-02 check PostgreSQL version at startup | Done | |
| RF-ACT-03 Resolve upgrade window | Done, not validated | A real upgrade is missing |
| RF-ACT-04 compatibility matrix in the panel | Partial | "About" points to `docs/14` instead of showing it |
| RF-ACT-05 PostgreSQL major-version wizard | No | 2.0 |
| RF-ACT-06 migration from another server | No | |

## 3. Non-functional requirements that changed or were not measured

| Requirement | Status |
|---|---|
| RNF-SEG-09 reauthentication on dangerous operations | Changed by decision (ADR 0021): typed confirmation and audit log; no reauthentication |
| RNF-USA-01 interface in Spanish and English | Changed by decision: English only, the only supported language (see `CHANGELOG.md`, Unreleased) |
| RNF-USA-03 WCAG 2.2 AA | To be confirmed: care was taken (shapes besides color, focus, measured contrast), no formal audit |
| RNF-USA-05 p95 under 300 ms with 50 libraries and 5000 backups | To be confirmed: not measured |
| RNF-MAN-01 test coverage 80 % | To be confirmed: not measured in CI |
| RNF-MAN-03 reproducible build | To be confirmed: `-trimpath` and pinned digests, two builds not compared |
| RNF-MAN-04 buildable without Internet (`go mod vendor`) | No |
| RNF-MAN-05 OpenAPI validated in tests | No (moved to 1.1) |
| RNF-COM-01 Resolve 21.1.x | Validated on Linux; macOS and Windows untested |
| The rest (reliability, security, operation) | Done per the plan; the CI failure battery covers cuts in the middle of a job |

## 4. Use cases of the plan

Done and validated: CU-01 to 05, 07 to 15, 17, 18, 20, 22, 23, 24. Done without validation with
Resolve: CU-16 (restoring a downloaded backup in Resolve), CU-19 email and ntfy, CU-21 upgrade
window, CU-25 and CU-26 (1.1 items, already in code). Untested for lack of a second workstation:
CU-06. Not done: CU-27 portal, CU-28 restic, CU-29 hooks, CU-30 migration from another server,
CU-31 `.resolvedbkey`, CU-32 PITR, CU-33 PostgreSQL major version.

## 5. What remains unvalidated with Resolve

- Resolve on macOS and Windows, and collaboration between two workstations (PoC-07, CU-06).
- Resolve's own Backup and Restore against sqsolve's files (PoC-04 steps 1, 2 and 4).
- Per-person credentials from Resolve and the database owned by `sqsolve_admin` (PoC-01 steps 3
  and 5).
- That the fingerprint changes with a version upgrade (PoC-03) and the upgrade window.
- Resolve 20.3.2 and the full version matrix (PoC-02).

## 6. What it needs, in order

1. **Off-site copy with restic (RF-RES-13, CU-28).** It is the only piece that turns "do not lose
   projects" into a promise against losing the server, not just against human error. Today
   backups and data live on the same NAS. A small dependency (the `restic` binary), already
   planned in `docs/06` and `docs/11` §9: scheduled `backup`, `forget --prune` and `check`,
   restore from the remote. This comes first.
2. **Lock after a replacement.** PoC-09 showed that Resolve reconnects on its own after the swap
   and saves over what was restored if someone had the project open. A new password on replace,
   or the library in maintenance until the administrator releases it, closes that hole. It is
   little code and protects the most delicate operation.
3. **Migration from another server (RF-ACT-06, CU-30).** Diego's own case: a PostgreSQL 15 with
   several libraries. List, back up with the right version's `pg_dump`, import with L2
   verification and create the library with its role. Today it is manual work.
4. **Validation on macOS and Windows and of collaboration.** It is not a feature, but it is the
   biggest unknown left in the product, and the one that separates "it works for me" from "it
   works for a studio". With a second workstation, an afternoon.
5. **Local templates (RF-BIB-02, E2).** Create libraries without going through Resolve and, along
   the way, rescue the precreated-database strategy that does not work today.
6. **Collaborator portal (RF-ACC-08, RF-USR-05).** Useful for studios with freelancers;
   pointless until per-person credentials are tested with Resolve.
7. **Notification grouping (RF-NOT-04)** and **compatibility matrix in the panel (RF-ACT-04)**:
   cheap polish.
8. **Interface language (RNF-USA-01).** Settled: the project is English only, so no translation
   infrastructure is needed.
9. **Hooks in a separate container, Prometheus, `sqsolve-check`, `.resolvedbkey`, PITR, OIDC and
   WebAuthn**: per the plan and in that order; none blocks a small studio today.

What is not needed and is better not done without a new reason: read-only mode (RF-BIB-11), the
external server mode (2.0) and the image registry mirror.
