# 17. Version control, CI/CD and releases

## 1. Repository

- A single repository: code, documentation, deployment, OpenAPI, tests.
- Main forge: To be confirmed (GitHub under Diego's account is what is planned). Mirror on a free
  forge (Codeberg) so as not to depend on a single platform.
- Structure:

```
cmd/sqsolve/            entry point
internal/               packages (see 21-tasks.md §1)
api/                    openapi.yaml and JSON schemas
deploy/compose.yaml     reference Compose
deploy/lab/             lab Compose for PoCs
docs/                   this documentation, manuals, compatibility, ADRs
test/                   integration, end to end, faults, fixtures
Dockerfile
Makefile
```

## 2. Workflow

- Main branch `main` always releasable. Changes on short branches with review.
- One-line commit messages in English, no trailers.
- `main` protected: green CI required.
- Signed `vX.Y.Z` tags to release.

## 3. Versioning

- SemVer. `1.0.0` is the MVP. `0.x` versions are pre-releases and may break compatibility.
- Changes that require administrator action (a new volume, a mandatory variable) only in major
  versions or, in 1.x, with a default value that keeps the previous behaviour.
- `CHANGELOG.md` in English, "Keep a Changelog" format, with the sections Added, Changed, Fixed,
  Security, Compatibility.

## 4. Continuous integration

| Job | Trigger | Content |
|---|---|---|
| `check` | Every push and PR | `gofmt -l`, `go vet`, `staticcheck`, `go test ./...` with `-race`, coverage, `govulncheck`, `gosec` |
| `integration` | Every push and PR | Integration tests with PostgreSQL 18 |
| `contract` | Every push and PR | API validation against OpenAPI |
| `e2e` | PR to `main` | `chromedp` against the full Compose |
| `image` | Every push | Build amd64 and arm64 image (not published on PRs) |
| `reproducible` | Nightly and on tags | Build twice in clean environments and compare the binary's SHA-256 |
| `faults` | Nightly | Fault and recovery tests |
| `fuzz` | Nightly | Long fuzzing |
| `zap` | Nightly | OWASP ZAP baseline |
| `release` | `v*` tag | All of the above, publish images, SBOM, signatures, notes |

Implemented today (2026-10-18) in `.github/workflows`:

- `ci.yml`:
  - `check`, with the latest patch of Go 1.26 (the minimum in `go.mod`) and of Go 1.27 (the one
    in the image): formatting, `go vet`, staticcheck, tests with `-race` including the
    integration tests against PostgreSQL 18 from the PGDG repository, and govulncheck.
  - `scenarios`: `test/scenarios.py` with real PostgreSQL and sqsolve. It simulates Resolve
    workstations over TCP and provokes failures: a workstation writing during the backup,
    restoring with Resolve open, sqsolve killed mid-job (no `pg_dump` survives and no `.partial`
    remains), PostgreSQL down and back, a backups folder without permission, trash and purge.
  - `smoke`, with users 1000 and 568 (the one used by TrueNAS apps): builds the image, brings up
    `deploy/compose.yaml` as is with temporary paths and runs `test/compose_e2e.py`
    (installation through the panel, a workstation through the published port, backup, test
    restore, restore as new, the console inside the container, `down` and `up` without losing
    data, and repeated `sqsolve-init`).
- `image.yml`: on every push to `main` and on `v*` tags, amd64 and arm64 image at
  `ghcr.io/donutinit/sqsolve` with buildx provenance and SBOM. Tags `edge` (main), `X.Y.Z`,
  `X.Y` and `latest` (releases) and `sha-<commit>`. The reference compose uses `X.Y` so that
  every fix arrives as "Update available" in TrueNAS.

staticcheck runs for now only with Go 1.26: v0.8.1 does not read the Go 1.27.2 format and
v0.8.2, which does, was published on 2026-10-12 (it can be upgraded from 2026-10-19 under the
7-day rule).

Pending from the table: OpenAPI contract, `e2e` with a browser, `reproducible`, `faults`, `fuzz`,
`zap`, cosign signatures and `release`.

Actions used, all pinned by commit SHA: `actions/checkout`, `actions/setup-go` and the Docker
ones (`setup-buildx`, `login`, `metadata`, `build-push`). Everything CI does can also be done
locally with `make`.

## 5. Build

- Binary: `CGO_ENABLED=0 go build -trimpath -buildvcs=true -ldflags "-s -w -X
  main.version=$(VERSION)"`, with `SOURCE_DATE_EPOCH` equal to the commit date.
- Image: multi-stage Dockerfile; build stage `golang:<version>-trixie@<digest>`; final stage
  `postgres:18.x-trixie@<digest>` plus the binary, unprivileged user by default,
  `ENTRYPOINT ["sqsolve"]`. OCI labels (`org.opencontainers.image.source`, `.revision`,
  `.version`, `.licenses=AGPL-3.0-only`).
- `go mod vendor` allows building without network access; `make vendor-check` checks that
  `vendor/` matches.

## 6. Releases

- Registries: GHCR (To be confirmed) and a mirror (Quay or the free forge). Tags `X.Y.Z`, `X.Y`,
  `X`; `latest` only points to the latest stable release, and the documentation never recommends
  using it.
- Keyless `cosign` signatures (OIDC identity of the `image.yml` workflow in this repository),
  plus the provenance attestation and the SBOM that buildx generates. Verification:
  `cosign verify ghcr.io/donutinit/sqsolve:X.Y.Z --certificate-identity-regexp
  'https://github.com/donutinit/sqsolve/.github/workflows/image.yml@refs/tags/v'
  --certificate-oidc-issuer https://token.actions.githubusercontent.com`, with cosign 3 or newer
  (the signature is stored in the OCI bundle format; cosign 2 does not find it). Checked with
  `0.1.6` on 2026-10-19. The project's own key and the registry mirror move to 1.1 (cut of
  2026-10-19).
- SBOM in SPDX and CycloneDX generated with `syft`, attached to the release and to the image.
- Release assets on GitHub (`release.yml`, on every `v*` tag): the `compose.yaml` of that version
  and the notes taken from the corresponding section of `CHANGELOG.md`. Standalone binaries and
  signed checksums move to 1.1.

## 7. Dependencies

- Go: only those in `06-stack.md`. Updates grouped once a month, with at least 7 days since
  their publication (except security fixes), reviewing the `go.sum` diff.
- Base images: pinned digest; a weekly job reports new digests of `postgres:18.x-trixie`; the
  update goes through the full test battery.
- UI without third-party resources (ADR 0004).

## 8. Release checklist

1. Full CI green, including the nightly jobs of the last week.
2. Compatibility matrix updated (`14-compatibility.md`).
3. Acceptance script (on major and minor versions) or smoke (on patches).
4. Release notes with upgrade and rollback steps.
5. Signed tag; automatic publication; manual verification of signatures and SBOM.
6. Update `deploy/compose.yaml` on `main` with the new digests.
