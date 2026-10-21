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
| `check` | Every push and PR | `gofmt -l`, `go vet`, `staticcheck`, `go mod tidy -diff`, one-message-per-code check, `go test ./...` with `-race`, coverage, `govulncheck`, `gosec` |
| `integration` | Every push and PR | Integration tests with PostgreSQL 18 |
| `contract` | Every push and PR | API validation against OpenAPI |
| `e2e` | PR to `main` | `chromedp` against the full Compose |
| `image` | Every push | Build amd64 and arm64 image (not published on PRs) |
| `reproducible` | Nightly and on tags | Build twice in clean environments and compare the binary's SHA-256 |
| `faults` | Nightly | Fault and recovery tests |
| `fuzz` | Nightly | Long fuzzing |
| `zap` | Nightly | OWASP ZAP baseline |
| `release` | `v*` tag | All of the above, publish images, SBOM, signatures, notes |

Implemented today (2026-10-20) in `.github/workflows`:

- `ci.yml` (push to `main`, pull requests, and `workflow_call` from `image.yml`):
  - `check`, with the latest patch of Go 1.26 (the minimum in `go.mod`) and of Go 1.27 (the one
    in the image): formatting, `go vet`, staticcheck v0.8.2 on both versions, `go mod tidy -diff`
    (`make tidy-check`), `make errors-check` (an `SQS-` code with two different messages fails;
    the codes that already had several wordings are the baseline in
    `test/errors-multi-message.txt` and must not grow), `make pg-image-check` (the PostgreSQL tag
    and digest of the `Dockerfile` and of `deploy/compose.yaml` are the same), tests with `-race`
    including the integration tests against PostgreSQL 18 from the PGDG repository, and
    govulncheck.
  - `scenarios`: `test/scenarios.py` with real PostgreSQL and sqsolve. It simulates Resolve
    workstations over TCP and provokes failures: a workstation writing during the backup,
    restoring with Resolve open, sqsolve killed mid-job (no `pg_dump` survives and no `.partial`
    remains), PostgreSQL down and back, a backups folder without permission, trash and purge.
  - `smoke`, with users 1000 and 568 (the one used by TrueNAS apps): builds the image, runs
    `govulncheck -mode=binary` (pinned version) on `/usr/local/bin/sqsolve` extracted from it
    (once, with user 1000), brings up `deploy/compose.yaml` as is with temporary paths and runs
    `test/compose_e2e.py` (installation through the panel, a workstation through the published
    port, backup, test restore, restore as new, the console inside the container, `down` and `up`
    without losing data, and repeated `sqsolve-init`).
- `image.yml`: on every push to `main`, on `v*` tags and by hand. Its first job (`ci`) runs the
  whole `ci.yml`; `publish` needs it, so no image reaches the registry (`edge`, `X.Y.Z`, `X.Y`,
  `latest`) unless the same checks that gate a pull request pass on that commit. `publish` builds
  the amd64 and arm64 image at `ghcr.io/donutinit/sqsolve` with buildx provenance and SBOM, tags
  `edge` (main), `X.Y.Z`, `X.Y` and `latest` (releases) and `sha-<commit>`, and signs the digest
  with cosign on `v*` tags (the signing identity is the `publish` job of `image.yml`, so the
  `cosign verify` identity regexp in section 6 stays valid). The reference compose uses `X.Y` so
  that every fix arrives as "Update available" in TrueNAS. On a push to `main` CI therefore runs
  twice, once as `ci.yml` (it feeds the badge in the README) and once inside `image.yml` (it
  gates publication). Its last job, `release`, runs only for `v*` tags and only after `publish`
  succeeded (`needs: publish`); it is the only job with `contents: write`.
- `release.yml` (reusable, `workflow_call` only; `image.yml` calls it): publishes the GitHub
  release with the notes from `CHANGELOG.md` and the compose file of that version, with the image
  pinned to the exact `X.Y.Z` (the file in the repository uses the moving `X.Y`). Because it runs
  after the image is pushed and signed, a public release never pins an image that does not exist;
  if CI or the image job fails there is no release, and the tag can be deleted and made again.
- `dependabot.yml`: weekly pull requests, each waiting 7 days after publication of the new
  version (`cooldown`), for the `docker` ecosystem (`Dockerfile`: the Go builder and PostgreSQL),
  `docker-compose` (`deploy/compose.yaml`: PostgreSQL) and `github-actions`. A new PostgreSQL
  major version and our own image are ignored. Because the PostgreSQL image is in two files and
  Dependabot opens one pull request per file, whoever merges it applies the same tag and digest to
  the other file in that pull request; `make pg-image-check` fails until both match. The pull
  request already runs the full CI, including the smoke test with the new PostgreSQL. Go modules
  are not covered: they follow section 7.

Pending from the table: OpenAPI contract, `e2e` with a browser, `reproducible` (two builds
compared in CI; checked by hand on 2026-10-20, see `06-stack.md` section 10), `faults` (partly
covered by `scenarios`), `fuzz`, `zap` and `gosec`.

Actions used, all pinned by commit SHA: `actions/checkout`, `actions/setup-go`, the Docker ones
(`setup-buildx`, `login`, `metadata`, `build-push`), `sigstore/cosign-installer` and
`softprops/action-gh-release`; Dependabot keeps the SHAs current. Everything CI does can also be
done locally with `make` (`make check`, `make tidy-check`, `make pg-image-check`, `make
vuln-binary BINARY=...`).

## 5. Build

- Binary: `CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-w -X main.version=...
  -X main.commit=..."`. The image build context has no `.git`, so there is no VCS stamp and the
  version and commit come from build arguments; `SOURCE_DATE_EPOCH` is not used (Go does not read
  it). With the same toolchain image and the same inputs the binary is identical (`06-stack.md`
  section 10); the layers of the image carry the build time and are not reproducible. `-s` is not
  used so that `govulncheck -mode=binary` can scan the shipped binary.
- Image: multi-stage Dockerfile; build stage `golang:<version>-trixie@<digest>`; final stage
  `postgres:18.x-trixie@<digest>` plus the binary, unprivileged user by default,
  `ENTRYPOINT ["/usr/local/bin/sqsolve"]`. OCI labels (`org.opencontainers.image.source`,
  `.revision`, `.version`, `.licenses=AGPL-3.0-only`). Both `FROM` lines are literal (no `ARG`) so
  that Dependabot can update them.
- There is no `vendor/` directory: modules are downloaded and checked against `go.sum`, and CI
  fails if `go.mod` or `go.sum` differ from what `go mod tidy` writes.

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
- SBOM: the one buildx generates (`sbom: true`), attached to the image as an attestation.
  `syft`, SPDX and CycloneDX files as release assets are not produced yet.
- Nothing is published unless `ci.yml` passes on the same commit (`image.yml` runs it first). The
  user guide ("Updating") tells operators how to verify the signature before installing.
- Release assets on GitHub (`release.yml`, called by `image.yml` after the image is published and
  signed): the `compose.yaml` of that version, with the image pinned to
  `ghcr.io/donutinit/sqsolve:X.Y.Z`, and the notes taken from the corresponding section of
  `CHANGELOG.md`. The workflow fails if the tag is not `vX.Y.Z` or if the asset would keep a moving
  tag. Standalone binaries and signed checksums move to 1.1.

## 7. Dependencies

- Go: only those in `06-stack.md`. Updates grouped once a month, with at least 7 days since
  their publication (except security fixes), reviewing the `go.sum` diff.
- Base images: pinned digest. Dependabot (`.github/dependabot.yml`) opens a weekly pull request
  when `postgres:18.x-trixie`, the Go builder image or a GitHub Action has a new version, 7 days
  after it was published; the pull request goes through the full CI. The PostgreSQL pin lives in
  two files that must stay equal (`make pg-image-check`).
- The PostgreSQL pin in `deploy/compose.yaml` is the operator's copy: a new sqsolve release does
  not change a running installation. A release that moves to a new PostgreSQL minor says so in its
  notes, and the operator pastes the new compose file or replaces the `postgres` image line
  (user guide, "Updating").
- UI without third-party resources (ADR 0004).

## 8. Release checklist

1. Full CI green, including the nightly jobs of the last week.
2. Compatibility matrix updated (`14-compatibility.md`).
3. Acceptance script (on major and minor versions) or smoke (on patches).
4. Release notes with upgrade and rollback steps. If the PostgreSQL pin changed since the
   previous release, say so and give the new `image:` line, because installed copies of the
   compose file keep the old digest.
5. Signed tag; automatic publication; manual verification of signatures and SBOM.
6. `deploy/compose.yaml` and the `Dockerfile` on `main` carry the same, current PostgreSQL tag and
   digest (Dependabot proposes them; `make pg-image-check` enforces that they match).
