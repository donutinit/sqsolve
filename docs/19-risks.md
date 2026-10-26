# 19. Risks

Probability (P) and impact (I): H high, M medium, L low. Reviewed with each release; this review
is for 1.2.2 (2026-10-26).

## 1. Technical and compatibility

| ID | Risk | P | I | Mitigation | Early signal |
|---|---|---|---|---|---|
| T-01 | Resolve changes its behaviour with PostgreSQL in a new version | M | H | Black box (ADR 0007); compatibility script within 14 days; upgrade window; `pre_upgrade` backups | Release notes that mention Project Server or libraries |
| T-02 | Resolve requires superuser or being the database owner | M | M | Strategy E1, confirmed in PoC-01 with 21.1.1 (owner = library role, renames with superuser over the socket) | PoC-01 |
| T-03 | Resolve 20.3.x fails with PostgreSQL 18 | M | M | None in place: sqsolve ships PostgreSQL 18 only and 20.3.x has not been tested (PoC-02 not run for it) | PoC-02 |
| T-04 | The `.resolvedbkey` format is closed or changes | H | L | sqsolve does not generate access keys; the panel gives the connection values to type in Resolve | PoC-05 |
| T-05 | With a project left open, Resolve reconnects after a replace or revert and saves its pending changes into the restored library | M | H | Seen in PoC-09 and on screen in the 1.2.2 acceptance; the replace and revert pages always ask to close the project in Resolve on every workstation first; "as new" mode by default; the lock after a replacement is on the roadmap (`20-roadmap.md`) | PoC-09, `acceptance/1.2.2.md` |
| T-06 | Libraries restored from the same backup get mixed up in Resolve (repeated internal identifiers) | L | M | PoC-04 step 3, partly done: Resolve opened a copy restored as new while the original stayed in its list; whether the original was left untouched was not checked. E2 is discarded | PoC-04 |
| T-07 | `pg_dump` blocks Resolve during schema changes | L | M | `--lock-wait-timeout`; scheduling outside working hours. The measurement of PoC-10 was not run | `log_lock_waits` |
| T-08 | Docker hides the source IP and network rules are useless | M | M | PoC-08: on the LAN the workstation's IP is visible with Docker on TrueNAS; through TrueNAS's Tailscale app every connection arrives from the container's address (README, "Network rules"). Other VPNs untested | PoC-08 |
| T-09 | A logic error in retention deletes needed backups | L | H | Pure function with property tests; `min_verified`; preview; two-phase deletion; pinning | Tests |
| T-10 | Power outage in the middle of the swap | L | H | Step journal and recovery table (11 §7.3); fault injection tests | Fault tests |
| T-11 | Full disk on `pgdata` stops PostgreSQL | M | H | Alerts at 80 % and 90 %; every restore, test restore (L2), recreation and export checks the free space first and refuses without it | Space indicator |
| T-12 | SQLite corruption | L | M | WAL, daily copies in `backups/_system`, catalog rebuild from manifests | Daily `PRAGMA integrity_check` |
| T-13 | Collation changes with a new image | L | H | `builtin` provider (PoC-01 step 7) and collation version indicator | Collation indicator |
| T-14 | Collaboration fails because of the workstations' network and the server gets the blame | H | L | Editors guide with ports. Collaboration between two workstations is not validated (PoC-07) and there is no `sqsolve-check` tool (`20-roadmap.md`) | User reports |
| T-15 | Change in the official PostgreSQL image (paths, entrypoint), or an installation that stays on an old PostgreSQL minor because its copy of the compose file pins the digest | L | M | Pinned digest; Dependabot opens a weekly pull request for each new `postgres:18.x-trixie` (7-day cooldown) and the full CI, including the smoke test with the reference compose, runs on it; `make pg-image-check` keeps the `Dockerfile` and `deploy/compose.yaml` equal; the release notes give the new `image:` line and the user guide says to apply it | Dependabot pull request |
| T-16 | A new version of Resolve changes the `.drp` or `.drx` format and the export produces files that import badly | M | M | Off by default; schema contract that turns it off by itself (`SQS-E001`); `SQS-E004` guards; per-version verification kit (`22` §6); "no guarantee" notice | New unregistered fingerprint |

## 2. Security

| ID | Risk | P | I | Mitigation |
|---|---|---|---|---|
| S-01 | Someone exposes 5432 to the Internet | M | H | Documentation, strong passwords and SCRAM, network rules. The panel does not warn when the advertised address is public |
| S-02 | SQL or command injection in sqsolve | L | H | Controls T-06 and T-07 of `08-security.md`; role without superuser; fuzzing |
| S-03 | Loss of the master key | M | M | Mandatory recovery kit; the libraries remain safe; documented credential rotation |
| S-04 | Supply chain of Go or of the base image | L | H | Closed dependency list, 7-day wait (also Dependabot's cooldown), `govulncheck` on the source and on the binary inside the image, pinned digest, SBOM, signatures; images are published only after CI passes |
| S-05 | A compromised workstation leaks a library password | M | M | Per-person credentials that are revoked one by one, rotation of the library password, network rules. Per-person credentials have not been tested with Resolve (PoC-01 step 5) |

## 3. Legal

| ID | Risk | P | I | Mitigation |
|---|---|---|---|---|
| J-01 | Improper use of Blackmagic trademarks | L | M | Own name; descriptive use; independence notice (18 §4) |
| J-02 | Publicly describing the `.resolvedbkey` format | L | M | Only observation of files generated by the user; legal consultation before publishing anything about it; no distribution of Blackmagic files |
| J-03 | Unintentionally distributing the Resolve schema (fixtures, templates, PoC logs) | L | M | Synthetic fixtures; templates local only; PoC logs outside the repo; review in PR |
| J-04 | Confusion with Studio Server | L | L | No copying of text or design; own documentation |
| J-05 | License incompatibility of dependencies | L | M | License review of the SBOM in CI |

## 4. Operational and project

| ID | Risk | P | I | Mitigation |
|---|---|---|---|---|
| O-01 | A single maintainer | H | M | Complete documentation, simple stack, ADRs, instructions for agents in `AGENTS.md` |
| O-02 | Limited access to hardware and licenses to test Resolve | M | M | PoCs concentrated in sessions; short compatibility script; community reports in a fixed format |
| O-03 | Growing scope (media modules, desktop clients) | M | M | Scope in `03-product.md`; everything new goes through ADR and roadmap |
| O-04 | Users who do not test their restores | H | H | Weekly test restore (L2) of the latest verified backup of each library (`system.verify_all`, Sundays at 05:00). There is no reminder of the L3 drill |
| O-05 | The administrator uses the `latest` tag and receives a major version without reading the notes | M | M | The documentation uses version tags. sqsolve does not ask for confirmation before a new major version migrates SQLite |

## 5. Known issues

Behavior of 1.2.2 that is known to be wrong or weaker than it should be. None of them affects the
safety of backups, restores or recovery. Other documents cite them by ID.

| ID | Behavior in 1.2.2 | Effect |
|---|---|---|
| K-01 | `GET /api/v1/jobs/{id}/logs` answers 200 with `null` items for a job that does not exist, and the empty lists of jobs, job steps, logs, events, audit entries and `attention` come back as `null` instead of `[]` (`10-api.md` §1) | A client has to read `null` as an empty list and cannot tell an unknown job from one without logs |
| K-02 | The detail of `SQS-E005` includes the PostgreSQL socket path and the `sqsolve_admin` role, and any authenticated user can see it | Internal names are disclosed; no secret is |
| K-03 | Replacing a library, moving it to the trash, recovering it from the trash and a purge started by hand are queued without the unique job identity of ADR 0028 | Two identical requests for one library are not merged into one job |
| K-04 | When a purge is interrupted with no trash database left and a previous-state database still on the server, reconciliation reports `SQS-L046` at every start; the purge journal does not record the previous-state database | The same warning at every start |
| K-05 | `sqsolve-init` checks the mode of existing secret files and folders, but not of the secret files it has just created | A secret file created with a wider mode is not caught by that run of `sqsolve-init` |
| K-06 | `pgidentity` emits its "cluster accepted" event even when its transaction fails | The event can announce a cluster identity that was not stored |
| K-07 | Rewriting `pg_hba.conf` (`SyncHBA`) after a settings or member change has no audit entry of its own | The rewrite does not appear in the audit log as a separate entry |
| K-08 | The redaction in `pg.SetRolePassword` drops the wrapped error chain | Callers cannot match the underlying PostgreSQL error with `errors.Is` or `errors.As` |
| K-09 | `pre_restore` backups are protected and also take places among the last N of `keep_last` | A few restores or reverts can push regular backups out, including the one just restored (seen in the 1.2.2 acceptance) |
