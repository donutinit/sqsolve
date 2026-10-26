# Remaining plan

Date: 2026-10-25, with 1.2.1 published.

This file contains unfinished or unvalidated work only. Completed work belongs in
`CHANGELOG.md`, `docs/21-tasks.md`, the accepted ADRs and the acceptance records. Remove an
item from this file when its acceptance criteria are met.

## 1. Ship the 1.2.2 stabilization release

Security and recovery correctness take priority over new features after the 2026-10-25 repository
audit. Keep each fix small enough to review independently, but release them together because they
enforce the same promise: an operation is either durable and audited, or it reports failure and
can be reconciled.

### 1.1 Release blockers

| Order | Change | Implementation | Acceptance |
|---|---|---|---|
| P0.1 | Confine backup paths | Validate the complete v1 manifest in `ReadManifest`; require `file` to be a base name with the expected suffix; resolve symlinks and use `fsx.Within` in catalog rebuild and every catalog path lookup; make `AbsPath` return an error; never download, restore or delete a record whose path is invalid | Tests cover `../`, absolute paths, symlinks, unsupported manifest versions and malformed IDs. Existing invalid catalog rows are reported and disabled without deleting any file. The documented `api/schemas/manifest.v1.json` exists and matches the Go type |
| P0.2 | Make the job journal mandatory | In `jobs.Run.Step`, persist the step before calling it and propagate insertion errors. In `Journal`, require exactly one affected row. Keep using the existing reconciliation journals instead of adding another recovery layer | A refused `job_steps` insert and a zero-row journal update both stop the operation before PostgreSQL or files change. Reconciliation tests still pass after a forced process stop |
| P0.3 | Finish ADR 0026 | Inventory every state change and disclosure. Use `RecordTx` for SQLite changes, `Audit.Do` for PostgreSQL and files, `jobs.Audited` for jobs and `Audit.Require` for disclosures. Remove ignored audit results | With an injected `audit_log` failure, each protected action returns `SQS-X040` and does not happen. A result-entry failure after an external change returns `SQS-X041`. Settings, backup pinning, TOTP, unlock, notification channels, member credentials and job cancellation are covered |
| P0.4 | Stop false-success destructive jobs | Propagate required PostgreSQL and SQLite errors in purge and recreate. Check role lookup, credential storage, grants, library state and schedule updates. If an error happens after an irreversible step, retain the journal and return a recoverable failure instead of emitting success | Fault tests at every ignored call leave the job failed or interrupted, never succeeded. Startup reconciliation produces one catalog state that matches PostgreSQL and keeps backups |

### 1.2 Consistency and regression work

1. Move deletion of other sessions into the password-change transaction. An injected session
   deletion failure must roll back the new password and its audit entry.
2. Propagate `fsx.SyncDir` errors when deleting backup files and update the catalog only after the
   directory entry is durable. Extend the existing power-loss model.
3. Decide the identity of a unique job. If requests with different parameters must remain distinct,
   include canonical parameters in `jobs.Unique`; otherwise document and test that the first
   request wins. This changes job semantics, so record the decision in a Proposed ADR before code.
4. Raise `internal/` coverage to 80 %, concentrating on `internal/api`, `internal/web`,
   recovery fault paths and audit failures. Do not add tests of getters only to move the number.
5. Bring interactive controls to a 44 px target at every breakpoint, including small buttons,
   account links, sign-out and tabs. Preserve the current visual size with spacing where needed.
6. Replace the Spanish slug fallback with an English one and reconcile the restore and mobile
   navigation documentation with ADR 0021 and the implementation.
7. Fix notices with inline markup. `.flash` is a flex container, so each text run and inline
   element of a notice becomes its own column and long values overflow the card (seen on
   `/backups/{id}/restore` with 11 open connections listed). Render the notice as one text block
   next to its shape and wrap long values.

### 1.3 Release and deployment

1. Implement P0.1 through P0.4 in that order. After each one, run `make check` and the integration
   tests of the touched packages. Before the release, run `make test-integration`, `make fuzz`,
   `make scenarios`, the image smoke test and binary `govulncheck`.
2. Do not automatically rewrite or delete existing catalog rows during upgrade. On startup, report
   invalid backup paths as a critical event and block operations on those rows until an
   administrator reviews them.
3. Build 1.2.2 through the existing release workflow so the image, SBOM, provenance, signature and
   Compose attachment are produced by CI. Add no dependency; the required validation and
   confinement primitives already exist in the repository and standard library.
4. Deploy first to mininas with a current control-plane backup and at least one verified backup per
   library. Exercise catalog rebuild, backup download and deletion, password session revocation,
   purge, recreate, process interruption and restart recovery.
5. Repeat the restore and reconnect checks with real Resolve before promoting the image. Record the
   result in `docs/acceptance/1.2.2.md`; do not generalize it to platforms or Resolve versions that
   were not tested.
6. Publish 1.2.2 only when every release blocker and regression test passes. Roll back the binary
   and image if needed, but keep the control-plane backup because no rollback may discard catalog
   or audit entries created by the newer version.

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
| RNF-USA-03 WCAG 2.2 AA | Complete the 44 px work in section 1.2 and run a formal accessibility audit |
| RNF-USA-05 p95 under 300 ms | Measure with 50 libraries and 5000 backups |
| RNF-MAN-01 test coverage 80 % | Raise the recorded 70.5 %, focusing on API, web, recovery and audit paths |
| RNF-MAN-03 reproducible build | Compare two clean builds; `-trimpath` and pinned digests alone are not evidence |
| RNF-MAN-04 build without Internet | Decide whether `go mod vendor` is still required, then implement or amend the requirement through an ADR |
| RNF-MAN-05 OpenAPI contract | Add and validate `api/openapi.yaml` |
| RNF-COM-01 Resolve 21.1.x | Validate macOS and Windows; Linux is the only recorded platform |

## 3. Product roadmap after stabilization

1. **Off-site copy with restic (RF-RES-13, CU-28).** Schedule `backup`, `forget --prune` and
   `check`, and support recovery from the remote. The `restic` binary is already approved in
   `docs/06-stack.md`.
2. **Lock after a replacement.** PoC-09 showed that Resolve reconnects after a swap and can save
   over the restored state. Rotate the password during replacement or keep the library in
   maintenance until an administrator releases it.
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
