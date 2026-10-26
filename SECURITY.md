# Security

sqsolve is designed for a studio's local network or a VPN; the panel (9432) and PostgreSQL (5432)
must never be exposed to the Internet (ADR 0015). With that model, the panel asks for a normal
username and password, with no reauthentication or extra friction (ADR 0021); what protects the
projects is the network, the verified backups and the audit log.

## Reporting a vulnerability

Use GitHub's private reporting: in the repository, the **Security** tab, "Report a
vulnerability". Do not open a public issue with the details.

What to include: sqsolve version (panel footer or `sqsolve version`), how to reproduce it and
what impact it has. If you can, attach the diagnostic bundle from Settings, Maintenance, which
contains no secrets.

Commitment: response within 7 days; fix in a patch version of the current series and a notice in
the release notes. Coordinated disclosure at 90 days, or earlier if the fix is already
published.

## Scope

In scope: the `sqsolve` binary, the image `ghcr.io/donutinit/sqsolve`, `deploy/compose.yaml`, the
panel, the API and the console.

Out of scope: DaVinci Resolve, PostgreSQL and Docker themselves (report them to their projects),
and configurations that expose the ports to the Internet against the documentation.

## What sqsolve does on its own

- Passwords with Argon2id (at most 4 checked at once); attempt limits per IP and per account;
  after ten consecutive wrong passwords a 15-minute lock that refuses password logins only (open
  sessions and API tokens keep working), lifted sooner with `sqsolve admin unlock` from the
  console (ADR 0025).
- Optional TOTP; API tokens with scopes; CSRF; strict headers (CSP without external resources).
- Panel always over HTTPS. PostgreSQL offers TLS to Resolve; "TLS only" mode requires it.
- Library passwords encrypted with the master key; secrets never in logs; secret files refused at
  start when their mode lets others read them (`SQS-C010`, and `SQS-C085` in `sqsolve-init`).
- Hash-chained audit log of every action, exportable. A sensitive action, or a password, backup
  or kit download, is not done when its entry cannot be written (ADR 0026).
- The `sqsolve` container runs unprivileged, with a read-only file system and no Linux
  capabilities; `sqsolve-init` runs as root without network, with a read-only file system and
  only the capabilities that change owners and modes (`CHOWN`, `DAC_OVERRIDE`, `FOWNER`,
  `FSETID`), then exits.

The full threat model is in [`docs/08-security.md`](docs/08-security.md).
