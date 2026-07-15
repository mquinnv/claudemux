#!/bin/sh
# claudemux installer.
#   curl -fsSL https://raw.githubusercontent.com/mquinnv/claudemux/main/install.sh | sh
#
# Env:
#   CLAUDEMUX_PREFIX   install dir (default ~/.local/bin)
#   CLAUDEMUX_VERSION  version to install (default: latest release)
set -eu

REPO="mquinnv/claudemux"
PREFIX="${CLAUDEMUX_PREFIX:-$HOME/.local/bin}"

die() { echo "claudemux: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required but not installed"; }

need curl
need tar
need uname

os="$(uname -s)"
case "$os" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *)      die "unsupported OS: $os (claudemux needs tmux and bash; Windows is not supported)" ;;
esac

arch="$(uname -m)"
case "$arch" in
  arm64|aarch64) arch=arm64 ;;
  x86_64|amd64)  arch=amd64 ;;
  *)             die "unsupported architecture: $arch" ;;
esac

version="${CLAUDEMUX_VERSION:-}"
if [ -z "$version" ]; then
  version="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
  [ -n "$version" ] || die "could not determine the latest release"
fi
v="${version#v}"

tarball="claudemux_${v}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

tmp="$(mktemp -d)"
# shellcheck disable=SC2064  # $tmp must expand NOW, not at trap time
trap "rm -rf '$tmp'" EXIT INT TERM

echo "claudemux: downloading $tarball ($version)"
curl -fsSL "$base/$tarball"    -o "$tmp/$tarball" || die "download failed: $base/$tarball"
curl -fsSL "$base/SHA256SUMS"  -o "$tmp/SHA256SUMS" || die "could not fetch SHA256SUMS"

# Verify before unpacking. An installer piped from the network into a shell that
# skips this is a supply-chain hole.
expected="$(sed -n "s#^\([a-f0-9]\{64\}\)  \(\./\)\{0,1\}${tarball}\$#\1#p" "$tmp/SHA256SUMS")"
# The `\(\./\)\{0,1\}` makes the leading ./ optional: the release workflow
# writes bare filenames, but tolerate a ./ prefix in case SHA256SUMS was
# generated the other way.
[ -n "$expected" ] || die "$tarball is not listed in SHA256SUMS"

if command -v shasum >/dev/null 2>&1; then
  actual="$(shasum -a 256 "$tmp/$tarball" | cut -d' ' -f1)"
elif command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$tmp/$tarball" | cut -d' ' -f1)"
else
  die "need shasum or sha256sum to verify the download"
fi

[ "$actual" = "$expected" ] || die "checksum MISMATCH for $tarball
  expected $expected
  actual   $actual
Refusing to install."

echo "claudemux: checksum OK"

mkdir -p "$PREFIX"
tar -xzf "$tmp/$tarball" -C "$tmp"

# All four files stay siblings: claudemux resolves project-color-resolve.sh, and
# claudemux-head resolves claudemux-map.sh, by looking next to themselves.
for f in claudemux-head claudemux project-color-resolve.sh claudemux-map.sh; do
  install -m 0755 "$tmp/$f" "$PREFIX/$f"
done

echo "claudemux: installed to $PREFIX"

# Register the Claude Code hook so nobody hand-edits settings.json.
"$PREFIX/claudemux-head" hook ensure || echo "claudemux: could not register the hook automatically" >&2

case ":$PATH:" in
  *":$PREFIX:"*) ;;
  *) echo "claudemux: WARNING — $PREFIX is not on your PATH. Add it:"
     echo "    export PATH=\"$PREFIX:\$PATH\"" ;;
esac

# This installer is intentionally standalone (it does not use or require brew).
# But if brew IS here, the formula is the better-managed option — it handles
# tmux/jq/git and `brew upgrade` — so point the user at it without switching to it.
if command -v brew >/dev/null 2>&1; then
  echo "claudemux: Homebrew detected — for managed upgrades and automatic"
  echo "    tmux/jq/git, you may prefer:  brew install mquinnv/tap/claudemux"
fi

echo "claudemux: done. Run: claudemux <project-dir>"
