#!/bin/sh
# pcoder Work Sync bootstrap. Served by pcoder with this server's URL and
# version baked in. It contains no sync logic: it checks the tools the
# client needs, downloads and verifies the static client, then runs it.
set -eu

SERVER='__SERVER__'
VERSION='__VERSION__'

say() { printf '%s\n' "$*" >&2; }
die() { say ""; say "$*"; exit 1; }

os=$(uname -s 2>/dev/null || true)
arch=$(uname -m 2>/dev/null || true)
case "$os" in
	Darwin) goos=darwin ;;
	Linux) goos=linux ;;
	*) die "Unsupported operating system: ${os:-unknown}.
Work Sync supports macOS and Linux on amd64 and arm64. Windows is not supported yet." ;;
esac
case "$arch" in
	x86_64|amd64) goarch=amd64 ;;
	arm64|aarch64) goarch=arm64 ;;
	*) die "Unsupported architecture: ${arch:-unknown}. Work Sync supports amd64 and arm64." ;;
esac

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL --proto '=http,https' "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -q -O "$2" "$1"; }
else
	die "curl or wget is required but not installed.

macOS:
  curl ships with macOS

Ubuntu/Debian:
  sudo apt-get install -y curl

Fedora:
  sudo dnf install -y curl

Install one of them, then run the same command again."
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	die "sha256sum (or shasum on macOS) is required but not installed."
fi

if ! command -v git >/dev/null 2>&1; then
	die "git is required but not installed.

macOS:
  xcode-select --install

Ubuntu/Debian:
  sudo apt-get install -y git

Fedora:
  sudo dnf install -y git

Install git, then run the same command again."
fi

command -v mktemp >/dev/null 2>&1 || die "mktemp is required but not installed."

cache="${XDG_CACHE_HOME:-$HOME/.cache}"
dir="$cache/pcoder-sync/bin/$VERSION"
bin="$dir/pcoder-sync"

if [ ! -x "$bin" ]; then
	mkdir -p "$dir" || die "Cannot create $dir."
	tmp=$(mktemp "${TMPDIR:-/tmp}/pcoder-sync.XXXXXX") || die "mktemp failed."
	trap 'rm -f "$tmp" "$tmp.sum"' EXIT INT TERM
	target="$goos-$goarch"
	fetch "$SERVER/sync/bin/$target" "$tmp" ||
		die "Client download failed.
Open $SERVER in a browser and run the command again."
	fetch "$SERVER/sync/bin/$target.sha256" "$tmp.sum" ||
		die "Checksum download failed.
Open $SERVER in a browser and run the command again."
	if [ "$(sha "$tmp")" != "$(cat "$tmp.sum")" ]; then
		die "Client checksum mismatch.
Delete $dir and run the same command again."
	fi
	chmod 700 "$tmp" || die "Cannot make the client executable."
	mv "$tmp" "$bin" || die "Cannot install $bin."
	rm -f "$tmp.sum"
fi

# restore is a local operation: it needs no server or sync code.
if [ "${1:-}" = "restore" ]; then
	exec "$bin" "$@"
fi
exec "$bin" --server "$SERVER" "$@"
