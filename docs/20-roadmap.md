# 20. Roadmap

No dates. What sqsolve does today is described in the other documents, the history of each
release is in `CHANGELOG.md` and the known issues of the current release are in `19-risks.md`
§5.

## 1. Released

| Version | Date | Content |
|---|---|---|
| 0.1.0 to 0.1.6 | 2026-10-18 and 2026-10-19 | Libraries, access, verified backups, restore, panel, API, notifications and recovery kit; visual identity (ADR 0020), proportionate security (ADR 0021), activity-based backups (ADR 0022), experimental `.drp` and `.drx` export (ADR 0019), signed images |
| 1.0.0 | 2026-10-19 | First stable version (`acceptance/1.0.md`, `poc/`) |
| 1.1.0 to 1.1.2 | 2026-10-20 | English as the only language; export catalog with separate views |
| 1.2.0 | 2026-10-21 | Fixes from the stability audit of 2026-10-20 |
| 1.2.1 | 2026-10-22 | Fixes from the repository audit of 2026-10-21 |
| 1.2.2 | 2026-10-26 | Stabilization, checked on the test installation and with Resolve (`acceptance/1.2.2.md`) |

## 2. Next

In order of priority:

1. **Off-site copy with restic (RF-RES-13, CU-28).** Schedule `backup`, `forget --prune` and
   `check`, and recover from the remote. The `restic` binary is already approved in `06-stack.md`.
2. **Lock after a replacement.** PoC-09 and the 1.2.2 acceptance showed that a Resolve with the
   project open reconnects after a replace or revert and saves its pending changes into the
   restored library (`19-risks.md` T-05). Rotate the password during the replacement, or keep the
   library in maintenance until an administrator releases it.
3. **Migration from another server (RF-ACT-06, CU-30).** List PostgreSQL 15 libraries, dump them
   with the matching client version, import with L2 verification and create their roles.
4. **The Resolve validation that is missing** (`14-compatibility.md` §2.1). Per-person
   credentials are validated before work starts on the collaborator portal.
5. **Local templates (RF-BIB-02, E2).** Create libraries without going through Resolve, as the
   supported replacement for the discarded empty-database strategy.
6. **Collaborator portal (RF-ACC-08, RF-USR-05)**, limited to the libraries granted to each
   collaborator.
7. **The compatibility matrix in the panel (RF-ACT-04).**
8. **Long term:** adopt an existing database (RF-BIB-10), isolated hooks (RF-TAR-08), Prometheus
   metrics (RF-OBS-06), capacity of the media volumes, `sqsolve-check` for workstations,
   `.resolvedbkey` access keys (RF-ACC-07), schedulable maintenance, PITR (RF-RES-16), OIDC,
   WebAuthn, the PostgreSQL major-version wizard (RF-ACT-05) and an external server mode.

The non-functional requirements that 1.2.2 does not meet or has no evidence for are marked in
`04-requirements.md` §2.

## 3. Priorities

1. That nothing loses data (backups, swap, retention, recovery after outages).
2. That a workstation connects without help.
3. That the administrator always knows the state (notifications, health, audit log).
4. Convenience and team features.

Verification, safe swap and recovery are never cut to make room for anything else.
