#!/bin/sh
# OpenSignage one-line installer: downloads the latest release from GitHub, checks its
# signature and runs the installer of the package.
#
#   curl -fsSL https://raw.githubusercontent.com/testbild-media/OpenSignage/main/install.sh | sh -s -- server
#   curl -fsSL https://raw.githubusercontent.com/testbild-media/OpenSignage/main/install.sh | sh -s -- player --name "Bar 1"
#   (without curl: wget -qO- <same URL> | sh -s -- server)
#
# Roles: server (install-server.sh) | player (install-client.sh). Options of this script:
#   --version X       install release X instead of the newest one (e.g. 1.2.0)
#   --repo OWNER/NAME take the release from another GitHub repository
#   --download-only   download and verify only, print the unpacked directory
# Every other option goes to the installer of the package (e.g. --name, --server, --admin).
#
# The whole script is one function that runs at the end, so a download cut off half-way
# never executes a partial script.

main() {
	set -eu
	REPO=testbild-media/OpenSignage
	VERSION=""
	ROLE=""
	DOWNLOAD_ONLY=0
	# Trusted release key (the same the server and the agents trust, internal/common/release).
	PUBKEY='-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEA5w7fufvnSzj/99bUODdFjp8l1r28QS9ay8l/9RkRgco=
-----END PUBLIC KEY-----'

	say() { printf '==> %s\n' "$*"; }
	die() {
		printf 'ERROR: %s\n' "$*" >&2
		exit 1
	}

	# ---- arguments: role and our own options; the rest goes to the package installer
	n=$#
	i=0
	while [ "$i" -lt "$n" ]; do
		a=$1
		shift
		i=$((i + 1))
		case "$a" in
		server | player | client)
			if [ -z "$ROLE" ]; then ROLE=$a; else set -- "$@" "$a"; fi
			;;
		--version)
			[ "$i" -lt "$n" ] || die "--version needs a value"
			VERSION=${1#v}
			shift
			i=$((i + 1))
			;;
		--repo)
			[ "$i" -lt "$n" ] || die "--repo needs a value"
			REPO=$1
			shift
			i=$((i + 1))
			;;
		--download-only) DOWNLOAD_ONLY=1 ;;
		-h | --help)
			sed -n '2,15p' "$0" 2>/dev/null | sed 's/^# \{0,1\}//' || true
			echo "Usage: install.sh server|player [--version X] [--repo OWNER/NAME] [installer options]"
			exit 0
			;;
		*) set -- "$@" "$a" ;;
		esac
	done
	TTY=""
	if [ -r /dev/tty ] && (: </dev/tty) 2>/dev/null; then TTY=/dev/tty; fi
	if [ -z "$ROLE" ] && [ "$DOWNLOAD_ONLY" -eq 0 ]; then
		[ -n "$TTY" ] || die "say what to install: … | sh -s -- server   or   … | sh -s -- player"
		printf 'Install the OpenSignage server or a player (screen)? [server/player]: ' >/dev/tty
		read -r ROLE </dev/tty || ROLE=""
	fi
	case "$ROLE" in
	server) SCRIPT=install-server.sh ;;
	player | client) SCRIPT=install-client.sh ;;
	"") [ "$DOWNLOAD_ONLY" -eq 1 ] || die "unknown role: choose server or player" ;;
	*) die "unknown role \"$ROLE\": choose server or player" ;;
	esac

	[ "$(uname -s)" = Linux ] || die "OpenSignage installs on Linux (Debian, Ubuntu, Raspberry Pi OS)"
	SUDO=""
	if [ "$(id -u)" -ne 0 ]; then
		command -v sudo >/dev/null 2>&1 || die "run as root or install sudo"
		SUDO=sudo
	fi

	# ---- platform: the userland decides (32-bit Raspberry Pi OS on a 64-bit Pi is armv7)
	ARCH=$(dpkg --print-architecture 2>/dev/null || uname -m)
	case "$ARCH" in
	amd64 | x86_64) ARCH=amd64 ;;
	arm64 | aarch64) ARCH=arm64 ;;
	armhf | armv7l | armv8l) ARCH=armv7 ;;
	*) die "no OpenSignage package for this platform ($ARCH)" ;;
	esac

	# ---- tools
	if command -v curl >/dev/null 2>&1; then
		fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
		fetch_stdout() { curl -fsSL --retry 3 "$1"; }
	elif command -v wget >/dev/null 2>&1; then
		fetch() { wget -q -O "$2" "$1"; }
		fetch_stdout() { wget -q -O- "$1"; }
	else
		die "curl or wget is needed"
	fi
	if ! command -v openssl >/dev/null 2>&1; then
		say "installing openssl (to check the release signature)"
		$SUDO apt-get install -y -qq openssl >/dev/null || die "openssl is needed to check the release signature"
	fi
	for t in sha256sum base64 tar; do
		command -v "$t" >/dev/null 2>&1 || die "$t is needed"
	done

	# ---- release: the newest one (pre-releases included) or the one asked for
	if [ -z "$VERSION" ]; then
		say "looking for the newest release of $REPO"
		VERSION=$(fetch_stdout "https://api.github.com/repos/$REPO/releases?per_page=10" |
			tr ',' '\n' | sed -n 's/^ *"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1) || true
		[ -n "$VERSION" ] || die "no release found in https://github.com/$REPO/releases"
	fi
	PKG="opensignage-$VERSION-linux-$ARCH.tar.gz"
	BASE=${OPENSIGNAGE_RELEASE_URL:-https://github.com/$REPO/releases/download/v$VERSION}

	WORK=$(mktemp -d /tmp/opensignage-install.XXXXXX)
	trap 'rm -rf "$WORK"' EXIT INT TERM
	chmod 755 "$WORK"
	say "downloading OpenSignage $VERSION ($ARCH)"
	fetch "$BASE/SHA256SUMS" "$WORK/SHA256SUMS" || die "release $VERSION not found ($BASE)"
	fetch "$BASE/SHA256SUMS.sig" "$WORK/SHA256SUMS.sig" || die "release $VERSION has no signature"
	fetch "$BASE/$PKG" "$WORK/$PKG" || die "download of $PKG failed"

	# ---- signature (Ed25519 over SHA256SUMS) and checksum of the package
	printf '%s\n' "$PUBKEY" >"$WORK/release.pub"
	tr -d ' \r\n' <"$WORK/SHA256SUMS.sig" | base64 -d >"$WORK/sig.bin" 2>/dev/null || die "signature unreadable"
	openssl pkeyutl -verify -pubin -inkey "$WORK/release.pub" -rawin -in "$WORK/SHA256SUMS" \
		-sigfile "$WORK/sig.bin" >/dev/null 2>&1 || die "SIGNATURE INVALID – the release was not signed by the OpenSignage key. Nothing was installed."
	want=$(awk -v f="$PKG" '$2 == f {print $1}' "$WORK/SHA256SUMS")
	[ -n "$want" ] || die "$PKG is not listed in the signed checksums"
	got=$(sha256sum "$WORK/$PKG" | cut -d' ' -f1)
	[ "$want" = "$got" ] || die "checksum of $PKG does not match the signed list. Nothing was installed."
	say "signature and checksum verified"

	tar -xzf "$WORK/$PKG" -C "$WORK"
	DIR="$WORK/opensignage-$VERSION-linux-$ARCH"
	[ -d "$DIR" ] || die "unexpected package layout"
	if [ "$DOWNLOAD_ONLY" -eq 1 ]; then
		KEEP=$(mktemp -d /tmp/opensignage-$VERSION.XXXXXX)
		mv "$DIR" "$KEEP/"
		echo "$KEEP/opensignage-$VERSION-linux-$ARCH"
		return 0
	fi

	say "running $SCRIPT"
	cd "$DIR"
	# The installer may ask questions (e.g. the player name): from the terminal, never from
	# the pipe this script came through.
	if [ -n "$TTY" ]; then
		$SUDO sh "./$SCRIPT" "$@" </dev/tty
	else
		$SUDO sh "./$SCRIPT" "$@" </dev/null
	fi
}

main "$@"
