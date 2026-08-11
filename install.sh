#!/bin/sh
#
# install.sh: put a figaro binary on this machine and nothing else.
#
#   curl -fsSL https://figar.org/install.sh | sh
#   curl -fsSL https://figar.org/install.sh | sh -s -- --version v0.24.0
#   curl -fsSL https://figar.org/install.sh | sh -s -- --dir /usr/local/bin
#
# It downloads one release archive, checks its sha256 against the release's
# checksums.txt, and moves a single static binary into place. There is no
# share/figaro tree to manage: the first-party skills are embedded in the
# binary, so one file IS the install. The `fig` alias is a symlink next to it.
#
# What it deliberately does NOT do: touch your shell rc, install a service,
# stop a running daemon, or fall back to something you did not ask for. If a
# step cannot be done honestly it says so and, where the install still stands
# without it (completions, quarantine, PATH), it warns and moves on.
#
# The script is fetched over a pipe, so it must survive being read by `sh`
# with no tty, no arguments, and no repository around it. Nothing below may
# assume it lives inside a checkout, or that figar.org served it: the raw
# GitHub URL is an equally supported source.

set -eu

REPO="jack-work/figaro"
# Where release assets live. Override for a mirror, or for testing against a
# `goreleaser build --snapshot` tree served over file:// or localhost. The
# layout is always <base>/<tag>/<file>.
BASE_URL="${FIGARO_BASE_URL:-https://github.com/$REPO/releases/download}"
LATEST_URL="https://github.com/$REPO/releases/latest"

VERSION="${FIGARO_VERSION:-}"
INSTALL_DIR="${FIGARO_INSTALL_DIR:-$HOME/.local/bin}"

if [ -t 2 ]; then B=$(printf '\033[1m'); D=$(printf '\033[2m'); R=$(printf '\033[0m'); else B=""; D=""; R=""; fi

die()  { printf 'install: %s\n' "$*" >&2; exit 1; }
say()  { printf '%s==>%s %s\n' "$B" "$R" "$*" >&2; }
warn() { printf 'install: %swarning:%s %s\n' "$B" "$R" "$*" >&2; }
note() { printf '   %s%s%s\n' "$D" "$*" "$R" >&2; }
have() { command -v "$1" >/dev/null 2>&1; }

usage() {
	cat >&2 <<'EOF'
usage: install.sh [--version <vX.Y.Z>] [--dir <path>]

Installs the figaro binary (and the `fig` alias) from a GitHub release.

  --version <ver>  Release to install, with or without the leading v.
                   Default: whatever /releases/latest redirects to.
  --dir <path>     Install directory. Default: ~/.local/bin
  -h, --help       This.

Environment:
  FIGARO_VERSION       same as --version
  FIGARO_INSTALL_DIR   same as --dir
  FIGARO_BASE_URL      asset root, default the GitHub releases download URL

Piped invocation passes flags after `--`:
  curl -fsSL https://figar.org/install.sh | sh -s -- --dir /usr/local/bin
EOF
	exit 2
}

while [ $# -gt 0 ]; do
	case "$1" in
		--version) [ $# -ge 2 ] || die "--version needs a value"; VERSION="$2"; shift ;;
		--dir)     [ $# -ge 2 ] || die "--dir needs a value";     INSTALL_DIR="$2"; shift ;;
		-h|--help) usage ;;
		*)         die "unknown argument: $1 (try --help)" ;;
	esac
	shift
done

# ------------------------------------------------------------------- tools

if have curl; then
	DL=curl
elif have wget; then
	DL=wget
else
	die "neither curl nor wget found: cannot download anything"
fi

have tar || die "tar not found: the release archives are tarballs"

if have sha256sum; then
	SHA=sha256sum
elif have shasum; then
	SHA=shasum
else
	# Refusing beats installing an unverified binary from the internet.
	die "no sha256sum or shasum: refusing to install without checksum verification"
fi

sha256_of() {
	case "$SHA" in
		sha256sum) sha256sum "$1" ;;
		shasum)    shasum -a 256 "$1" ;;
	esac | cut -d' ' -f1
}

# fetch URL FILE: exit non-zero on any HTTP error, never write a partial file
# that the checksum would then have to catch.
fetch() {
	case "$DL" in
		curl) curl -fsSL --retry 2 -o "$2" "$1" ;;
		wget) wget -q -O "$2" "$1" ;;
	esac
}

# ------------------------------------------------------------------ platform

os=$(uname -s)
case "$os" in
	Linux)  os=linux ;;
	Darwin) os=darwin ;;
	*)      die "unsupported operating system: $os (linux and darwin only; on Windows use install.ps1)" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64|amd64)  arch=amd64 ;;
	aarch64|arm64) arch=arm64 ;;
	*)             die "unsupported architecture: $arch (amd64 and arm64 only)" ;;
esac

# ------------------------------------------------------------------- version
#
# The releases/latest redirect is the cheap way to ask "what is current":
# unauthenticated GitHub API calls are rate limited per IP, and a shared NAT
# burns that budget long before a human notices.

resolve_latest() {
	url=""
	case "$DL" in
		curl) url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "$LATEST_URL" || true) ;;
		wget) url=$(wget -q -S --max-redirect=0 -O /dev/null "$LATEST_URL" 2>&1 \
			| sed -n 's/^[[:space:]]*[Ll]ocation:[[:space:]]*//p' | tr -d '\r' | head -1) ;;
	esac
	case "$url" in
		*/releases/tag/*) printf '%s\n' "${url##*/releases/tag/}" ;;
		*)                return 1 ;;
	esac
}

if [ -z "$VERSION" ]; then
	say "resolving the latest release"
	VERSION=$(resolve_latest) || die "could not resolve the latest release from $LATEST_URL
    Pass one explicitly: --version v0.24.0"
	[ -n "$VERSION" ] || die "the latest-release redirect gave no tag: pass --version"
fi

# The tag carries a v, the version stamped into the binary and the archive
# name does not. Accept either spelling from the user and derive both.
TAG="v${VERSION#v}"
SEMVER="${VERSION#v}"

ARCHIVE="figaro_${SEMVER}_${os}_${arch}.tar.gz"
ARCHIVE_URL="$BASE_URL/$TAG/$ARCHIVE"
SUMS_URL="$BASE_URL/$TAG/checksums.txt"

say "figaro $TAG ($os/$arch) -> $INSTALL_DIR"

# ------------------------------------------------------------------ download

tmp=$(mktemp -d "${TMPDIR:-/tmp}/figaro-install.XXXXXX") || die "mktemp failed"
trap 'rm -rf "$tmp"' EXIT INT TERM HUP

note "$ARCHIVE_URL"
fetch "$ARCHIVE_URL" "$tmp/$ARCHIVE" \
	|| die "download failed: $ARCHIVE_URL
    Check that $TAG exists and ships a $os/$arch archive."

note "$SUMS_URL"
fetch "$SUMS_URL" "$tmp/checksums.txt" \
	|| die "download failed: $SUMS_URL
    The release exists but has no checksums.txt: refusing to install unverified."

want=$(awk -v f="$ARCHIVE" '$2 == f || $2 == "*" f { print $1; exit }' "$tmp/checksums.txt")
[ -n "$want" ] || die "$ARCHIVE is not listed in checksums.txt: refusing to install"
got=$(sha256_of "$tmp/$ARCHIVE")
[ "$want" = "$got" ] || die "checksum mismatch for $ARCHIVE
    expected $want
    got      $got
    Do not run the downloaded file. Re-run the install; if it fails again,
    the asset or your connection is not to be trusted."
say "sha256 ok"

tar -xzf "$tmp/$ARCHIVE" -C "$tmp" || die "could not unpack $ARCHIVE"
[ -f "$tmp/figaro" ] || die "$ARCHIVE contains no figaro binary"

# ------------------------------------------------------------------- install

mkdir -p "$INSTALL_DIR" || die "cannot create $INSTALL_DIR"
[ -w "$INSTALL_DIR" ] || die "$INSTALL_DIR is not writable by you.
    Either pick another with --dir, or run this as a user who owns it."

# Write beside the target and rename. Overwriting a binary in place gives
# ETXTBSY (or, worse, corrupts the image of a running daemon); rename(2)
# swaps the directory entry and leaves the running process on its old inode.
staged="$INSTALL_DIR/.figaro.$$.new"
cp "$tmp/figaro" "$staged" || die "cannot write to $INSTALL_DIR"
chmod 755 "$staged"
mv -f "$staged" "$INSTALL_DIR/figaro" || { rm -f "$staged"; die "could not move the binary into $INSTALL_DIR"; }

# `fig` is a pure rename, not an argv rewrite: cmd/figaro/main.go dispatches
# on filepath.Base(os.Args[0]), so usage and completions come out under the
# name that was typed. Relative target so moving the directory keeps it valid.
if ! ln -sfn figaro "$INSTALL_DIR/fig" 2>/dev/null; then
	if cp -f "$INSTALL_DIR/figaro" "$INSTALL_DIR/fig" 2>/dev/null; then
		warn "could not symlink fig; installed a copy instead"
	else
		warn "could not install the fig alias"
	fi
fi

if [ "$os" = darwin ]; then
	# Gatekeeper kills a quarantined unsigned binary without a useful message.
	# Best effort: a system without xattr has nothing to strip anyway.
	xattr -d com.apple.quarantine "$INSTALL_DIR/figaro" 2>/dev/null || true
fi

# The channel marker. internal/update reads this file (update.ChannelMarker,
# ".figaro-channel") from beside the binary to learn how figaro got here, and
# on `script` it tells the user to re-run
#   curl -fsSL https://figar.org/install.sh | sh
# rather than guessing. ~/.local/bin is a shared drawer: a brew-installed or
# nix-installed figaro sitting in a different directory must not be "upgraded"
# by a command that would damage it. The recognised words are script,
# homebrew, go-install and nix; anything else is ignored, so do not invent one.
# Written by rename for the same reason the binary is: never a half-written
# marker next to a good binary.
marker_tmp="$INSTALL_DIR/.figaro-channel.$$.new"
if printf 'script\n' >"$marker_tmp" 2>/dev/null; then
	mv -f "$marker_tmp" "$INSTALL_DIR/.figaro-channel" 2>/dev/null \
		|| { rm -f "$marker_tmp"; warn "could not write $INSTALL_DIR/.figaro-channel: figaro update will not know how you installed"; }
else
	rm -f "$marker_tmp" 2>/dev/null || true
	warn "could not write $INSTALL_DIR/.figaro-channel: figaro update will not know how you installed"
fi

# --------------------------------------------------------------- completions
#
# Generated by running the binary we just installed, so they can never
# describe a different version than the one on disk. Entirely best effort:
# a failure here costs tab completion, not the install.

data_home="${XDG_DATA_HOME:-$HOME/.local/share}"
config_home="${XDG_CONFIG_HOME:-$HOME/.config}"

gen_completion() {
	name="$1"; shell="$2"; dest="$3"
	mkdir -p "$(dirname "$dest")" 2>/dev/null || return 1
	"$INSTALL_DIR/$name" completion "$shell" >"$dest.tmp" 2>/dev/null || { rm -f "$dest.tmp"; return 1; }
	mv -f "$dest.tmp" "$dest" 2>/dev/null || { rm -f "$dest.tmp"; return 1; }
}

completions=0
for name in figaro fig; do
	[ -e "$INSTALL_DIR/$name" ] || continue
	gen_completion "$name" bash "$data_home/bash-completion/completions/$name"      && completions=1 || true
	gen_completion "$name" zsh  "$data_home/zsh/site-functions/_$name"              && completions=1 || true
	gen_completion "$name" fish "$config_home/fish/completions/$name.fish"          && completions=1 || true
done
[ "$completions" = 1 ] || warn "could not generate shell completions (the binary still works)"

# -------------------------------------------------------------------- report

installed=$("$INSTALL_DIR/figaro" --version 2>/dev/null | head -1) \
	|| die "installed $INSTALL_DIR/figaro but it will not run.
    Wrong architecture, or a corrupt download."
[ -n "$installed" ] || die "installed $INSTALL_DIR/figaro but it printed no version"

say "installed $installed"
note "$INSTALL_DIR/figaro"
note "$INSTALL_DIR/fig"

# The skills are embedded in the binary, so a truncated or mismatched artifact
# can still print a version and still be useless. `doctor skills` lists what
# this binary actually carries, which is the cheapest proof that the thing on
# disk is whole. A release older than the command has no way to answer, so a
# failure here is a warning and not a refusal.
if ! "$INSTALL_DIR/figaro" doctor skills >/dev/null 2>&1; then
	warn "\`figaro doctor skills\` did not succeed. Either this release predates
    the command, or the binary is incomplete. Check it by hand:
        $INSTALL_DIR/figaro doctor skills"
fi

case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*)
		warn "$INSTALL_DIR is not on your PATH. Add it:"
		printf '\n' >&2
		printf '   bash  %s\n' "echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.bashrc" >&2
		printf '   zsh   %s\n' "echo 'export PATH=\"$INSTALL_DIR:\$PATH\"' >> ~/.zshrc" >&2
		printf '   fish  %s\n' "fish_add_path $INSTALL_DIR" >&2
		printf '\n' >&2
		;;
esac

# An angelus started by the OLD binary keeps serving until it is told to
# stop: the new file on disk changes nothing for a process already running.
# Same resolution order as internal/cli/angelus_client.go.
if [ -n "${FIGARO_RUNTIME_DIR:-}" ]; then
	runtime_dir="$FIGARO_RUNTIME_DIR"
elif [ -n "${XDG_RUNTIME_DIR:-}" ]; then
	runtime_dir="$XDG_RUNTIME_DIR/figaro"
else
	runtime_dir="${TMPDIR:-/tmp}/figaro"
fi
if [ -e "$runtime_dir/angelus.sock" ]; then
	warn "a figaro daemon is already running (socket at $runtime_dir/angelus.sock)."
	note "It is still the old binary. From a terminal, NOT from inside an aria:"
	note "    figaro stop"
fi

cat >&2 <<EOF

${B}figaro is installed.${R} Also reachable as ${B}fig${R}.

  figaro login anthropic      # or: figaro login copilot
  figaro -- buongiorno
EOF
