# 06. Technology stack

Criteria, in this order: risk of data loss, operational simplicity, supply chain surface,
5-year maintainability, sufficient performance, community.

## 1. Backend language: **Go**

| Option | Pros | Cons |
|---|---|---|
| **Go** | Single static binary; minimal startup and memory; excellent handling of processes, contexts and cancellation; trivial cross-compilation to arm64; standard library sufficient for HTTP, templates, TLS, cryptography and logs; mature PostgreSQL ecosystem (pgx) | Less expressive for rich domain models; verbose error handling |
| Python (FastAPI) | Fast to write; many potential contributors | Interpreter and dependencies in the image; more fragile packaging and reproducibility; more memory |
| TypeScript (Node) | Same language in UI and server | Wide and risky npm supply chain; the project wants to avoid Node in the build |
| Rust | Memory safety, single binary | Slower development; fewer people able to maintain it; the problem does not demand that performance |

Version: `go.mod` declares `go 1.26` as the minimum (the system version on lizeth). CI builds
with the latest patch version of the current stable branch (1.27 as of 2026-10-18).

## 2. UI: **server-side templates** (no htmx, see ADR 0004)

| Option | Pros | Cons |
|---|---|---|
| **`html/template` + vendored htmx + own CSS** | No Node toolchain; a single binary with embedded assets; automatic escaping against XSS; live progress with SSE; little JavaScript to audit | Very rich interactions cost more; untyped templates |
| SPA (Svelte, React, Vue) | Rich interaction; clean API separation | npm toolchain, hundreds of packages, two applications to maintain |
| `templ` (typed templates for Go) | Typing | Extra generation tool; not needed for the size of this UI |

The UI does not consume the REST API: both call the same services. This way the API is not
distorted for the UI and the UI does not pay extra latency.

Allowed JavaScript: one small file of our own (`app.js`, under 300 lines) for copying to the
clipboard, typed confirmations and live progress. No `eval`, no inline scripts. htmx was
discarded during implementation: forms with POST, redirect and GET are enough. The theme follows
the system.

## 3. Control plane state: **SQLite**

| Option | Pros | Cons |
|---|---|---|
| **SQLite (modernc.org/sqlite, pure Go)** | Independent of PostgreSQL; a single file; transactions; hot copies with `VACUUM INTO`; no server | Only one writer at a time (enough here) |
| `sqsolve` database inside the same PostgreSQL | A single engine | If PostgreSQL fails or is restored, the panel loses its state exactly when it is needed; mixes control plane and data plane |
| JSON files | Simple | No transactions or queries; corruption on power loss |

`modernc.org/sqlite` avoids CGO and keeps the build static and cross-compilable.

## 4. Library database: **PostgreSQL 18, official image**

| Option | Pros | Cons |
|---|---|---|
| **`postgres:18.x-trixie` from Docker Official Images, pinned by digest** | Maintained by the Docker and PostgreSQL community; fast security updates; the one NAS catalogs use | Configuration has to be passed through files and `-c` |
| PostgreSQL 13 as Blackmagic recommended for 18 to 20 | Matches old installations | Unsupported since 2025-11-13 [01 §12] |
| Packaged third-party images | Convenient configuration | Add an intermediate vendor with its own distribution policy |
| PostgreSQL built by us | Full control | Own security maintenance without need |

Details and per-version alternatives in ADR 0006.

## 5. Backups

| Need | Chosen | Alternatives evaluated |
|---|---|---|
| Per-library backup, restorable in Resolve | `pg_dump` custom format orchestrated by sqsolve | `prodrigestivill/postgres-backup-local` (no verification, catalog or safe restore); cron scripts (no state or UI) |
| Encrypted offsite copy (1.1) | **restic** (BSD-2, single binary, encryption and deduplication by default, `restic check`) | rclone (no encryption by default or repository verification); Borg (requires a Borg server over SSH); Kopia (valid, less widespread on NAS) |
| Point-in-time recovery (2.0) | **pgBackRest** (MIT) | WAL-G (valid; pgBackRest has better verification and documentation); Barman (Python, meant for dedicated servers) |
| Encryption of individual files (1.1) | **age** (`filippo.io/age`, BSD-3) | GPG (complex to operate) |

## 6. Job scheduling

| Option | Pros | Cons |
|---|---|---|
| **In-process scheduler + SQLite queue** | State visible in the UI; controlled missed runs; no other service | Has to be written well (bounded and tested scope) |
| cron inside the container (like Studio Server with `/etc/cron.d`) | Familiar | Another supervised process; invisible state; hard to test |
| Host scheduler (NAS cron, systemd timers) | Already exists | Configuration outside the app; not portable |

Cron expressions: `github.com/adhocore/gronx` (MIT) to validate and compute the next run with
time zone. Fallback if it is abandoned: `github.com/robfig/cron/v3` (MIT), only its parser.

## 7. Notifications

Own implementation of three channels (webhook with HMAC, SMTP with `net/smtp` and STARTTLS or
implicit TLS, ntfy over HTTP). Apprise was discarded because it would put Python in the image for
three simple protocols.

## 8. Authentication

- Passwords: Argon2id from `golang.org/x/crypto/argon2`.
- TOTP: own implementation of RFC 6238 (about 60 lines, tested with the RFC vectors).
- OIDC (2.0): `github.com/coreos/go-oidc/v3` (Apache-2.0) with `golang.org/x/oauth2`.
- WebAuthn (2.0): `github.com/go-webauthn/webauthn` (BSD-3), to be evaluated in its ADR.

## 9. Closed list of Go dependencies for 1.0

| Module | License | Use |
|---|---|---|
| `github.com/jackc/pgx/v5` | MIT | PostgreSQL client, `Identifier.Sanitize` |
| `modernc.org/sqlite` | BSD-3 | SQLite without CGO |
| `golang.org/x/crypto` | BSD-3 | Argon2id, XChaCha20-Poly1305 |
| `golang.org/x/sys` | BSD-3 | Only its `unix` package, to turn off terminal echo when the console asks for a password (`cmd/sqsolve/main.go`) |
| `github.com/adhocore/gronx` | MIT | Cron expressions |
| `github.com/klauspost/compress` | BSD-3 | Only its `zstd` package, for the experimental project and grade export (ADR 0019) |

The closed list for 1.0 has six modules since 2026-10-20. This is a correction of the list, not a
new dependency: `golang.org/x/sys` and `github.com/klauspost/compress` were already imported by
the code (the first was missing from this table, the second was marked `// indirect` in `go.mod`
although `internal/drp/blobs.go` imports it directly). `go.mod` now lists both as direct
requirements, and CI fails if it differs from what `go mod tidy` writes (`make tidy-check`).

Tests only: `github.com/testcontainers/testcontainers-go` (MIT), `github.com/chromedp/chromedp`
(MIT), `github.com/getkin/kin-openapi` (MIT).

No vendored third-party assets: styles, scripts and the SVG brand are our own
(`internal/web/static`).

Any new module requires an ADR, a version published at least 7 days ago, a license review and a
review of the transitive dependency tree (`go mod graph`).

## 10. Image and packaging

- The sqsolve image is `FROM postgres:18.x-trixie@sha256:...` (ADR 0016) to guarantee that
  `pg_dump` and `pg_restore` have the same major version as the server and to share layers with
  the PostgreSQL container.
- The binary is built in a `golang:<version>-trixie` stage pinned by digest, with
  `CGO_ENABLED=0 -trimpath -buildvcs=false` and the version and commit injected through
  `-ldflags`: the build context has no `.git`, so there is nothing to stamp. `-w` drops the DWARF
  data but the symbol table stays, because `govulncheck -mode=binary` needs it to scan the binary
  that ships (it reports false positives on one stripped with `-s`). Two builds of the same
  commit with the same toolchain give the same binary (checked on 2026-10-20 with Go 1.27.2 for
  linux/amd64: identical SHA-256 with and without `go build -a`). The layers of the image are not
  reproducible: their timestamps are those of the build.
- The two base images are written literally in their `FROM` lines so that Dependabot can update
  them. The PostgreSQL reference (tag and digest) must be the same in the `Dockerfile` and in
  `deploy/compose.yaml`; `make pg-image-check` verifies it in CI.
- Deployment: Docker Compose v2. No Helm, no Kubernetes, no NAS-specific catalogs.

## 11. Development tools and CI

| Tool | Use |
|---|---|
| `make` | Single entry point: `make check`, `make test-integration`, `make image` |
| `gofmt`, `go vet`, `staticcheck` | Quality |
| `govulncheck` | Known vulnerabilities in dependencies and the standard library |
| `gosec` | Static security analysis (planned; not run in CI yet) |
| Docker Buildx | Multi-architecture images and their SBOM and provenance attestations (`sbom: true`, `provenance: true`) |
| `syft` | SBOM in SPDX and CycloneDX (planned; today the SBOM is the one Buildx attaches to the image) |
| `cosign` | Keyless signing of the images published for `v*` tags |
| Dependabot | Weekly pull requests for the base images and the GitHub Actions, with a 7-day cooldown |
| OWASP ZAP (Docker image) | UI scan in CI (planned) |

CI is written for GitHub Actions with syntax compatible with Forgejo Actions, so the integration
can be moved or duplicated to a free forge without rewriting it. The reusable workflow
(`workflow_call`, which `image.yml` uses to run `ci.yml`) and Dependabot were added on 2026-10-20
and have not been checked on Forgejo: To be confirmed.
