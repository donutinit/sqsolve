# SPDX-License-Identifier: AGPL-3.0-only
# syntax=docker/dockerfile:1

# The final image starts from the same PostgreSQL image as the server, so
# pg_dump and pg_restore always match the server's major version (ADR 0016).
ARG GO_IMAGE=golang:1.27-trixie@sha256:47667934874913a449b96faa7ef4777fa285d9ec29126ee80ed61c45b7cccf0e
ARG PG_IMAGE=postgres:18.6-trixie@sha256:74935e72241653ca55e0414067e6d8763aceb8a810eb51b452253ec3dcfc4336

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS TARGETARCH VERSION=dev COMMIT=unknown SOURCE_DATE_EPOCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION} -X main.commit=${COMMIT}" \
      -o /out/sqsolve ./cmd/sqsolve

FROM ${PG_IMAGE}
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
