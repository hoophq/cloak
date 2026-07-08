#!/bin/sh
# cloak installer.
#
#   curl -fsSL https://raw.githubusercontent.com/hoophq/cloak/main/install.sh | sh
#
# Environment overrides:
#   CLOAK_VERSION=v0.1.0    install a specific tag (default: latest release)
#   CLOAK_BIN_DIR=/path     install directory (default: /usr/local/bin, else ~/.local/bin)
set -eu

REPO="hoophq/cloak"

err() { echo "cloak-install: $*" >&2; exit 1; }
have() { command -v "$1" >/dev/null 2>&1; }

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  darwin | linux) ;;
  *) err "unsupported OS: $os (macOS and Linux only)" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) err "unsupported architecture: $arch" ;;
esac

if have curl; then
  dl() { curl -fsSL "$1"; }
  dlo() { curl -fsSL -o "$2" "$1"; }
elif have wget; then
  dl() { wget -qO- "$1"; }
  dlo() { wget -qO "$2" "$1"; }
else
  err "need curl or wget"
fi

tag="${CLOAK_VERSION:-}"
if [ -z "$tag" ]; then
  tag=$(dl "https://api.github.com/repos/$REPO/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)
  [ -n "$tag" ] || err "could not determine the latest version; set CLOAK_VERSION"
fi
ver=${tag#v} # release filenames drop the leading v

base="https://github.com/$REPO/releases/download/$tag"
archive="cloak_${ver}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "cloak-install: downloading $archive ($tag)"
dlo "$base/$archive" "$tmp/$archive" || err "download failed: $base/$archive"

# Verify the checksum when a tool is available.
if dlo "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null; then
  want=$(grep " ${archive}\$" "$tmp/checksums.txt" | awk '{print $1}')
  if [ -n "$want" ]; then
    if have sha256sum; then got=$(sha256sum "$tmp/$archive" | awk '{print $1}')
    elif have shasum; then got=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
    else got=""; fi
    [ -z "$got" ] || [ "$got" = "$want" ] || err "checksum mismatch for $archive"
    [ -z "$got" ] || echo "cloak-install: checksum OK"
  fi
else
  echo "cloak-install: warning: checksums.txt unavailable; skipping verification" >&2
fi

tar -xzf "$tmp/$archive" -C "$tmp" cloak || err "extract failed"

bindir="${CLOAK_BIN_DIR:-}"
if [ -z "$bindir" ]; then
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    bindir=/usr/local/bin
  else
    bindir="$HOME/.local/bin"
  fi
fi
mkdir -p "$bindir"

if [ -w "$bindir" ]; then
  install -m 0755 "$tmp/cloak" "$bindir/cloak"
elif have sudo; then
  echo "cloak-install: installing to $bindir (needs sudo)"
  sudo install -m 0755 "$tmp/cloak" "$bindir/cloak"
else
  err "cannot write to $bindir and sudo is unavailable; set CLOAK_BIN_DIR"
fi

echo "cloak-install: installed to $bindir/cloak"
case ":$PATH:" in
  *":$bindir:"*) ;;
  *) echo "cloak-install: note: $bindir is not on your PATH" >&2 ;;
esac
"$bindir/cloak" --version || true
