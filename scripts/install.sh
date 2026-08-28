#!/bin/sh
# Installs the keel CLI from the latest GitHub release.
#
#   curl -fsSL https://ucgeorge.github.io/keel/install.sh | sh
#
# Environment:
#   KEEL_VERSION      release tag to install (default: the latest release)
#   KEEL_INSTALL_DIR  where to put the binary (default: /usr/local/bin when
#                     writable — with sudo if a terminal is attached —
#                     otherwise ~/.local/bin)
#
# Supports macOS and Linux on amd64/arm64. On Windows use install.ps1 or
# WSL. The archive's SHA-256 is verified against the release's checksums.txt.
set -eu

REPO="UcGeorge/keel"

say()  { printf '%s\n' "$*"; }
fail() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

have() { command -v "$1" >/dev/null 2>&1; }

# --- platform ----------------------------------------------------------------
os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in
  darwin|linux) ;;
  mingw*|msys*|cygwin*) fail "use install.ps1 on Windows: irm https://ucgeorge.github.io/keel/install.ps1 | iex" ;;
  *) fail "unsupported operating system: $os" ;;
esac

arch="$(uname -m)"
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "unsupported architecture: $arch" ;;
esac

# --- fetch helpers -----------------------------------------------------------
if have curl; then
  fetch()      { curl -fsSL "$1" -o "$2"; }
  latest_tag() { curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##'; }
elif have wget; then
  fetch()      { wget -q "$1" -O "$2"; }
  latest_tag() { wget -q --max-redirect=5 --server-response -O /dev/null "https://github.com/$REPO/releases/latest" 2>&1 | sed -n 's#.*Location: .*/tag/##p' | tail -1 | tr -d '\r'; }
else
  fail "curl or wget is required"
fi

# --- version -----------------------------------------------------------------
version="${KEEL_VERSION:-}"
if [ -z "$version" ]; then
  version="$(latest_tag)"
  [ -n "$version" ] || fail "could not determine the latest release of $REPO"
fi
case "$version" in v*) ;; *) version="v$version" ;; esac
bare="${version#v}"

archive="keel_${bare}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

# --- download and verify -----------------------------------------------------
tmp="$(mktemp -d 2>/dev/null || mktemp -d -t keel)"
trap 'rm -rf "$tmp"' EXIT INT TERM

say "Downloading keel $version for $os/$arch"
fetch "$base/$archive" "$tmp/$archive" || fail "download failed: $base/$archive"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

expected="$(grep " $archive\$" "$tmp/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || fail "no checksum for $archive in checksums.txt"
if have sha256sum; then
  actual="$(sha256sum "$tmp/$archive" | awk '{print $1}')"
elif have shasum; then
  actual="$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')"
else
  fail "sha256sum or shasum is required to verify the download"
fi
[ "$actual" = "$expected" ] || fail "checksum mismatch for $archive (expected $expected, got $actual)"

tar -xzf "$tmp/$archive" -C "$tmp" keel

# --- install -----------------------------------------------------------------
use_sudo=""
dir="${KEEL_INSTALL_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then
    dir=/usr/local/bin
  elif have sudo && [ -t 0 ]; then
    dir=/usr/local/bin
    use_sudo=sudo
  else
    dir="$HOME/.local/bin"
  fi
fi
$use_sudo mkdir -p "$dir"
[ -n "$use_sudo" ] && say "Installing to $dir (sudo may prompt for your password)"
$use_sudo install -m 0755 "$tmp/keel" "$dir/keel"

say "Installed $("$dir/keel" --version) to $dir/keel"
case ":$PATH:" in
  *":$dir:"*) ;;
  *)
    say ""
    say "$dir is not on your PATH. Add it to your shell profile, e.g.:"
    say "  export PATH=\"$dir:\$PATH\""
    ;;
esac
say ""
say "Next: cd into a repository and run 'keel dev' — the docs are at https://keel-cloud.mintlify.site"
