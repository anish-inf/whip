#!/bin/sh
# whip installer — downloads the released binary, verifies its checksum
# against the published SHA256SUMS, and installs it to a directory on your PATH.
#
# Everything it does is printed as it happens. Read it first if you like:
#   curl -fsSL https://raw.githubusercontent.com/context-labs/whip/main/install.sh
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/context-labs/whip/main/install.sh | sh
# Env overrides:
#   WHIP_VERSION=v0.1.0   pin a version (default: latest release)
#   WHIP_BIN_DIR=~/bin    force the install directory
#   GH_TOKEN=...           GitHub token (needed while the repo is private;
#                          or just have the `gh` CLI authenticated)
set -eu

REPO="context-labs/whip"
API="https://api.github.com/repos/$REPO"

say() { printf '  %s\n' "$*"; }
die() { printf 'whip install: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

need uname
need mktemp
need curl

# --- auth ---
# While the repo is private, release assets need a token. Prefer the `gh`
# CLI's stored auth; fall back to GH_TOKEN; otherwise try anonymous (works
# once the repo is public).
TOKEN=""
if command -v gh >/dev/null 2>&1 && gh auth status >/dev/null 2>&1; then
  TOKEN=$(gh auth token)
elif [ -n "${GH_TOKEN:-}" ]; then
  TOKEN="$GH_TOKEN"
fi
api() { # api <path> — JSON on stdout
  if [ -n "$TOKEN" ]; then
    curl -fsSL -H "Authorization: Bearer $TOKEN" -H "Accept: application/vnd.github+json" "$API/$1"
  else
    curl -fsSL -H "Accept: application/vnd.github+json" "$API/$1"
  fi
}
api_asset() { # api_asset <id> <outfile> — binary download
  if [ -n "$TOKEN" ]; then
    curl -fsSL -H "Authorization: Bearer $TOKEN" -H "Accept: application/octet-stream" "$API/releases/assets/$1" -o "$2"
  else
    curl -fsSL -H "Accept: application/octet-stream" "$API/releases/assets/$1" -o "$2"
  fi
}

# a sha256 tool
if command -v sha256sum >/dev/null 2>&1; then SHA() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then SHA() { shasum -a 256 "$1" | cut -d' ' -f1; }
else die "need sha256sum or shasum"; fi

# --- platform ---
os=$(uname -s); arch=$(uname -m)
case "$os" in Linux) os=linux;; Darwin) os=darwin;; *) die "unsupported OS: $os (Linux/macOS only)";; esac
case "$arch" in x86_64|amd64) arch=x64;; arm64|aarch64) arch=arm64;; *) die "unsupported arch: $arch";; esac
ASSET="whip-$os-$arch"

# --- version + asset IDs ---
VERSION="${WHIP_VERSION:-}"
say "Resolving ${VERSION:-latest} release..."
if [ -n "$VERSION" ]; then
  REL=$(api "releases/tags/$VERSION") || die "release $VERSION not found"
else
  REL=$(api "releases/latest") || die "could not reach the releases API"
fi
[ -n "$REL" ] || die "empty release response"

# Parse the release JSON. python3 ships with macOS and virtually every
# Linux distro; grep/sed fallback covers the odd box without it.
json_field() { # json_field <python-expr> — reads $REL on stdin
  printf '%s' "$REL" | python3 -c "import json,sys; r=json.load(sys.stdin); print($1)" 2>/dev/null
}
if command -v python3 >/dev/null 2>&1; then
  VERSION=$(json_field "r['tag_name']")
  BIN_ID=$(json_field "next(a['id'] for a in r['assets'] if a['name']=='$ASSET')")
  SUMS_ID=$(json_field "next(a['id'] for a in r['assets'] if a['name']=='SHA256SUMS')")
else
  VERSION=$(printf '%s' "$REL" | grep '"tag_name"' | head -1 | sed 's/.*"tag_name": *"\([^"]*\)".*/\1/')
  # asset objects are one-per-line after splitting on the assets array's },{;
  # within an asset object the first "id" is the asset's own.
  BIN_ID=$(printf '%s' "$REL" | tr -d '\n' | sed 's/.*"assets": *\[//' | sed 's/},{/}\n{/g' \
    | grep "\"name\": *\"$ASSET\"" | head -1 | sed 's/^[^{]*{[^i]*"id": *\([0-9]*\).*/\1/')
  SUMS_ID=$(printf '%s' "$REL" | tr -d '\n' | sed 's/.*"assets": *\[//' | sed 's/},{/}\n{/g' \
    | grep '"name": *"SHA256SUMS"' | head -1 | sed 's/^[^{]*{[^i]*"id": *\([0-9]*\).*/\1/')
fi
[ -n "$VERSION" ] || die "could not determine the latest release (set WHIP_VERSION)"
[ -n "$BIN_ID" ] || die "no asset $ASSET in release $VERSION"
[ -n "$SUMS_ID" ] || die "no SHA256SUMS in release $VERSION"

printf '\nwhip %s — %s\n' "$VERSION" "$ASSET"

# --- download + verify ---
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
say "Downloading $ASSET..."
api_asset "$BIN_ID" "$tmp/$ASSET" || die "download failed"
say "Downloading SHA256SUMS..."
api_asset "$SUMS_ID" "$tmp/SHA256SUMS" || die "checksum list download failed"

expected=$(grep " $ASSET\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
[ -n "$expected" ] || die "no checksum for $ASSET in SHA256SUMS"
actual=$(SHA "$tmp/$ASSET")
say "expected sha256: $expected"
say "actual   sha256: $actual"
[ "$expected" = "$actual" ] || die "CHECKSUM MISMATCH — refusing to install. The download does not match the published checksum."
say "OK: checksum verified"
chmod 755 "$tmp/$ASSET"

# --- pick an install dir on PATH (first writable wins; create ~/.local/bin if needed) ---
in_path() { case ":$PATH:" in *":$1:"*) return 0;; *) return 1;; esac; }
DEST=""
if [ -n "${WHIP_BIN_DIR:-}" ]; then
  mkdir -p "$WHIP_BIN_DIR" 2>/dev/null || true
  DEST="$WHIP_BIN_DIR"
else
  for d in /usr/local/bin /opt/homebrew/bin "$HOME/.local/bin" "$HOME/bin"; do
    if [ -d "$d" ] && [ -w "$d" ]; then DEST="$d"; break; fi
  done
  # nothing writable existed — create the standard user dir
  [ -z "$DEST" ] && { mkdir -p "$HOME/.local/bin" && DEST="$HOME/.local/bin"; }
fi
[ -n "$DEST" ] && [ -w "$DEST" ] || die "no writable install directory found"

mv "$tmp/$ASSET" "$DEST/whip"
say "OK: installed to $DEST/whip"

# --- ensure it's on PATH ---
if ! in_path "$DEST"; then
  # Single-quote DEST (escaping embedded quotes) so a directory with spaces or
  # shell metacharacters can't inject code when the rc file is sourced.
  esc=$(printf '%s' "$DEST" | sed "s/'/'\\\\''/g")
  line="export PATH='$esc':\"\$PATH\""
  # Update every shell rc that exists, and always ~/.profile (create it if
  # missing) so a fresh machine still gets PATH on next login.
  touch "$HOME/.profile" 2>/dev/null || true
  for rc in "$HOME/.zshrc" "$HOME/.bashrc" "$HOME/.profile"; do
    [ -e "$rc" ] || continue
    grep -qF "$line" "$rc" 2>/dev/null || printf '\n# whip\n%s\n' "$line" >> "$rc"
  done
  say "Added $DEST to your PATH — restart your shell, or run now: $line"
fi

printf '\n'
"$DEST/whip" --version || true

if [ "$os" = "darwin" ] && [ "$arch" = "arm64" ]; then
  cat <<'EOF'

macOS notes:
  • First run may trigger Gatekeeper — if "whip" is blocked, allow it in
    System Settings → Privacy & Security, or run: xattr -d com.apple.quarantine $(which whip)
  • computer_exec's native tier (driving apps) needs Accessibility + Screen
    Recording granted to your terminal app when prompted.
EOF
fi

printf '\nDone. Run `whip` to start.\n'
