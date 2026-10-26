# 02. Proofs of concept

The PoCs turn the INVESTIGATE items of `01-research.md` into facts before writing the code that
depends on them. Each PoC produces a report in `docs/poc/poc-NN-<name>.md` with the date, exact
versions, steps, results and the decision taken.

## Rules for all PoCs

- They run with software legally obtained by Diego (free Resolve and Studio) on his
  workstations. Resolve does not run in CI or on lizeth (no GPU).
- Resolve's behaviour is only observed from the outside: PostgreSQL logs, system catalog,
  network traffic and files the user generates. No decompiling or modifying binaries.
- Logs containing Resolve's SQL statements are kept outside the repo (Diego's `/mnt/...`) and
  are not published. Only conclusions go into the report (for example, "it runs `CREATE
  DATABASE` while connected to the `postgres` database"), not the schema.
- Base environment: the lab `compose.yaml` in `deploy/lab/` (SQS-020), with PostgreSQL 18 and
  `log_statement = 'all'`, `log_connections = on`, `log_disconnections = on`.

## Summary and blockers

| PoC | Topic | Priority | Blocks |
|---|---|---|---|
| PoC-01 | Library lifecycle and minimum privileges | Critical | SQS-200 to SQS-230 (libraries). **Partially done on 2026-10-19** (`docs/poc/poc-01-lifecycle.md`): E1 confirmed, E0 does not work with 21.1.1; member role and `sqsolve_admin` as owner pending |
| PoC-02 | Resolve × PostgreSQL version matrix | Critical | ADR 0006, `14-compatibility.md` |
| PoC-03 | Schema fingerprint and upgrade detection | High | SQS-240. **Partially done on 2026-10-19** (`docs/poc/poc-03-fingerprint.md`): stable under use; a real Resolve upgrade is still missing |
| PoC-04 | Backup format and cross restore | Critical | SQS-300 to SQS-340 (backups). **Partially done on 2026-10-19** (`docs/poc/poc-04-backup-format.md`): Resolve opens libraries restored by sqsolve; its own Backup and Restore remain untested |
| PoC-05 | `.resolvedbkey` keys | Medium | SQS-610 (experimental module) |
| PoC-06 | TLS and SCRAM from Resolve | High | Default values of `pg_hba.conf`. **Done on 2026-10-19** (`docs/poc/poc-06-tls.md`): Resolve 21.1.1 Linux encrypts on its own and connects with mandatory `hostssl` |
| PoC-07 | Collaboration and network traffic | High | Documentation for editors |
| PoC-08 | Network: source IP in Docker, port, VPN | High | Network rules per library, remote access guide. **Done on 2026-10-19** (`docs/poc/poc-08-network.md`): real IP on the LAN; through the TrueNAS Tailscale app the container IP arrives |
| PoC-09 | Restricted states and forced disconnect | Critical | Restore by swap, maintenance mode. **Done on 2026-10-19** (`docs/poc/poc-09-forced-disconnect.md`): Resolve reconnects on its own after the swap; the project must be closed first. Seen on screen in the 1.2.2 acceptance (2026-10-26), for a revert too |
| PoC-10 | Locks and `pg_dump` duration under real use | Medium | Scheduling recommendations |

Suggested order: PoC-01, PoC-02 and PoC-06 in one session; then PoC-04 and PoC-09; then the
rest.

## PoC-01. Lifecycle and minimum privileges

**Question.** What does Resolve do when creating and connecting a library, and with which
minimum privileges does it work?

**Procedure.**

1. Create a role `lab_a` with `LOGIN NOSUPERUSER NOCREATEROLE CREATEDB` and a SCRAM password.
   In Resolve 21.1: "Create" a library `lab_a`. Record which database it connects to first
   (`log_connections`), which database-level statements it runs (`CREATE DATABASE` with which
   template, encoding and collation options) and whether it creates extensions or functions.
2. Repeat with a `NOCREATEDB` role and an empty database `lab_b` created beforehand with owner
   `lab_b`: try "Create" and "Connect". Write down the literal error messages.
3. Database `lab_c` owned by `sqsolve_admin`, with the `public` schema owned by `lab_c` and
   `GRANT CONNECT, TEMP ON DATABASE lab_c TO lab_c`. Try "Create" and "Connect" as `lab_c`.
4. With an already initialized library, remove `CREATEDB` from the role and verify that
   creating, opening, saving and deleting projects and importing media keeps working.
5. Member role: `CREATE ROLE lab_a_ana LOGIN IN ROLE lab_a`, `ALTER ROLE lab_a_ana SET role =
   'lab_a'`. Connect Resolve as `lab_a_ana`, create a project, and check in the catalog that
   the new objects are owned by `lab_a`.
6. Count simultaneous connections per workstation (`pg_stat_activity`) at rest, with a project
   open and during a render. Write down `application_name` and `client_addr`.
7. With the database created on a cluster with `--locale-provider=builtin --builtin-locale=C.UTF-8`,
   check that the ordering of projects and bins in Resolve is acceptable with accented names.
8. Repeat 1 and 4 with Resolve 20.3.2.

**Success.** The minimum set of privileges for each phase (initialization and daily use) and
the applicable creation strategy (E0, E1 or E2 from `07-libraries-and-postgresql.md`) are
known.

**Decision it enables.** Creation strategy, ownership model (database owner: `sqsolve_admin` or
the library role), per-person credentials and `max_connections`.

**Plan B.** If Resolve requires owning the database, the owner will be the library role and the
swap renames will be done with the superuser over the local socket, with an audit entry.

## PoC-02. Version matrix

**Question.** Which Resolve versions work with which PostgreSQL versions?

**Procedure.** For each combination, create a library, create and open a project, import
media, edit a timeline, close and reopen, back up from Resolve:

| Resolve \ PostgreSQL | 16 | 17 | 18 |
|---|---|---|---|
| 21.1.x | test | test | test |
| 20.3.2 | test | test | test |
| 19.x (optional) | | | test |

**Success.** A table with "works", "works with warnings" or "fails" and the literal message.

**Decision it enables.** PostgreSQL 18 is confirmed as the only default version, or an
alternative profile (17) is added only for teams tied to a Resolve version.

## PoC-03. Schema fingerprint

**Question.** Does the fingerprint defined in `07-libraries-and-postgresql.md` §9 change when
Resolve upgrades a library and stay stable under normal use?

**Procedure.** Create a library with 20.3.2, compute the fingerprint, use it for an hour
(projects, bins, timelines), recompute; open it with 21.1 and accept the upgrade if Resolve
offers it; recompute; open a project in 21.1; recompute.

**Success.** The fingerprint does not change under normal use and changes when the schema
changes. If upgrading does not change the schema (only project data), it is documented that the
fingerprint does not detect that case and the upgrade window becomes mandatory in the guide.

## PoC-04. Backup format and cross restore

**Questions.** Is Resolve's backup file a `pg_dump` file in custom format? Does Resolve
restore a backup made by sqsolve? Can two libraries restored from the same backup coexist?

**Procedure.**

1. Back up from the Project Manager of Resolve 21.1 and 20.3.2. Inspect with `file` and
   `pg_restore --list`. Write down the archive format version.
2. Back up with `pg_dump --format=custom --quote-all-identifiers` from PostgreSQL 18. Restore
   from the Resolve 21.1 and 20.3.2 interface.
3. Restore with `pg_restore --no-owner --no-privileges --role=<role>` into a new database with
   another name and another role. Connect both libraries (original and copy) to the same
   workstation; open, modify and close projects in both; check that they do not mix.
4. Try the default compression (gzip) and `--compress=zstd`. Write down which one Resolve
   accepts.

**Success.** It is known whether sqsolve backups can be restored from Resolve, with which
options, and whether restoring as a copy is safe.

**Plan B.** If Resolve does not accept sqsolve's files, the interface says so clearly and
restores are done only from sqsolve. Nothing in the backup design depends on that
compatibility.

## PoC-05. `.resolvedbkey` keys

**Question.** Can sqsolve generate keys that Resolve accepts?

**Procedure.**

1. On a workstation with the official Project Server (Windows or macOS), add a library hosted
   on sqsolve, enable sharing if required and export a key.
2. Document the XML structure (elements, attributes, encoding, whether the password is in the
   clear or obfuscated). Do not copy the file into the repo: only the description of the
   fields.
3. Generate an equivalent key by hand with other values. Import it into Resolve 21.1 and 20.3.2
   on Windows, macOS and Linux. Try a host name instead of an IP, passwords with symbols and a
   user other than `postgres`.

**Success.** Resolve imports the generated key on all three platforms.

**Plan B.** If the format is encrypted, signed or unstable across versions, the module is
DISCARDED and the interface offers only manual connection instructions.

## PoC-06. TLS and SCRAM

**Question.** Does Resolve connect with `scram-sha-256` and over TLS?

**Procedure.** `password_encryption = scram-sha-256` and `pg_hba` with `scram-sha-256` only.
With `ssl = on`, check `pg_stat_ssl` for each Resolve connection. Then change the lines to
`hostssl` and test that Resolve still connects. Try `ssl_min_protocol_version = TLSv1.3`.

**Success.** It is known whether TLS can be mandatory by default. If Resolve uses TLS in
"prefer" mode without verifying the certificate, it is documented that encryption protects
against passive eavesdropping but not against impersonation, and the VPN recommendation for
access outside the LAN stays.

**Result (2026-10-19).** Done; report in `docs/poc/poc-06-tls.md`. Resolve Studio 21.1.1 on
Linux negotiates TLS 1.3 on its own, accepts the self-signed certificate without verifying it
and also connects with mandatory `hostssl` ("TLS only" mode). Pending on macOS and Windows.

## PoC-07. Collaboration and traffic

**Questions.** What traffic does collaboration generate? Does it work with the free version of
21?

**Procedure.** Two workstations with Studio on the same library, collaboration enabled in the
project. Capture with `tcpdump` or Wireshark on both workstations and on the server. Test chat,
bin and timeline locking, refreshing collaborators' changes. Block 50059-50314 in the firewall
of one workstation and observe the symptom. Repeat with a workstation running free Resolve.

**Success.** List of ports and traffic directions; visible symptoms of each failure for the
troubleshooting guide; answer about the free version.

## PoC-08. Network

**Questions.** Does PostgreSQL see the client's real IP with the port published by Docker? Does
Resolve accept another port? Do the library and collaboration work over a VPN?

**Procedure.**

1. On Diego's NAS, with the reference Compose file and a bridge network, connect from a
   workstation and read `client_addr` in `pg_stat_activity`. Repeat with `network_mode: host`
   for comparison.
2. Try `IP:port` and a host name in Resolve's location field.
3. With WireGuard (or Headscale), connect a remote workstation to the library and measure the
   experience when opening and saving projects. Test collaboration between a local and a remote
   workstation.

**Success.** It is decided whether per-library network rules are reliable with bridge or
whether `host` is documented as a requirement to use them; the tested remote access is
documented.

## PoC-09. Restricted states and forced disconnect

**Questions.** How does Resolve react when sqsolve cuts its connections, locks the database or
replaces it with a restored one under the same name?

**Procedure.** With a project open on a workstation:

1. `ALTER DATABASE x ALLOW_CONNECTIONS false` and `pg_terminate_backend` on its sessions. Write
   down Resolve's message and whether it retries.
2. Swap: restore a copy into `x__restore_1`, rename `x` to `x__prev_1` and `x__restore_1` to
   `x`, reopen connections. Does Resolve reconnect and see the restored state? Are there local
   caches showing stale data?
3. `ALTER DATABASE x SET default_transaction_read_only = on`. Does Resolve open projects in
   read-only mode or fail?
4. `ALTER ROLE x NOLOGIN` with open sessions.

**Success.** The visible behaviour is known in order to write the interface warnings and the
"before restoring" guide; it is decided whether the "archived (read only)" state is viable.

## PoC-10. Locks and `pg_dump` duration

**Question.** Does a hot backup affect editors?

**Procedure.** With a large test library (several projects with long timelines; size recorded
with `pg_database_size`), run `pg_dump` while one workstation edits and another saves
frequently. Measure duration, lock waits (`pg_locks`, `log_lock_waits = on`) and the experience
in Resolve. Repeat the test restore into an auxiliary database.

**Success.** Duration data per size for the guide; confirmation that the hot backup does not
block normal work or, if it does, in which operations.
