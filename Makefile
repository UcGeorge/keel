VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  = -X github.com/smart-minds/keel/internal/version.Version=$(VERSION)

.PHONY: build build-keel build-cloud css sqlc test test-short vet fmt run-cloud embed clean

## build: compile both binaries into ./bin
build: build-keel build-cloud

build-keel:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/keel ./cmd/keel

build-cloud:
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/keel-cloud ./cmd/keel-cloud

## css: recompile Tailwind into the embedded stylesheet (requires npm install)
css:
	npx tailwindcss -i internal/web/tailwind.css -o internal/web/static/css/app.css --minify

## sqlc: regenerate the database query code
sqlc:
	sqlc generate

## test: full test suite (uses Docker for engine + cloud E2E tests)
test:
	go test ./... -timeout 600s

## test-short: fast tests only (no Docker, no PostgreSQL)
test-short:
	go test ./... -short

vet:
	go vet ./...

fmt:
	gofmt -w cmd internal

## run-cloud: run keel-cloud against the compose PostgreSQL
run-cloud: build-cloud
	KEEL_DATABASE_URL=postgres://keel:keel@localhost:5432/keel?sslmode=disable ./bin/keel-cloud

## embed: vendor the keel CLI into another repository — builds binaries for
## macOS/Linux/Windows (amd64+arm64) into DIR/.keel/bin and adds `make keel-*`
## targets to DIR's Makefile, so that repo's team can use Keel without this
## source tree. Re-run to update; the managed block is replaced in place.
embed:
	@test -n "$(DIR)" || (echo "DIR is required, e.g. make embed DIR=../my-project" && exit 1)
	./scripts/embed.sh "$(DIR)"

clean:
	rm -rf bin

# >>> keel targets — managed by Keel; re-run `make embed` from the Keel repository to update >>>
KEEL_ROOT := $(patsubst %/,%,$(dir $(abspath $(firstword $(MAKEFILE_LIST)))))
ifeq ($(OS),Windows_NT)
KEEL_OS := windows
KEEL_EXT := .exe
ifeq ($(PROCESSOR_ARCHITECTURE),ARM64)
KEEL_ARCH := arm64
else
KEEL_ARCH := amd64
endif
else
KEEL_OS := $(shell uname -s | tr '[:upper:]' '[:lower:]')
KEEL_ARCH := $(shell uname -m | sed -e 's/^x86_64$$/amd64/' -e 's/^aarch64$$/arm64/')
KEEL_EXT :=
endif
KEEL_BIN := $(KEEL_ROOT)/.keel/bin/keel-$(KEEL_OS)-$(KEEL_ARCH)$(KEEL_EXT)

.PHONY: keel keel-dev keel-validate keel-deploy keel-manifest keel-run keel-bin-check

# `keel` is defined first so that in a Makefile created by the embed, a bare
# `make` shows the CLI help; appended to an existing Makefile, the project's
# own first target stays the default.

## keel: show the Keel CLI help
keel: keel-bin-check
	@"$(KEEL_BIN)" --help

keel-bin-check:
	@test -x "$(KEEL_BIN)" || { echo "No Keel binary for $(KEEL_OS)/$(KEEL_ARCH) at $(KEEL_BIN)."; echo "Ask a maintainer to re-run 'make embed DIR=$(KEEL_ROOT)' from the Keel repository."; exit 1; }

## keel-dev: run the Keel UI for this repository (pass flags via ARGS, e.g. ARGS="-p 3500")
keel-dev: keel-bin-check
	@"$(KEEL_BIN)" dev $(ARGS)

## keel-validate: validate this repository's keel.yaml
keel-validate: keel-bin-check
	@"$(KEEL_BIN)" validate

## keel-deploy: run a deployment, e.g. make keel-deploy ARGS="production -t client-a"
keel-deploy: keel-bin-check
	@"$(KEEL_BIN)" deploy $(ARGS)

## keel-manifest: export a variable manifest, e.g. make keel-manifest ARGS="production -o values.md"
keel-manifest: keel-bin-check
	@"$(KEEL_BIN)" manifest $(ARGS)

## keel-run: run any Keel command, e.g. make keel-run ARGS="init"
keel-run: keel-bin-check
	@"$(KEEL_BIN)" $(ARGS)
# <<< keel targets <<<
