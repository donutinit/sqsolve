# SPDX-License-Identifier: AGPL-3.0-only
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
STATICCHECK = honnef.co/go/tools/cmd/staticcheck@v0.8.1
GOVULNCHECK = golang.org/x/vuln/cmd/govulncheck@v1.9.0
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build check fmt vet lint vuln test test-integration scenarios image clean errors

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o sqsolve ./cmd/sqsolve

## check: format, vet, staticcheck, unit tests and govulncheck
check: fmt vet lint test vuln

fmt:
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

lint:
	go run $(STATICCHECK) ./...

vuln:
	go run $(GOVULNCHECK) ./...

## test: tests; integration tests are skipped when PostgreSQL is not installed
test:
	go test -race -count=1 ./...

## test-integration: requires PostgreSQL (set SQSOLVE_TEST_PG_BIN if it is not in PATH)
test-integration:
	SQSOLVE_REQUIRE_PG=1 go test -count=1 ./...

## scenarios: induced failures against real PostgreSQL 18 and the real server (about 7 minutes)
scenarios:
	python3 test/scenarios.py

image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t sqsolve:$(VERSION) .

clean:
	rm -f sqsolve coverage.out

## errors: regenerate docs/manual/errors.md with every SQS- code in the source
errors:
	@{ printf '# Error codes\n\nGenerated with `make errors` from the source code; do not edit by hand. Each code\nis listed with the text shown in the panel, the API, the console or the log.\n\n| Code | Message |\n|---|---|\n'; \
	grep -rhoE 'SQS-[A-Z][0-9]{3}[^"`]*' cmd internal --include='*.go' --exclude='*_test.go' \
	| sed -E 's/%[-+ 0-9.]*[a-zA-Z]/.../g; s/[[:space:]]+$$//; s/[|]/\\|/g' \
	| sort -u | sort -t' ' -k1,1 -s | awk -F' ' '!seen[$$1]++ { code=$$1; $$1=""; sub(/^ */, ""); printf "| `%s` | %s |\n", code, $$0 }'; } > docs/manual/errors.md
	@echo "docs/manual/errors.md: $$(grep -c '^| `SQS' docs/manual/errors.md) codes"
