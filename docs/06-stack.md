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

Version: `go.mod` declares `go 1.26.0` as the minimum (the system version on lizeth). CI runs
the checks with the latest patch of Go 1.26 and of Go 1.27, and the image is built with
`golang:1.27-trixie` pinned by digest.

## 2. UI: **server-side templates** (no htmx, see ADR 0004)

| Option | Pros | Cons |
|---|---|---|
| **`html/template` + own `app.js` + own CSS** | No Node toolchain; a single binary with embedded assets; automatic escaping against XSS; live progress with SSE; little JavaScript to audit | Very rich interactions cost more; untyped templates |
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
| Encrypted offsite copy (not implemented) | **restic** (BSD-2, single binary, encryption and deduplication by default, `restic check`); not in the image | rclone (no encryption by default or repository verification); Borg (requires a Borg server over SSH); Kopia (valid, less widespread on NAS) |
| Point-in-time recovery (not implemented) | **pgBackRest** (MIT) | WAL-G (valid; pgBackRest has better verification and documentation); Barman (Python, meant for dedicated servers) |
| Encryption of individual files (not implemented) | **age** (`filippo.io/age`, BSD-3); not a dependency | GPG (complex to operate) |

Only the first row exists in 1.2.2. The other three are not implemented; restic and pgBackRest
are the tools chosen for directions in `20-roadmap.md`. None of the three is in the image or in
`go.mod`.

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
- TOTP: own implementation of RFC 6238 (`internal/auth/totp.go`, 75 lines, tested with the RFC
  vectors).
- OIDC and WebAuthn are not implemented (`20-roadmap.md`). The candidates, if they are, are
  `github.com/coreos/go-oidc/v3` (Apache-2.0) with `golang.org/x/oauth2` and
  `github.com/go-webauthn/webauthn` (BSD-3); each one would need its ADR.

## 9. Closed list of Go dependencies

| Module | License | Use |
|---|---|---|
| `github.com/jackc/pgx/v5` | MIT | PostgreSQL client, `Identifier.Sanitize` |
| `modernc.org/sqlite` | BSD-3 | SQLite without CGO |
| `golang.org/x/crypto` | BSD-3 | Argon2id, XChaCha20-Poly1305 |
| `golang.org/x/sys` | BSD-3 | Only its `unix` package, to turn off terminal echo when the console asks for a password (`cmd/sqsolve/main.go`) |
| `github.com/adhocore/gronx` | MIT | Cron expressions |
| `github.com/klauspost/compress` | BSD-3 | Only its `zstd` package, for the experimental project and grade export (ADR 0019) |

The closed list has six modules since 2026-10-20, the same six in 1.2.2. This was a correction
of the list, not a new dependency: `golang.org/x/sys` and `github.com/klauspost/compress` were already imported by
the code (the first was missing from this table, the second was marked `// indirect` in `go.mod`
although `internal/drp/blobs.go` imports it directly). `go.mod` now lists both as direct
requirements, and CI fails if it differs from what `go mod tidy` writes (`make tidy-check`).

Tests add no module: they use the standard library, throwaway PostgreSQL clusters started by
`internal/testpg` from local binaries, and Python scripts in `test/`. `testcontainers-go`,
`chromedp` and `kin-openapi` were considered for container, browser and OpenAPI tests and are not
dependencies: there is no browser test and no `api/openapi.yaml`.

No vendored third-party assets: styles, scripts and the SVG brand are our own
(`internal/web/static`).

Any new module requires an ADR, a version published at least 7 days ago, a license review and a
review of the transitive dependency tree (`go mod graph`).

## 10. Image and packaging

- The sqsolve image is `FROM postgres:18.x-trixie@sha256:...` (`postgres:18.6-trixie` in 1.2.2,
  ADR 0016) to guarantee that `pg_dump` and `pg_restore` have the same major version as the
  server and to share layers with the PostgreSQL container.
- The binary is built in a `golang:<version>-trixie` stage pinned by digest, with
  `CGO_ENABLED=0 -trimpath -buildvcs=false` and the version and commit injected through
  `-ldflags`: the build context has no `.git`, so there is nothing to stamp. `-w` drops the DWARF
  data but the symbol table stays, because `govulncheck -mode=binary` needs it to scan the binary
  that ships (it reports false positives on one stripped with `-s`). Two builds of the same
  commit with the same toolchain gave the same binary in one check by hand on one machine
  (2026-10-20, Go 1.27.2, linux/amd64: identical SHA-256 with and without `go build -a`). Two
  builds in clean environments have not been compared, so RNF-MAN-03 has no evidence. The
  layers of the image are not reproducible: their timestamps are those of the build.
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
| Docker Buildx | Multi-architecture images and their SBOM and provenance attestations (`sbom: true`, `provenance: true`) |
| `cosign` | Keyless signing of the images published for `v*` tags |
| Dependabot | Weekly pull requests for the base images and the GitHub Actions, with a 7-day cooldown |
| `python3` | Scripts in `test/`: failure scenarios (`make scenarios`), the error code check (`make errors-check`) and the image walkthrough of the CI smoke job |
| `make fuzz` | Every Go fuzz target: 10 s each in CI, 5 minutes each nightly (`fuzz.yml`) |

`gosec`, `syft` and OWASP ZAP are not part of the build or CI: the SBOM is the one Buildx
attaches to the image, and an OWASP ZAP baseline was run by hand once for 1.0 (`08-security.md`, "Security review of
1.0").

The forge is GitHub (`github.com/donutinit/sqsolve`) and CI is GitHub Actions: `ci.yml` (checks,
integration tests against PostgreSQL 18, failure scenarios and the image smoke test, on every
push to `main` and every pull request), `image.yml` (runs `ci.yml` and then publishes the
multi-architecture image to `ghcr.io/donutinit/sqsolve`, signed for `v*` tags), `release.yml`
(the GitHub release with the compose file pinned to the version) and `fuzz.yml` (nightly
fuzzing). The workflows have not been run on any other forge.
