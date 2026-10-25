# ADR 0027. The reference compose follows the major version tag

**Status:** Accepted (2026-10-25), Diego's decision. Replaces the `X.Y` tag of the reference
compose described in `17-ci-cd-and-releases.md`.

**Context.** The reference compose used the moving `X.Y` tag (`1.2`). TrueNAS shows "Update
available" for a custom app only when the digest behind the same tag changes in the registry:
once a day its middleware (25.10, `app.image.op.check_update`) compares each local image tag
with the registry, and the Update button runs `docker compose pull` and recreates the app. A new
minor version publishes a new tag (`1.3`) and never moves `1.2`, so an installation stayed on its
series without any notice: mininas still ran `1.1` after 1.2.0 and 1.2.1 were released.

**Decision.** `image.yml` also publishes the major version tag `X`
(`type=semver,pattern={{major}}`) and the reference compose uses `ghcr.io/donutinit/sqsolve:1`.
The compose attached to each GitHub release still pins `X.Y.Z`. A prerelease moves neither `X.Y`
nor `X`. `X` only changes with a new major version, which for sqsolve means a new PostgreSQL major
version and a logical migration (ADR 0017), so that never arrives through the Update button.

**Consequences.**

- Every 1.x release, minor versions included, is offered as "Update available" on TrueNAS and
  arrives with `docker compose pull` elsewhere. A person still presses the button: nothing
  updates on its own.
- A minor version can migrate the catalog. Startup already keeps a copy in `app/state-backups/`
  before each migration (`12-deployment.md` §8.1) and the release notes say how to go back; the
  notes card on TrueNAS and the user guide ask for a verified backup before Update.
- Installations with a series tag (`1.1`, `1.2`) move to `1` once, by hand (user guide,
  "Updating"). On TrueNAS the stored YAML has the anchors expanded, so the image is changed in
  three places.
- The `1` tag exists from the first release published after this change.

**Alternatives.**

- `latest`: it would also move to 2.0, which needs a migration.
- Keeping `X.Y` and asking operators to edit the tag for each minor version: that is what left
  installations behind.
- Automatic updates (a Watchtower-like container): an update needs a backup first and someone
  who reads the notes.
