# PoC-06. TLS and SCRAM from Resolve

Date: 2026-10-19. Server: mininas, sqsolve 0.1.5, PostgreSQL 18.6 with the self-signed
certificate from `sqsolve-init`, `ssl = on`, `ssl_min_protocol_version = TLSv1.2`,
`password_encryption = scram-sha-256`. Workstation: Linux, DaVinci Resolve Studio 21.1.1.10, the
`apolo` library added in the Project Manager with IP, user and password, without any TLS option
(Resolve does not expose one).

## Procedure and literal results

1. With `pg_hba.conf` on `host` lines (opportunistic TLS), Resolve session already open:

   ```
   apolo|192.168.50.99|Resolve_0007433F5FC0|t|TLSv1.3   (encrypted TLS_AES_256_GCM_SHA384)
   ```

   Resolve negotiates TLS on its own (libpq `sslmode=prefer`) and accepts the self-signed
   certificate without verifying it.

2. "TLS only" mode enabled in Settings (`hostssl` lines):

   | Client | Result |
   |---|---|
   | `psql ... sslmode=disable` | `FATAL: pg_hba.conf rejects connection for host "192.168.50.99", user "apolo", database "apolo", no encryption` |
   | `psql` by default | connects, `ssl = t`, TLSv1.3 |
   | Resolve, new connection forced with the scripting API (`SetCurrentDatabase` to another database and back to `apolo`) | `True`; lists the projects; in `pg_stat_ssl` its six connections show `ssl = t`, TLSv1.3 |

3. "TLS only" mode disabled: `psql ... sslmode=disable` connects in cleartext again.

SCRAM: all the connections above authenticated with `scram-sha-256`; Resolve needed nothing
special.

## Decisions it enables

- **TLS can be mandatory** with Resolve 21.1.1 on Linux: "TLS only" mode is safe to enable in
  that combination. It stays off by default and with a warning because it was not tested with
  Resolve on macOS or Windows or with earlier versions; the "Encrypted" column of the sessions
  lets you check it before enabling it in each installation.
- Resolve does not verify the certificate: encryption protects against passive eavesdropping, not
  against server impersonation. The recommendation to use a VPN outside the LAN (ADR 0015)
  stands.
- `ssl_min_protocol_version = TLSv1.3` was not tested; with 21.1.1 negotiating 1.3 it is
  feasible, but it waits until there is evidence from the other platforms.

## Status

VERIFIED for Resolve 21.1.1 on Linux. REPORTED for macOS and Windows (same libpq, untested).
