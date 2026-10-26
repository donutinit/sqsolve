# Remaining plan

Date: 2026-10-26, with 1.2.2 published.

This file contains unfinished or unvalidated work only. Completed work belongs in
`CHANGELOG.md`, `docs/21-tasks.md`, the accepted ADRs and the acceptance records. Remove an
item from this file when its acceptance criteria are met.

## 1. Follow-ups from 1.2.2

Found while closing 1.2.2 and left out of it because none breaks a promise of the release. Each
one needs a test of its own when it is fixed.

1. `GET /api/v1/jobs/{id}/logs` answers 200 with `null` items for an unknown job, and empty event
   and log lists are `null` instead of `[]`.
2. The detail of `SQS-E005` shows the PostgreSQL socket path and the `sqsolve_admin` role to any
   authenticated user.
3. Replacing a library, moving it to the trash and a purge by hand do not use `jobs.Unique`
   (ADR 0028 and the changelog were narrowed to what does).
4. `reconcilePurge` with no trash database and a previous-state database on the server reports
   `SQS-L046` at every start; the purge journal does not record `PrevDB`.
5. `sqsolve-init` checks the mode of existing secret files and folders, but not of the secret
   files it has just created.
6. `pgidentity` emits the "cluster accepted" event even when its transaction fails.
7. `SyncHBA` after a settings or member change has no audit entry of its own (it predates
   1.2.2).
8. The redaction in `pg.SetRolePassword` drops the wrapped error chain.
9. Retention: `pre_restore` backups are protected and also take places among the last N, so a
   few restores push out the regular backups, including the one just restored
   (`docs/acceptance/1.2.2.md`). Decide whether protected backups should count toward
   `keep_last`.

## 2. Validation still required

### 2.1 Resolve

- Resolve on macOS and Windows, and collaboration between two workstations (PoC-07, CU-06).
- Resolve's own Backup and Restore against sqsolve's files (PoC-04 steps 1, 2 and 4).
- Per-person credentials from Resolve and a database owned by `sqsolve_admin` (PoC-01 steps 3
  and 5).
- A schema fingerprint change during a real Resolve upgrade, including the upgrade window
  (PoC-03).
- Resolve 20.3.2 and the complete version matrix (PoC-02).
- SMTP and ntfy against real servers.

### 2.2 Non-functional requirements

| Requirement | Remaining evidence |
|---|---|
| RNF-USA-03 WCAG 2.2 AA | Run a formal accessibility audit (the 44 px targets are done in 1.2.2) |
| RNF-USA-05 p95 under 300 ms | Measure with 50 libraries and 5000 backups |
| RNF-MAN-03 reproducible build | Compare two clean builds; `-trimpath` and pinned digests alone are not evidence |
| RNF-MAN-04 build without Internet | Decide whether `go mod vendor` is still required, then implement or amend the requirement through an ADR |
| RNF-MAN-05 OpenAPI contract | Add and validate `api/openapi.yaml` |
| RNF-COM-01 Resolve 21.1.x | Validate macOS and Windows; Linux is the only recorded platform |

## 3. Product roadmap after stabilization

1. **Off-site copy with restic (RF-RES-13, CU-28).** Schedule `backup`, `forget --prune` and
   `check`, and support recovery from the remote. The `restic` binary is already approved in
   `docs/06-stack.md`.
2. **Lock after a replacement.** PoC-09 showed that Resolve reconnects after a swap and can save
   over the restored state, and the 1.2.2 acceptance saw it on screen, for a revert too: its next
   save writes its pending changes into the restored library. Rotate the password during
   replacement or keep the library in maintenance until an administrator releases it.
3. **Migration from another server (RF-ACT-06, CU-30).** List PostgreSQL 15 libraries, dump them
   with the matching client version, import with L2 verification and create their roles.
4. **Complete the Resolve validation in section 2.1.** Per-person credentials must be validated
   before work starts on the collaborator portal.
5. **Local templates (RF-BIB-02, E2).** Create libraries without going through Resolve and provide
   a supported replacement for the discarded empty-database strategy.
6. **Collaborator portal (RF-ACC-08, RF-USR-05).** Limit it to libraries granted to each
   collaborator.
7. **Notification grouping (RF-NOT-04)** and **the compatibility matrix in the panel
   (RF-ACT-04)**.
8. **Long-term work:** adopt an existing database (RF-BIB-10), isolated hooks (RF-TAR-08),
   Prometheus metrics (RF-OBS-06), `sqsolve-check`, `.resolvedbkey` (RF-ACC-07), PITR
   (RF-RES-16), OIDC, WebAuthn and the PostgreSQL major-version wizard (RF-ACT-05).
