# SPDX-License-Identifier: AGPL-3.0-only
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
STATICCHECK = honnef.co/go/tools/cmd/staticcheck@v0.8.2
GOVULNCHECK = golang.org/x/vuln/cmd/govulncheck@v1.9.0
LDFLAGS = -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build check fmt vet lint vuln test test-integration scenarios image clean errors errors-check tidy-check pg-image-check vuln-binary fuzz

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o sqsolve ./cmd/sqsolve

## check: format, vet, staticcheck, unit tests, error codes and govulncheck
check: fmt vet lint test errors-check vuln

fmt:
	@out=$$(gofmt -l cmd internal); if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

lint:
	go run $(STATICCHECK) ./...

## tidy-check: go.mod and go.sum are what `go mod tidy` would write (needs the module cache or network)
tidy-check:
	go mod tidy -diff

vuln:
	go run $(GOVULNCHECK) ./...

## vuln-binary: govulncheck on a built binary, for example the one inside the image: make vuln-binary BINARY=path
vuln-binary:
	@test -n "$(BINARY)" || { echo "usage: make vuln-binary BINARY=path/to/sqsolve"; exit 1; }
	go run $(GOVULNCHECK) -mode=binary $(BINARY)

## pg-image-check: the PostgreSQL image (tag and digest) is the same in the Dockerfile and in deploy/compose.yaml
pg-image-check:
	@df=$$(sed -n -E 's/^FROM (--platform=[^ ]+ )?(postgres:[^ ]+).*/\2/p' Dockerfile); \
	dc=$$(sed -n -E 's/^[[:space:]]*image:[[:space:]]+(postgres:[^[:space:]#]+).*/\1/p' deploy/compose.yaml); \
	case "$$df" in postgres:*@sha256:*) ;; *) echo "pg-image-check: no pinned postgres image in the Dockerfile"; exit 1;; esac; \
	if [ "$$df" != "$$dc" ]; then \
	  echo "pg-image-check: the PostgreSQL image differs"; echo "  Dockerfile:          $$df"; echo "  deploy/compose.yaml: $$dc"; \
	  echo "Update both files to the same tag and digest (Dependabot opens one pull request per file)."; exit 1; \
	fi; echo "pg-image-check: $$df"

## test: tests; integration tests are skipped when PostgreSQL is not installed
test:
	go test -race -count=1 ./...

## fuzz: runs every fuzz target (func Fuzz... in a _test.go file) for FUZZTIME each; `make test` already runs their seeds
FUZZTIME ?= 30s
fuzz:
	@grep -rHo --include='*_test.go' '^func Fuzz[A-Za-z0-9_]*' cmd internal | sort -u | while IFS=: read -r file fn; do \
	  name=$${fn#func }; pkg=./$$(dirname $$file); echo "fuzz $$name in $$pkg for $(FUZZTIME)"; \
	  go test -run '^$$' -fuzz "^$$name\$$" -fuzztime $(FUZZTIME) $$pkg || exit 1; \
	done

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
	grep -rhE 'SQS-[A-Z][0-9]{3}' cmd internal --include='*.go' --exclude='*_test.go' | grep -vE '^[[:space:]]*//' \
	| grep -oE 'SQS-[A-Z][0-9]{3}( [^"`]*)?' \
	| sed -E 's/%[-+ 0-9.]*[a-zA-Z]/.../g; s/[[:space:]]+$$//; s/[|]/\\|/g' \
	| sort -u | sort -t' ' -k1,1 -s \
	| awk '{ code=$$1; $$1=""; sub(/^ */, ""); if (!(code in msg)) order[++n]=code; if (!(code in msg) || (msg[code] == "" && $$0 != "")) msg[code]=$$0 } END { for (i=1;i<=n;i++) printf "| `%s` | %s |\n", order[i], msg[order[i]] }'; } > docs/manual/errors.md
	@echo "docs/manual/errors.md: $$(grep -c '^| `SQS' docs/manual/errors.md) codes"

## errors-check: fails when one SQS- code carries two different messages (test/check_error_codes.py)
errors-check:
	@python3 -B -m unittest discover -s test -p 'test_check_error_codes.py' -q
	@python3 -B test/check_error_codes.py cmd internal
