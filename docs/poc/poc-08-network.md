# PoC-08. Network: source IP in Docker, port and VPN

Date: 2026-10-19. Server: mininas, TrueNAS 25.10.7, sqsolve 0.1.5 as a Custom App (Docker,
published ports `5432:5432` and `9432:9432`), PostgreSQL 18.6. Client: Diego's workstation
(Linux, 192.168.50.99) with `psql` 18 and DaVinci Resolve Studio 21.1.1. VPN: TrueNAS's
Tailscale app on the same NAS (100.92.52.75).

## Procedure and literal results

Query on each connection, as the user of the `apolo` library:

```sql
SELECT inet_client_addr(), client_addr, ssl, version
FROM pg_stat_activity a LEFT JOIN pg_stat_ssl s USING (pid) WHERE a.pid = pg_backend_pid();
```

| From | `client_addr` seen by PostgreSQL | TLS |
|---|---|---|
| LAN, `psql -h 192.168.50.172` | `192.168.50.99` (the workstation's real IP) | `t`, TLSv1.3 |
| LAN, Resolve 21.1.1 (session `Resolve_0007433F5FC0`) | `192.168.50.99` | `t`, TLSv1.3 |
| Tailscale, `psql -h 100.92.52.75` | `172.16.10.1` | `t`, TLSv1.3 |
| LAN, `psql ... sslmode=disable` | `192.168.50.99` | `f` (accepted: `host`, not `hostssl`) |

## Decisions it enables

- **The workstation's real IP is visible on the LAN** with Docker on TrueNAS: the per-library
  network rules (`pg_hba.conf`) work with the LAN subnets as they are.
- **Over Tailscale, the TrueNAS app translates the source address**: every connection that enters
  through the VPN arrives as `172.16.10.1`, the IP of the Tailscale container. A network rule for
  "collaborators over VPN" has to allow that address (or its network), not the `100.x`
  addresses; and one collaborator cannot be told apart from another by IP through that app. The
  README documents it under "Network rules".
- Port 5432 works published with no further configuration; collaboration port 50059 does not go
  through sqsolve (ADR 0012) and is left for PoC-07.
- With today's `host` lines, a cleartext connection gets in. Moving to `hostssl` is the "TLS
  only" mode (PoC-06).

## Status

VERIFIED for LAN and for TrueNAS's Tailscale app. Other VPNs (WireGuard on the host, Headscale)
untested: the IP seen depends on whether they do NAT. NOT DONE: the `network_mode: host`
comparison, another port or a host name in Resolve's location field, and Resolve over the VPN;
collaboration is left for PoC-07, which has not run.
