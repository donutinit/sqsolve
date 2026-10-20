# ADR 0008. PostgreSQL role model

**Status:** Accepted, pending PoC-01.

**Context.** Blackmagic's convention uses the `postgres` superuser with password `DaVinci` for
everything [01 §5]. A superuser exposed on the network gives control of the server and, through
`COPY ... PROGRAM`, of the container.

**Decision.**
- `postgres` and `sqsolve_admin` only over the local socket; rejected over the network in
  `pg_hba.conf`.
- `sqsolve_admin` without superuser, with `CREATEDB`, `CREATEROLE`, `pg_monitor`,
  `pg_signal_backend` and `pg_read_all_data`.
- One owner role per library, with the same name as the database, that can only connect to it.
  `CREATEDB` only during the initialization window.
- In 1.1, per-person roles that are members of the owner with `SET role` by default.

**Alternatives.** Shared superuser (insecure); one role per workstation without a common owner
(objects would end up spread across owners).

**Consequences.** Requires PostgreSQL 16+ (`CREATEROLE` semantics). If PoC-01 shows that Resolve
needs to own the database, the owner will be the library role and the swap renames will use the
superuser over the socket, audited.
