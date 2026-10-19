# SPDX-License-Identifier: AGPL-3.0-only
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo desconocido)
STATICCHECK = honnef.co/go/tools/cmd/staticcheck@v0.8.1
GOVULNCHECK = golang.org/x/vuln/cmd/govulncheck@v1.9.0
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build check fmt vet lint vuln test test-integration scenarios image clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o sqsolve ./cmd/sqsolve

## check: formato, vet, staticcheck, pruebas unitarias y govulncheck
check: fmt vet lint test vuln

fmt:
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then echo "sin formato:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

lint:
	go run $(STATICCHECK) ./...

vuln:
	go run $(GOVULNCHECK) ./...

## test: pruebas; las de integración se saltan si no hay PostgreSQL instalado
test:
	go test -race -count=1 ./...

## test-integration: exige PostgreSQL (define SQSOLVE_TEST_PG_BIN si no está en el PATH)
test-integration:
	SQSOLVE_REQUIRE_PG=1 go test -count=1 ./...

## scenarios: fallas provocadas contra PostgreSQL 18 y el servidor reales (unos 7 minutos)
scenarios:
	python3 test/scenarios.py

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t sqsolve:$(VERSION) .

clean:
	rm -f sqsolve coverage.out
