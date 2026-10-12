#!/bin/sh
# perch installer: POSIX sh (dash, bash, zsh, busybox, Termux).
#
#   curl -fsSL https://raw.githubusercontent.com/amirhosseinbanaei/perch/main/install.sh | sh
#
# PERCH_VERSION      version to install (default: the latest release)
# PERCH_INSTALL_DIR  where to put the binary (default: $PREFIX/bin on Termux, /usr/local/bin when
#                    writable, else ~/.local/bin)
# PERCH_BASE_URL     download base (default: https://github.com/amirhosseinbanaei/perch/releases);
#                    files are read from $PERCH_BASE_URL/download/v<version>/
#
# Re-running it upgrades in place. It never calls sudo.
set -eu

BASE="${PERCH_BASE_URL:-https://github.com/amirhosseinbanaei/perch/releases}"
BASE="${BASE%/}"

say() { printf '%s\n' "$*"; }
die() {
	printf 'perch install: %s\n' "$*" >&2
	exit 1
}
have() { command -v "$1" >/dev/null 2>&1; }

# ── platform ──────────────────────────────────────────────────────────────────
case "$(uname -s)" in
Linux) os=linux ;;
Darwin) os=darwin ;;
*) die "unsupported OS: $(uname -s). Release builds exist for Linux and macOS." ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
armv7l | armv7 | armv8l) arch=armv7 ;;
*) die "unsupported architecture: $(uname -m). Release builds exist for amd64, arm64 and armv7." ;;
esac
if [ "$os" = darwin ] && [ "$arch" = armv7 ]; then
	die "unsupported platform: darwin/armv7"
fi

# ── download helpers ──────────────────────────────────────────────────────────
if have curl; then
	fetch() { curl -fsSL -o "$2" "$1"; }
elif have wget; then
	fetch() { wget -q -O "$2" "$1"; }
else
	die "neither curl nor wget is installed"
fi

# The latest version, from the redirect of $BASE/latest (…/releases/tag/vX.Y.Z). No API call,
# so no rate limit.
latest() {
	if have curl; then
		curl -fsSLI -o /dev/null -w '%{url_effective}' "$BASE/latest"
	else
		wget -S --spider "$BASE/latest" 2>&1 | sed -n 's/^ *[Ll]ocation: *\([^ ]*\).*/\1/p' | tail -n 1
	fi
}

if [ -n "${PERCH_VERSION:-}" ]; then
	version="${PERCH_VERSION#v}"
else
	url="$(latest)" || die "could not reach $BASE/latest"
	url="$(printf '%s' "$url" | tr -d '\r')"
	tag="${url##*/}"
	version="${tag#v}"
fi
case "$version" in
'' | */* | *' '*) die "could not find the latest version at $BASE/latest; set PERCH_VERSION" ;;
esac

# ── fetch and verify ──────────────────────────────────────────────────────────
tmp="$(mktemp -d 2>/dev/null || mktemp -d -t perch)"
trap 'rm -rf "$tmp"' EXIT
trap 'rm -rf "$tmp"; exit 130' INT TERM

file="perch_${version}_${os}_${arch}.tar.gz"
say "perch ${version} for ${os}/${arch}"
fetch "$BASE/download/v${version}/${file}" "$tmp/$file" || die "download failed: $BASE/download/v${version}/${file}"
fetch "$BASE/download/v${version}/checksums.txt" "$tmp/checksums.txt" || die "download failed: checksums.txt"

expected="$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$tmp/checksums.txt")"
[ -n "$expected" ] || die "$file is not listed in checksums.txt"
if have sha256sum; then
	actual="$(sha256sum "$tmp/$file" | awk '{ print $1 }')"
elif have shasum; then
	actual="$(shasum -a 256 "$tmp/$file" | awk '{ print $1 }')"
else
	die "neither sha256sum nor shasum is installed; cannot verify the download"
fi
if [ "$expected" != "$actual" ]; then
	die "checksum mismatch for $file (expected $expected, got $actual); refusing to install"
fi

gzip -dc "$tmp/$file" | (cd "$tmp" && tar -xf - perch) || die "could not unpack $file"
[ -f "$tmp/perch" ] || die "$file holds no perch binary"

# ── install ───────────────────────────────────────────────────────────────────
termux=false
if [ -n "${TERMUX_VERSION:-}" ] || case "${PREFIX:-}" in *com.termux*) true ;; *) false ;; esac; then
	termux=true
fi

hint=""
if [ -n "${PERCH_INSTALL_DIR:-}" ]; then
	dir="$PERCH_INSTALL_DIR"
elif [ "$termux" = true ]; then
	dir="$PREFIX/bin"
elif [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
	dir=/usr/local/bin
else
	dir="$HOME/.local/bin"
	hint=yes
fi
mkdir -p "$dir" || die "cannot create $dir"
[ -w "$dir" ] || die "$dir is not writable. Re-run with PERCH_INSTALL_DIR set, or as root:
  curl -fsSL https://raw.githubusercontent.com/amirhosseinbanaei/perch/main/install.sh | sudo sh"

# Replace atomically, so a running perch keeps working during an upgrade.
cp "$tmp/perch" "$dir/.perch.new"
chmod 755 "$dir/.perch.new"
mv -f "$dir/.perch.new" "$dir/perch"
say "installed $dir/perch"

if [ -n "$hint" ]; then
	case ":$PATH:" in
	*":$dir:"*) ;;
	*)
		say ""
		say "$dir is not on your PATH. Add it, e.g. in ~/.profile:"
		say "  export PATH=\"\$HOME/.local/bin:\$PATH\""
		say "or install system-wide with:"
		say "  curl -fsSL https://raw.githubusercontent.com/amirhosseinbanaei/perch/main/install.sh | sudo sh"
		;;
	esac
fi

if ! have tmux; then
	cmd="install tmux with your package manager"
	if [ "$termux" = true ]; then
		cmd="pkg install tmux"
	elif have apt-get; then
		cmd="sudo apt install tmux"
	elif have dnf; then
		cmd="sudo dnf install tmux"
	elif have pacman; then
		cmd="sudo pacman -S tmux"
	elif have apk; then
		cmd="sudo apk add tmux"
	elif have brew; then
		cmd="brew install tmux"
	fi
	say ""
	say "warning: tmux is not installed. Install it with: $cmd"
fi

"$dir/perch" version || true
