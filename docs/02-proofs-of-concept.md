# 02. Proofs of concept

The PoCs were defined to turn the INVESTIGATE items of `01-research.md` into facts before writing
the code that depends on them. This document keeps each PoC's question and procedure and records
which ones ran, what they found and which were not run. Each PoC that ran has a report in
`docs/poc/poc-NN-<name>.md` with the date, exact versions, steps, results and the decision taken.
Resolve behaviour that neither those reports nor the acceptance records (`docs/acceptance/`) cover
is not validated with real Resolve.

## Rules for all PoCs

- They run with software legally obtained by Diego (free Resolve and Studio) on his
  workstations. Resolve does not run in CI or on lizeth (no GPU).
- Resolve's behaviour is only observed from the outside: PostgreSQL logs, system catalog,
  network traffic and files the user generates. No decompiling or modifying binaries.
- Logs containing Resolve's SQL statements are kept outside the repo (Diego's `/mnt/...`) and
  are not published. Only conclusions go into the report (for example, "it runs `CREATE
  DATABASE` while connected to the `postgres` database"), not the schema.
- Base environment of the PoCs that ran: the sqsolve test installation on mininas (TrueNAS
  25.10.7, Custom App, PostgreSQL 18.6, sqsolve 0.1.5 and 0.1.6) and Diego's Linux workstation
  with Resolve Studio 21.1.1 build 10. The results come from the system catalog,
  `pg_stat_activity`, `pg_stat_ssl`, Resolve's log and its scripting API. A separate lab Compose
  with `log_statement = 'all'` was not built.

## Summary

| PoC | Topic | Priority | Blocks | Status |
|---|---|---|---|---|
| PoC-01 | Library lifecycle and minimum privileges | Critical | Creation strategy, roles and credentials (`07` §3 to §6, ADR 0008) | **Partly run on 2026-10-19** (`docs/poc/poc-01-lifecycle.md`): E1 works, E0 does not work with 21.1.1, daily use works without `CREATEDB`. Not run: a database owned by `sqsolve_admin` (step 3) and a member role (step 5), so sqsolve's per-person credentials are not validated with Resolve |
| PoC-02 | Resolve × PostgreSQL version matrix | Critical | ADR 0006, `14-compatibility.md` | **Not run.** The records cover only Resolve Studio 21.1.1 with PostgreSQL 18.6; 20.3.2 and the rest of the matrix are not validated |
| PoC-03 | Schema fingerprint and upgrade detection | High | `library.schema_changed` (`07` §9) and the upgrade window (`14` §5) | **Partly run on 2026-10-19** (`docs/poc/poc-03-fingerprint.md`): stable under use. Not run: a real Resolve upgrade, so a fingerprint change on upgrade is not validated |
| PoC-04 | Backup format and cross restore | Critical | Backup and restore (`11`), any claim about Resolve's own Backup and Restore | **Partly run on 2026-10-19** (`docs/poc/poc-04-backup-format.md`): Resolve opens a library restored by sqsolve. Not run: steps 1, 2 and 4, so Resolve's own Backup and Restore against sqsolve's files is not validated |
| PoC-05 | `.resolvedbkey` keys | Medium | `.resolvedbkey` module (ADR 0013) | **Not run.** sqsolve does not generate keys |
| PoC-06 | TLS and SCRAM from Resolve | High | Default values of `pg_hba.conf`, "TLS only" mode | **Run on 2026-10-19** (`docs/poc/poc-06-tls.md`): Resolve 21.1.1 on Linux encrypts on its own and connects with mandatory `hostssl`. Not tested on macOS or Windows |
| PoC-07 | Collaboration and network traffic | High | Documentation for editors, ADR 0012 | **Not run.** Collaboration between two workstations is not validated |
| PoC-08 | Network: source IP in Docker, port, VPN | High | Network rules per library, remote access guide | **Partly run on 2026-10-19** (`docs/poc/poc-08-network.md`): real IP on the LAN; through the TrueNAS Tailscale app the container IP arrives. Not run: another port or a host name in Resolve, Resolve and collaboration over the VPN |
| PoC-09 | Restricted states and forced disconnect | Critical | Restore by swap, revert, maintenance mode | **Run on 2026-10-19** (`docs/poc/poc-09-forced-disconnect.md`): Resolve reconnects on its own after the swap; the project must be closed first. Seen on screen in the 1.2.2 acceptance (2026-10-26), for a revert too. Not run: steps 3 and 4 |
| PoC-10 | Locks and `pg_dump` duration under real use | Medium | Scheduling recommendations | **Not run.** The effect of a hot backup on editors is not measured |

PoC-01, 03, 04, 06, 08 and 09 ran on 2026-10-19, the day of the 1.0 acceptance
(`docs/acceptance/1.0.md`); PoC-09 ran again by hand, watching Resolve's screen, on 2026-10-26
(`docs/acceptance/1.2.2.md`). PoC-02, 05, 07 and 10 have not run.

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

**Result (2026-10-19).** Partly run; report in `docs/poc/poc-01-lifecycle.md`. With Resolve Studio
21.1.1 on Linux: "Create" from the Project Manager inside sqsolve's initialization window works
(E1) and leaves the database owned by the library role with only `plpgsql`; "Connect" to an
empty database (E0) does not initialize it and Resolve marks it incompatible; daily use works
with the role without `CREATEDB`; an idle workstation holds 1 connection, with bursts of 6 when
switching library or loading a project. Plan B became the normal path without the superuser:
`sqsolve_admin` acts as owner through membership of the library role (`07` §3). Not run: steps 3
(database owned by `sqsolve_admin`), 5 (member role), 7 (accent ordering) and 8 (Resolve 20.3.2),
and the render case of step 6.

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

**Result.** Not run. The PoCs that ran and both acceptance records used only Resolve Studio 21.1.1
with PostgreSQL 18.6. PostgreSQL 18 is the only packaged version and there is no 17 profile.

## PoC-03. Schema fingerprint

**Question.** Does the fingerprint defined in `07-libraries-and-postgresql.md` §9 change when
Resolve upgrades a library and stay stable under normal use?

**Procedure.** Create a library with 20.3.2, compute the fingerprint, use it for an hour
(projects, bins, timelines), recompute; open it with 21.1 and accept the upgrade if Resolve
offers it; recompute; open a project in 21.1; recompute.

**Success.** The fingerprint does not change under normal use and changes when the schema
changes. If upgrading does not change the schema (only project data), it is documented that the
fingerprint does not detect that case and the upgrade window becomes mandatory in the guide.

**Result (2026-10-19).** Partly run; report in `docs/poc/poc-03-fingerprint.md`. Only 21.1.1 was
available: the fingerprint stayed the same under normal use (creating, editing, saving and
deleting projects and timelines). The library was not created with 20.3.2 nor upgraded, so
whether an upgrade changes the fingerprint is not validated.

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

**Result (2026-10-19).** Partly run; report in `docs/poc/poc-04-backup-format.md`. Step 3: a backup
of `apolo` restored by sqsolve as the new library `apolo_poc` opened in Resolve 21.1.1, which
listed its projects and created one in it; the check that the original library did not receive
that project was not run. Steps 1, 2 and 4 were not run (they need Resolve's Project Manager), so
the format of Resolve's own backup file, whether Resolve restores sqsolve's files and which
compression it accepts are not known.

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

**Result.** Not run. sqsolve does not generate `.resolvedbkey` keys: the library page gives the
values to type in Resolve's Project Manager (ADR 0013, `docs/20-roadmap.md`).

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
and also connects with mandatory `hostssl` ("TLS only" mode). Not tested on macOS or Windows,
and `ssl_min_protocol_version = TLSv1.3` was not tried.

## PoC-07. Collaboration and traffic

**Questions.** What traffic does collaboration generate? Does it work with the free version of
21?

**Procedure.** Two workstations with Studio on the same library, collaboration enabled in the
project. Capture with `tcpdump` or Wireshark on both workstations and on the server. Test chat,
bin and timeline locking, refreshing collaborators' changes. Block 50059-50314 in the firewall
of one workstation and observe the symptom. Repeat with a workstation running free Resolve.

**Success.** List of ports and traffic directions; visible symptoms of each failure for the
troubleshooting guide; answer about the free version.

**Result.** Not run. No record covers two workstations on the same library (the 1.0 acceptance
left AC-04 to AC-06 undone), so collaboration, its ports and its behaviour with the free version
are not validated.

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

**Result (2026-10-19).** Partly run; report in `docs/poc/poc-08-network.md`. On the LAN, with the
port published by Docker on TrueNAS, PostgreSQL sees the workstation's real IP, for `psql` and for
Resolve 21.1.1. Through the TrueNAS Tailscale app every connection arrives with the IP of the
Tailscale container (`172.16.10.1`). Not run: the `network_mode: host` comparison, another port or
a host name in Resolve's location field, and Resolve or collaboration over the VPN (only `psql`
was tried over Tailscale).

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

**Result.** Run on 2026-10-19 through the scripting API (`docs/poc/poc-09-forced-disconnect.md`):
maintenance without closing sessions does not interrupt an open session, and after a swap that
closes the sessions Resolve 21.1.1 reconnects on its own to the restored database and keeps
saving. On 2026-10-26 the 1.2.2 acceptance saw it on screen for three replacements and a revert
(`docs/acceptance/1.2.2.md`): Resolve shows no dialog and its next save writes what it has in
memory into the restored library. The replace and revert pages ask to close the project in
Resolve first; a technical lock after a replacement is a roadmap item (`docs/20-roadmap.md`).
Steps 3 and 4 were not run: the "archived (read only)" state was not evaluated and sqsolve has
none, and what Resolve shows when maintenance, which sets the library roles `NOLOGIN` since ADR
0024, refuses a new connection is To be confirmed.

## PoC-10. Locks and `pg_dump` duration

**Question.** Does a hot backup affect editors?

**Procedure.** With a large test library (several projects with long timelines; size recorded
with `pg_database_size`), run `pg_dump` while one workstation edits and another saves
frequently. Measure duration, lock waits (`pg_locks`, `log_lock_waits = on`) and the experience
in Resolve. Repeat the test restore into an auxiliary database.

**Success.** Duration data per size for the guide; confirmation that the hot backup does not
block normal work or, if it does, in which operations.

**Result.** Not run: the duration of `pg_dump` per library size and its effect on editors are not
measured.
