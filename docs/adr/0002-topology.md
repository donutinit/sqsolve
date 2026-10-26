# ADR 0002. Deployment topology

**Status:** Accepted (2026-10-18). As of 1.2.2 there is no `sqsolve-hooks` container (ADR 0011,
`docs/20-roadmap.md`).

**Context.** It must install by pasting a YAML into any system with Docker Compose, run without
intervention and separate responsibilities without unnecessary complexity. Diego explicitly
asked not to design for any particular NAS.

**Decision.** One Compose project with `postgres` (official image), `sqsolve` (panel, API, jobs)
and `sqsolve-init` (a single run for secrets, configuration and permissions). In 1.1 an optional
`sqsolve-hooks` is added per profile. sqsolve talks to PostgreSQL over a Unix socket in a shared
volume.

**Alternatives.**
- A single container with PostgreSQL and sqsolve under a supervisor (like Studio Server 1): easier
  to copy on platforms without Compose, but it mixes lifecycles, complicates PostgreSQL upgrades
  and forces a supervisor.
- Kubernetes or Helm: disproportionate for the audience.
- Native packages (deb, rpm): out of scope for 1.x.

**Consequences.** Three services in the YAML. Platforms that do not accept Compose are left out
(an "all in one" mode is reconsidered if there is real demand). sqsolve cannot restart PostgreSQL
(it has no Docker socket): changes that require a restart are flagged in the UI.
