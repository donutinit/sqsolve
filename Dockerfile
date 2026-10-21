# syntax=docker/dockerfile:1
# SPDX-License-Identifier: AGPL-3.0-only

# The final image starts from the same PostgreSQL image as the server, so
# pg_dump and pg_restore always match the server's major version (ADR 0016).
#
# Both images are written literally in their FROM lines (not through ARG) so that
# Dependabot can read and update them (.github/dependabot.yml). The PostgreSQL
# reference must stay identical to the one in deploy/compose.yaml: CI checks it
# (make pg-image-check).

FROM --platform=$BUILDPLATFORM golang:1.27-trixie@sha256:47667934874913a449b96faa7ef4777fa285d9ec29126ee80ed61c45b7cccf0e AS build
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# -w drops DWARF but keeps the symbol table: govulncheck -mode=binary needs it to scan the
# binary that ships (CI does it on the file inside the image) and reports false positives
# on a binary stripped with -s. -buildvcs=false because the context has no .git: the
# version and commit are injected through -ldflags.
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
      -ldflags "-w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/sqsolve ./cmd/sqsolve

FROM postgres:18.6-trixie@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336
ARG VERSION=dev COMMIT=unknown
LABEL org.opencontainers.image.title="sqsolve" \
      org.opencontainers.image.description="Free server for DaVinci Resolve project libraries" \
      org.opencontainers.image.source="https://github.com/donutinit/sqsolve" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
COPY --from=build /out/sqsolve /usr/local/bin/sqsolve
ENV SQSOLVE_LISTEN=":9432"
EXPOSE 9432
USER 1000:1000
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["sqsolve", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/sqsolve"]
CMD ["serve"]
