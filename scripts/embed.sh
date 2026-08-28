#!/usr/bin/env bash
# embed.sh <target-dir>
#
# Embeds the Keel CLI into another repository so people WITHOUT the Keel
# source can run it:
#
#   1. Cross-compiles the `keel` binary (CLI + local UI; not keel-cloud) for
#      macOS, Linux, and Windows on amd64/arm64 into <dir>/.keel/bin/, with
#      a SHA256SUMS file.
#   2. Writes <dir>/.keel/.gitignore so Keel's machine-local state stays
#      ignored but bin/ is committable, and removes any `.keel/` line from
#      the target's root .gitignore that would override that.
#   3. Creates or updates a marker-delimited block of `keel*` targets in
#      <dir>/Makefile that picks the right binary for the host OS/arch.
#
# Re-running replaces the binaries and the Makefile block in place, so an
# embed is also the update path. Invoked via `make embed DIR=<dir>`.
set -euo pipefail

TARGET_DIR="${1:-}"
if [[ -z "$TARGET_DIR" ]]; then
  echo "usage: $0 <target-dir>   (or: make embed DIR=<target-dir>)" >&2
  exit 2
fi
if [[ ! -d "$TARGET_DIR" ]]; then
  echo "Target directory does not exist: $TARGET_DIR" >&2
  exit 1
fi
TARGET_DIR="$(cd "$TARGET_DIR" && pwd)"

KEEL_REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$KEEL_REPO"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X github.com/smart-minds/keel/internal/version.Version=${VERSION}"
PLATFORMS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"

BIN_DIR="$TARGET_DIR/.keel/bin"
mkdir -p "$BIN_DIR"

echo "Embedding keel ${VERSION} into ${TARGET_DIR}"
echo
for target in $PLATFORMS; do
  os="${target%/*}"
  arch="${target#*/}"
  ext=""
  [[ "$os" == "windows" ]] && ext=".exe"
  out="$BIN_DIR/keel-$os-$arch$ext"
  echo "  building $os/$arch"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/keel
done

(
  cd "$BIN_DIR"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 keel-* > SHA256SUMS
  else
    sha256sum keel-* > SHA256SUMS
  fi
)

# --- .keel/.gitignore: keep state ignored, keep bin/ committable ------------
cat > "$TARGET_DIR/.keel/.gitignore" <<'EOF'
# Managed by Keel. Everything in .keel is machine-local state (dev.db, …),
# except the vendored CLI binaries under bin/, which are meant to be
# committed so the whole team can run `make keel-*` without the Keel source.
*
!.gitignore
!bin
!bin/**
EOF

# A root-level `.keel/` ignore would override the nested rules and keep the
# binaries out of the repository — drop such lines (state stays ignored via
# .keel/.gitignore).
ROOT_IGNORE="$TARGET_DIR/.gitignore"
if [[ -f "$ROOT_IGNORE" ]] && grep -qE '^[[:space:]]*/?\.keel/?[[:space:]]*$' "$ROOT_IGNORE"; then
  tmp="$(mktemp)"
  grep -vE '^[[:space:]]*/?\.keel/?[[:space:]]*$' "$ROOT_IGNORE" \
    | grep -vxF '# Keel local state (keel dev)' > "$tmp" || true
  mv "$tmp" "$ROOT_IGNORE"
  echo
  echo "  note: removed the '.keel/' line from $ROOT_IGNORE — Keel state is"
  echo "        still ignored via .keel/.gitignore, but bin/ can now be committed."
fi

# --- Makefile block ----------------------------------------------------------
MK="$TARGET_DIR/Makefile"
BEGIN_MARK='# >>> keel targets — managed by Keel; re-run `make embed` from the Keel repository to update >>>'
END_MARK='# <<< keel targets <<<'

BLOCK_FILE="$(mktemp)"
cat > "$BLOCK_FILE" <<'EOF'
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
EOF

if [[ -f "$MK" ]] && grep -qF "$BEGIN_MARK" "$MK"; then
  # Replace the existing managed block in place.
  tmp="$(mktemp)"
  awk -v b="$BEGIN_MARK" -v e="$END_MARK" -v blk="$BLOCK_FILE" '
    index($0, b) { skip = 1; print; while ((getline line < blk) > 0) print line; next }
    index($0, e) { skip = 0 }
    !skip { print }
  ' "$MK" > "$tmp"
  mv "$tmp" "$MK"
  MK_ACTION="updated keel targets in"
else
  # Append (or create) the managed block.
  { [[ -f "$MK" ]] && [[ -s "$MK" ]] && echo; } >> "$MK" 2>/dev/null || true
  {
    echo "$BEGIN_MARK"
    cat "$BLOCK_FILE"
    echo "$END_MARK"
  } >> "$MK"
  MK_ACTION="added keel targets to"
fi
rm -f "$BLOCK_FILE"

echo
echo "Done — $MK_ACTION $MK"
echo
echo "  binaries:  $BIN_DIR ($(du -sh "$BIN_DIR" 2>/dev/null | cut -f1))"
(cd "$BIN_DIR" && ls -1 keel-* | sed 's/^/             /')
echo
echo "Anyone with this repository can now run:"
echo "  make keel            # CLI help"
echo "  make keel-dev        # the Keel UI"
echo "  make keel-validate   # validate keel.yaml"
echo "  make keel-run ARGS=\"…\"  # any other command"
echo
echo "Commit .keel/bin, .keel/.gitignore, and the Makefile to share it."
