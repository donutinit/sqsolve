# ADR 0015. Remote access

**Status:** Accepted (2026-10-18).

**Context.** PostgreSQL exposed to the Internet is a constant target and Resolve does not verify
certificates (to be confirmed in PoC-06).

**Decision.** The only documented remote access is over VPN (WireGuard or Headscale). sqsolve
warns if the advertised address is a public IP. No VPN service is bundled or depended on.

**Alternatives.** Publishing 5432 with TLS and network rules (fragile); commercial providers'
tunnels (proprietary dependency).

**Consequences.** Collaboration over VPN depends on workstations seeing each other; PoC-08
documents what works.
