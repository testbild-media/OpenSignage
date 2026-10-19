#!/bin/sh
# OpenSignage client installer for Debian-based Linux (Debian 12/13, Raspberry Pi OS
# Lite/Desktop Bookworm/Trixie). Checks capabilities, not versions.
#
# Usage: sudo ./install.sh [--server HOST[:PORT]] [--webgui-password PW] [--binary PATH]
#                          [--trusted-key osk1:KEY ...] [--no-video-drivers] [--no-gpu-memory]
#                          [--no-force-hdmi]
#
# Every system change is recorded in /var/lib/opensignage-agent-install/install-manifest
# (root only) so uninstall.sh can revert all of them. Re-running the installer upgrades
# in place.
#
# Ownership: the release tree /opt/opensignage-agent belongs to the service user because
# the agent installs updates itself (releases/<v>, current, previous). Root never
# executes anything from it: the services run as the service user, and the only
# privileged commands (systemctl in the rollback unit) are root-owned system binaries.
set -eu

PREFIX=/opt/opensignage-agent
LIBDIR=/usr/lib/opensignage-agent
CONFDIR=/etc/opensignage
CONF=$CONFDIR/agent.env
DATA=/var/lib/opensignage-agent
STATE=/var/lib/opensignage-agent-install
MANIFEST=$STATE/install-manifest
BACKUP=$STATE/backup
UNITDIR=/etc/systemd/system
SUDOERS=/etc/sudoers.d/opensignage-agent
XWRAPPER=/etc/X11/Xwrapper.config
SVCUSER=opensignage
UNITS="opensignage-display.service opensignage-agent.service opensignage-agent-rollback.service"

SERVER=""
NAME=""
WEBGUI_PW=""
BINARY=""
TRUSTED_KEYS=""
VIDEO_DRIVERS=1
GPU_MEMORY=1
FORCE_HDMI=1

say()  { printf '%s\n' "==> $*"; }
warn() { printf '%s\n' "WARNING: $*" >&2; }
die()  { printf '%s\n' "ERROR: $*" >&2; exit 1; }

usage() {
	cat <<EOF
Usage: sudo $0 [options]
  --server HOST[:PORT]     manual server address (default: automatic discovery)
  --name NAME              name of this player, e.g. "Bar left": the host name becomes
                           osp-bar-left (Open Signage Player) and the name is proposed
                           when the device is taken over in the admin WebGUI. Asked for
                           when run in a terminal; an update keeps the name.
  --webgui-password PW     client WebGUI password (default: opensignage, change forced)
  --binary PATH            opensignage-agent binary (default: from the package)
  --trusted-key osk1:KEY   release signing key trusted in addition to the built-in ones
                           (repeatable; written to OS_TRUSTED_KEYS in agent.env). Keys
                           added in the server's settings are not trusted by devices.
  --no-video-drivers       do not install the VA-API/Vulkan drivers for hardware video
                           decoding on Intel/AMD graphics (installed when the
                           distribution has them; used when the device setting
                           "hardware video decoding" is "on" or "auto")
  --no-gpu-memory          leave the GPU memory pool of a Raspberry Pi 2/3 as it is
                           (default: raised to 384 MB in config.txt, see below)
  --no-force-hdmi          do not switch the HDMI output of a Raspberry Pi on for good
                           (default: on in cmdline.txt, for TVs that do not report
                           themselves, see below)
  -h, --help               this help
EOF
}

# add_key KEY: validate a release key (osk1:<base64 of 32 bytes>) and remember it.
add_key() {
	k=${1#osk1:}
	case "$1" in osk1:*) ;; *) die "invalid release key: $1 (expected osk1:…)" ;; esac
	case "$k" in *[!A-Za-z0-9+/=]*) die "invalid release key: $1" ;; esac
	[ "${#k}" -eq 44 ] || die "invalid release key: $1"
	TRUSTED_KEYS=${TRUSTED_KEYS:+$TRUSTED_KEYS,}$1
}

while [ $# -gt 0 ]; do
	case "$1" in
	--server) [ $# -ge 2 ] || die "--server needs a value"; SERVER=$2; shift 2 ;;
	--server=*) SERVER=${1#*=}; shift ;;
	--name) [ $# -ge 2 ] || die "--name needs a value"; NAME=$2; shift 2 ;;
	--name=*) NAME=${1#*=}; shift ;;
	--webgui-password) [ $# -ge 2 ] || die "--webgui-password needs a value"; WEBGUI_PW=$2; shift 2 ;;
	--webgui-password=*) WEBGUI_PW=${1#*=}; shift ;;
	--binary) [ $# -ge 2 ] || die "--binary needs a value"; BINARY=$2; shift 2 ;;
	--binary=*) BINARY=${1#*=}; shift ;;
	--trusted-key) [ $# -ge 2 ] || die "--trusted-key needs a value"; add_key "$2"; shift 2 ;;
	--trusted-key=*) add_key "${1#*=}"; shift ;;
	--no-video-drivers) VIDEO_DRIVERS=0; shift ;;
	--no-gpu-memory) GPU_MEMORY=0; shift ;;
	--no-force-hdmi) FORCE_HDMI=0; shift ;;
	-h|--help) usage; exit 0 ;;
	*) usage >&2; die "unknown option: $1" ;;
	esac
done

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

# ---------------------------------------------------------------- capability checks

[ "$(id -u)" -eq 0 ] || die "please run as root (sudo $0)"
[ -d /run/systemd/system ] || die "systemd is required (not running as init)"
command -v apt-get >/dev/null 2>&1 || die "apt-get is required (Debian-based system)"
command -v dpkg >/dev/null 2>&1 || die "dpkg is required"
command -v runuser >/dev/null 2>&1 || die "runuser (util-linux) is required"

ARCH=$(dpkg --print-architecture)
case "$ARCH" in
amd64|arm64|armhf) ;;
*) die "unsupported architecture: $ARCH (supported: amd64, arm64, armhf)" ;;
esac

if [ -z "$BINARY" ]; then
	for c in "$SCRIPT_DIR/opensignage-agent" "$SCRIPT_DIR/../../../bin/opensignage-agent" "$SCRIPT_DIR/../../bin/opensignage-agent" "$SCRIPT_DIR/../bin/opensignage-agent"; do
		if [ -f "$c" ]; then BINARY=$c; break; fi
	done
fi
[ -n "$BINARY" ] && [ -f "$BINARY" ] || die "opensignage-agent binary not found (use --binary PATH)"
chmod 0755 "$BINARY" 2>/dev/null || true
VERSION_LINE=$("$BINARY" version 2>/dev/null) || die "$BINARY does not run on this system (wrong architecture?)"
VERSION=$(printf '%s\n' "$VERSION_LINE" | awk '{print $2}')
case "$VERSION" in
''|*/*|*..*|*[!A-Za-z0-9.+-]*) die "cannot determine the agent version from: $VERSION_LINE" ;;
esac
PKGROOT=$(cd "$(dirname "$BINARY")/.." && pwd)

if [ -n "$SERVER" ]; then
	case "$SERVER" in
	*[!A-Za-z0-9.:_\[\]-]*) die "invalid server address: $SERVER" ;;
	esac
fi
if [ -n "$WEBGUI_PW" ] && [ "${#WEBGUI_PW}" -lt 8 ]; then
	die "the WebGUI password must have at least 8 characters"
fi

# Player name → host name osp-<label>: lower case a–z, 0–9 and hyphens; umlauts and other
# letters with diacritics are spelled in plain letters (ä → ae, é → e).
osp_label() {
	printf '%s' "$1" |
		sed 's/Ä/ae/g; s/Ö/oe/g; s/Ü/ue/g; s/ä/ae/g; s/ö/oe/g; s/ü/ue/g; s/ß/ss/g' |
		{ LC_ALL=C.UTF-8 iconv -f UTF-8 -t ASCII//TRANSLIT 2>/dev/null || cat; } |
		tr 'A-Z' 'a-z' | tr -c 'a-z0-9' '-' | tr -s '-' | sed 's/^-//; s/-$//' | cut -c1-59 | sed 's/-$//'
}
name_ok() {
	case "$1" in *[[:cntrl:]]*) return 1 ;; esac
	printf '%s' "$1" | iconv -f UTF-8 -t UTF-8 >/dev/null 2>&1 || return 1 # terminal not in UTF-8
	[ "$(printf '%s' "$1" | LC_ALL=C.UTF-8 wc -m)" -le 64 ] || return 1
	printf '%s' "$(osp_label "$1")" | grep -q '[a-z]'
}
CUR_NAME=""
if [ -f "$CONF" ]; then
	CUR_NAME=$(sed -n 's/^OS_PLAYER_NAME=//p' "$CONF" | tail -n 1)
fi
if [ -z "$NAME" ] && [ -z "$CUR_NAME" ] && [ -t 0 ]; then
	while :; do
		printf 'Name of this player (e.g. "Bar left"; host name becomes osp-<name>, Enter = keep "%s"): ' "$(hostname)"
		read -r NAME || NAME=""
		[ -z "$NAME" ] && break
		name_ok "$NAME" && break
		echo "Please use at most 64 characters (UTF-8) with at least one letter a–z."
	done
fi
if [ -n "$NAME" ] && ! name_ok "$NAME"; then
	die "invalid --name: at most 64 characters (UTF-8) with at least one letter a–z"
fi

free_kb() { df -Pk "$1" 2>/dev/null | awk 'NR==2 {print $4}'; }
FREE_ROOT=$(free_kb /)
[ -n "$FREE_ROOT" ] && [ "$FREE_ROOT" -ge 1048576 ] || die "at least 1 GB free disk space is required on / (have ${FREE_ROOT:-?} KB)"

say "OpenSignage agent $VERSION for $ARCH"

# ---------------------------------------------------------------- manifest (root only)

umask 022
mkdir -p "$STATE" "$BACKUP"
chown root:root "$STATE" "$BACKUP"
chmod 0700 "$STATE" "$BACKUP"
# Migrate the manifest of older installers (it lived in the service user's data dir).
if [ ! -f "$MANIFEST" ] && [ -f "$DATA/install-manifest" ] && [ ! -L "$DATA/install-manifest" ]; then
	cp "$DATA/install-manifest" "$MANIFEST"
	if [ -f "$DATA/backup/Xwrapper.config" ] && [ ! -L "$DATA/backup/Xwrapper.config" ]; then
		cp "$DATA/backup/Xwrapper.config" "$BACKUP/Xwrapper.config"
	fi
	rm -rf "$DATA/install-manifest" "$DATA/backup"
fi
touch "$MANIFEST"
chmod 0600 "$MANIFEST"

# record LINE: append a manifest entry once.
record() {
	grep -qxF -- "$*" "$MANIFEST" 2>/dev/null || printf '%s\n' "$*" >>"$MANIFEST"
}
recorded() { grep -qxF -- "$*" "$MANIFEST" 2>/dev/null; }
record "dir $STATE"

# ---------------------------------------------------------------- packages

say "Installing packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -q || warn "apt-get update failed, trying with the existing package lists"

CHROMIUM_PKG=chromium
if ! apt-cache show --no-all-versions chromium >/dev/null 2>&1; then
	if apt-cache show --no-all-versions chromium-browser >/dev/null 2>&1; then
		CHROMIUM_PKG=chromium-browser
	else
		die "no chromium package available in the configured apt sources"
	fi
fi
PKGS="xserver-xorg-core xserver-xorg-video-fbdev xserver-xorg-legacy xinit x11-xserver-utils $CHROMIUM_PKG fonts-noto-color-emoji fonts-dejavu-core sudo"
# Wireless networks (network settings of the client WebGUI): Raspberry Pi OS has them already;
# on Debian a USB stick plugged in later works without installing anything.
PKGS="$PKGS wpasupplicant iw wireless-regdb"
# cvt (forced display modes) moved from xserver-xorg-core into its own package.
if apt-cache show --no-all-versions xcvt >/dev/null 2>&1; then PKGS="$PKGS xcvt"; fi

pkg_installed() { dpkg-query -W -f='${Status}' "$1" 2>/dev/null | grep -q 'install ok installed'; }
installed_list() { dpkg-query -W -f='${Status}|${Package}\n' 2>/dev/null | awk -F'|' '$1 == "install ok installed" {print $2}' | sort -u; }
pkg_available() { apt-cache show --no-all-versions "$1" >/dev/null 2>&1; }

# install_pkgs required|optional PKG...: install the missing packages and record every newly
# installed package (incl. dependencies) for an exact revert. Optional packages only warn
# when apt fails.
install_pkgs() {
	mode=$1; shift
	new=""
	for p in "$@"; do
		if ! pkg_installed "$p"; then new="$new $p"; fi
	done
	if [ -z "$new" ]; then
		[ "$mode" = optional ] || say "All packages already installed"
		return 0
	fi
	before=$(mktemp)
	installed_list >"$before"
	ok=1
	# shellcheck disable=SC2086
	apt-get install -y --no-install-recommends $new || ok=0
	for p in $(installed_list | comm -13 "$before" -); do record "pkg $p"; done
	rm -f "$before"
	if [ "$ok" -eq 0 ]; then
		[ "$mode" = optional ] || die "package installation failed:$new"
		warn "optional packages could not be installed:$new"
	fi
}
# shellcheck disable=SC2086
install_pkgs required $PKGS

# Hardware video decoding on Intel/AMD (device setting "on"): Chromium decodes with VA-API
# and composites with Vulkan, so it needs both drivers. Raspberry Pi OS needs nothing: its
# Chromium decodes with the kernel's V4L2 codec driver.
gpu_driver() {
	for u in /sys/class/drm/renderD*/device/uevent; do
		[ -f "$u" ] || continue
		d=$(sed -n 's/^DRIVER=//p' "$u")
		case "$d" in i915|xe|amdgpu) echo "$d"; return ;; esac
	done
}
if [ "$VIDEO_DRIVERS" -eq 1 ] && [ "$ARCH" = amd64 ]; then
	case "$(gpu_driver)" in
	i915|xe) CAND="intel-media-va-driver i965-va-driver mesa-vulkan-drivers" ;;
	amdgpu) CAND="mesa-va-drivers mesa-vulkan-drivers" ;;
	*) CAND="" ;;
	esac
	VIDEO_PKGS=""
	for p in $CAND; do
		# Keep a non-free variant the admin installed (it conflicts with the free one).
		if pkg_installed "$p-non-free"; then continue; fi
		if pkg_available "$p"; then VIDEO_PKGS="$VIDEO_PKGS $p"; fi
	done
	if [ -n "$VIDEO_PKGS" ]; then
		say "Installing video drivers for hardware decoding:$VIDEO_PKGS"
		# shellcheck disable=SC2086
		install_pkgs optional $VIDEO_PKGS
	fi
fi

# Network firmware: Wi-Fi chips (and some wired ones) need firmware from Debian's
# non-free-firmware component. The Debian installer enables it when the hardware needs it;
# here it is enabled as well (saved for uninstall) and the firmware the kernel misses is
# installed. Raspberry Pi OS ships its firmware.
is_raspios() { [ -f /etc/rpi-issue ]; }
enable_nonfree_firmware() {
	changed=0
	f=/etc/apt/sources.list.d/debian.sources
	if [ -f "$f" ] && grep -q '^Components:' "$f" && grep '^Components:' "$f" | grep -vq 'non-free-firmware'; then
		if ! recorded "restore $f"; then cp -p "$f" "$BACKUP/debian.sources"; record "restore $f"; fi
		sed -i '/^Components:/{/non-free-firmware/!s/$/ non-free-firmware/}' "$f"
		changed=1
	fi
	f=/etc/apt/sources.list
	if [ -f "$f" ] && grep -Eq '^deb(-src)?[[:space:]].*(deb|security|ftp\.[a-z]+)\.debian\.org' "$f" &&
		grep -E '^deb(-src)?[[:space:]].*(deb|security|ftp\.[a-z]+)\.debian\.org' "$f" | grep -vq 'non-free-firmware'; then
		if ! recorded "restore $f"; then cp -p "$f" "$BACKUP/sources.list"; record "restore $f"; fi
		sed -i -E '/^deb(-src)?[[:space:]].*(deb|security|ftp\.[a-z]+)\.debian\.org/{/non-free-firmware/!s/$/ non-free-firmware/}' "$f"
		changed=1
	fi
	if [ "$changed" -eq 1 ]; then
		say "Enabled the non-free-firmware component of the Debian sources"
		apt-get update -q || warn "apt-get update failed"
	fi
}
# missing_net_firmware: the packages with network firmware the kernel could not load.
missing_net_firmware() {
	dmesg 2>/dev/null | sed -n 's/.*Direct firmware load for \([^ ]*\) failed.*/\1/p' | sort -u | while read -r f; do
		if [ -e "/lib/firmware/$f" ] || [ -e "/lib/firmware/$f.xz" ] || [ -e "/lib/firmware/$f.zst" ]; then continue; fi
		case "$f" in
		iwlwifi-*) echo firmware-iwlwifi ;;
		rtw88/* | rtw89/* | rtlwifi/* | rtl_nic/*) echo firmware-realtek ;;
		ath9k_htc/* | ath10k/* | ath11k/* | ath12k/* | ar3k/* | ar5* | ar7* | ar9*) echo firmware-atheros ;;
		mediatek/* | mt7*) if pkg_available firmware-mediatek; then echo firmware-mediatek; else echo firmware-misc-nonfree; fi ;;
		brcm/*) echo firmware-brcm80211 ;;
		libertas/* | mrvl/*) echo firmware-libertas ;;
		ti-connectivity/*) echo firmware-ti-connectivity ;;
		esac
	done | sort -u
}
if ! is_raspios; then
	enable_nonfree_firmware
	FW_PKGS=$(missing_net_firmware | tr '\n' ' ')
	if [ -n "$FW_PKGS" ]; then
		say "Installing network firmware: $FW_PKGS"
		# shellcheck disable=SC2086
		install_pkgs optional $FW_PKGS
		warn "network firmware installed: restart the device (or plug the adapter in again) to load it"
	fi
fi

# Raspberry Pi 2/3 (VideoCore IV): the GPU has no MMU, so every graphics and video buffer
# comes from the contiguous memory pool (CMA). Raspberry Pi OS reserves 256 MB; a 1080p
# output with 720p video leaves about 30 MB, and a mode switch or a screenshot then fails to
# allocate and the video decoder errors out. Measured on a Pi 3 at 1080p with video,
# screenshots and plasma regeneration: up to 288 MB in use. Boards with 1 GB get 384 MB;
# pool memory the GPU does not use stays available to programs (at least 400 MB were
# always available). Takes effect after the next reboot; uninstall.sh restores the line.
PI_CMA=384
REBOOT_NEEDED=0
pi_model() { if [ -r /proc/device-tree/model ]; then tr -d '\000' </proc/device-tree/model; fi; }
# The Raspberry Pi 2/3 is not supported (it may work with simple content).
UNSUPPORTED_BOARD=""
case "$(pi_model)" in
"Raspberry Pi 3"* | "Raspberry Pi 2"* | "Raspberry Pi Compute Module 3"*) UNSUPPORTED_BOARD=$(pi_model) ;;
esac
unsupported_note() {
	[ -n "$UNSUPPORTED_BOARD" ] || return 0
	warn "$UNSUPPORTED_BOARD is not supported: known issue with its hardware video decoder,"
	warn "which fails about every 15-20 minutes when videos change often (the player recovers"
	warn "by itself). It can work with simple content, but it is not recommended. Use a"
	warn "Raspberry Pi 4 or a small x86 PC; on a Pi 3 use the official 5.1 V / 2.5 A supply."
}
unsupported_note
if [ "$GPU_MEMORY" -eq 1 ]; then
	case "$(pi_model)" in
	"Raspberry Pi 3"*|"Raspberry Pi 2"*|"Raspberry Pi Compute Module 3"*)
		BOOTCFG=""
		for f in /boot/firmware/config.txt /boot/config.txt; do
			if [ -f "$f" ] && [ ! -L "$f" ]; then BOOTCFG=$f; break; fi
		done
		MEM_KB=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
		CMA_KB=$(awk '/^CmaTotal:/ {print $2}' /proc/meminfo)
		CUR=""
		[ -z "$BOOTCFG" ] || CUR=$(sed -n 's/^dtoverlay=vc4-kms-v3d,cma-\([0-9][0-9]*\)$/\1/p' "$BOOTCFG" | head -n1)
		if [ -z "$BOOTCFG" ]; then
			warn "config.txt not found – GPU memory pool left as it is"
		elif [ "${MEM_KB:-0}" -lt 786432 ]; then
			: # 512 MB boards: the pool cannot grow
		elif [ "${CMA_KB:-0}" -ge $((PI_CMA * 1024)) ]; then
			: # already large enough
		elif grep -qx 'dtoverlay=vc4-kms-v3d' "$BOOTCFG"; then
			say "Raising the GPU memory pool to $PI_CMA MB in $BOOTCFG (active after a reboot)"
			sed -i "0,/^dtoverlay=vc4-kms-v3d\$/s//dtoverlay=vc4-kms-v3d,cma-$PI_CMA/" "$BOOTCFG"
			record "bootcma $BOOTCFG $PI_CMA"
			REBOOT_NEEDED=1
		elif [ -n "$CUR" ] && recorded "bootcma $BOOTCFG $CUR"; then
			# Set by an earlier run: raise an older, smaller value; reboot still pending.
			if [ "$CUR" -lt "$PI_CMA" ]; then
				say "Raising the GPU memory pool from $CUR to $PI_CMA MB in $BOOTCFG (active after a reboot)"
				sed -i "0,/^dtoverlay=vc4-kms-v3d,cma-$CUR\$/s//dtoverlay=vc4-kms-v3d,cma-$PI_CMA/" "$BOOTCFG"
				sed -i "\|^bootcma $BOOTCFG $CUR\$|d" "$MANIFEST"
				record "bootcma $BOOTCFG $PI_CMA"
			fi
			REBOOT_NEEDED=1
		else
			warn "$BOOTCFG sets the graphics overlay itself – GPU memory pool left as it is"
		fi
		;;
	esac
fi

# Raspberry Pi: the HDMI output is switched on for good. Many TVs do not report a connected
# screen (hot plug) to the Pi – the boot screen shows, then the kernel switches the output
# off and the screen stays black; the same happens when the TV is switched on after the
# player. The TV still reports its resolutions, so the display page (custom resolutions
# included) works as with any screen. Switched on: the port that reports a screen, else
# HDMI 0 (next to the power supply). Left alone when the boot line sets an HDMI output
# already or a DSI/DPI screen is connected. Takes effect after the next reboot;
# uninstall.sh removes it.
if [ "$FORCE_HDMI" -eq 1 ] && pi_model | grep -q '^Raspberry Pi'; then
	CMDLINE=""
	for f in /boot/firmware/cmdline.txt /boot/cmdline.txt; do
		if [ -f "$f" ] && [ ! -L "$f" ]; then CMDLINE=$f; break; fi
	done
	# drm_connected NAME: a connector NAME (glob) reports a connected screen.
	drm_connected() { cat /sys/class/drm/card*-$1/status 2>/dev/null | grep -qx connected; }
	if [ -z "$CMDLINE" ]; then
		warn "cmdline.txt not found – HDMI output left as it is"
	elif grep -q 'video=HDMI-A-' "$CMDLINE"; then
		: # set already (by an earlier run or by hand)
	elif drm_connected 'DSI-*' || drm_connected 'DPI-*'; then
		: # a DSI/DPI screen: a second, forced output would take its place
	else
		HDMI_PORT=HDMI-A-1
		if drm_connected HDMI-A-2 && ! drm_connected HDMI-A-1; then
			HDMI_PORT=HDMI-A-2
		fi
		say "Switching the HDMI output $HDMI_PORT on for good in $CMDLINE (active after a reboot)"
		sed -i "1 s/\$/ video=$HDMI_PORT:D/" "$CMDLINE"
		record "hdmiforce $CMDLINE $HDMI_PORT"
		REBOOT_NEEDED=1
	fi
fi

# ---------------------------------------------------------------- service user

if ! id "$SVCUSER" >/dev/null 2>&1; then
	say "Creating system user $SVCUSER"
	useradd --system --home-dir "$DATA" --no-create-home --shell /usr/sbin/nologin --user-group "$SVCUSER"
	record "user $SVCUSER"
fi
for g in video render input audio tty; do
	if getent group "$g" >/dev/null 2>&1; then
		if ! id -nG "$SVCUSER" | tr ' ' '\n' | grep -qx "$g"; then
			usermod -aG "$g" "$SVCUSER"
			record "group $SVCUSER $g"
		fi
	fi
done
as_user() { runuser -u "$SVCUSER" -- "$@"; }

[ -d "$DATA" ] || record "dir $DATA"
mkdir -p "$DATA"
chown "$SVCUSER:$SVCUSER" "$DATA"
chmod 0750 "$DATA"
record "dir $DATA"

# ---------------------------------------------------------------- release files

say "Installing release $VERSION to $PREFIX/releases/$VERSION"
if [ ! -d "$PREFIX" ]; then
	mkdir -p "$PREFIX"
	record "dir $PREFIX"
fi
# The agent updates itself, so the release tree belongs to the service user.
if [ "$(stat -c %U "$PREFIX")" != "$SVCUSER" ]; then
	chown -R "$SVCUSER:$SVCUSER" "$PREFIX"
fi
chmod 0755 "$PREFIX"
# Stage in a root-owned directory the service user can read, then copy as that user,
# so root never writes into (or follows links inside) the user-owned tree.
STAGE=$(mktemp -d /tmp/opensignage-stage.XXXXXX)
trap 'rm -rf "$STAGE"' EXIT
chmod 0755 "$STAGE"
mkdir -p "$STAGE/bin"
install -m 0755 "$BINARY" "$STAGE/bin/opensignage-agent"
if [ -d "$PKGROOT/share" ]; then cp -R "$PKGROOT/share" "$STAGE/share"; fi
if [ -f "$PKGROOT/VERSION" ]; then cp "$PKGROOT/VERSION" "$STAGE/VERSION"; else printf '%s\n' "$VERSION" >"$STAGE/VERSION"; fi
chmod -R a+rX "$STAGE"
# Upgrade or first install (decides the hints printed at the end).
WAS=$(readlink "$PREFIX/current" 2>/dev/null || true)
as_user sh -eu -c '
	PREFIX=$1; VERSION=$2; STAGE=$3
	REL=$PREFIX/releases/$VERSION
	mkdir -p "$PREFIX/releases"
	rm -rf "$REL.tmp"
	mkdir "$REL.tmp"
	cp -R "$STAGE/." "$REL.tmp/"
	rm -rf "$REL"
	mv "$REL.tmp" "$REL"
	OLD=$(readlink "$PREFIX/current" 2>/dev/null || true)
	if [ -n "$OLD" ] && [ "$OLD" != "releases/$VERSION" ]; then ln -sfn "$OLD" "$PREFIX/previous"; fi
	ln -sfn "releases/$VERSION" "$PREFIX/current.new"
	mv -Tf "$PREFIX/current.new" "$PREFIX/current"
	PREV=$(readlink "$PREFIX/previous" 2>/dev/null || true)
	for d in "$PREFIX"/releases/*; do
		[ -d "$d" ] || continue
		n=releases/$(basename "$d")
		if [ "$n" != "releases/$VERSION" ] && [ "$n" != "$PREV" ]; then rm -rf "$d"; fi
	done
' sh "$PREFIX" "$VERSION" "$STAGE"

[ -d "$LIBDIR" ] || record "dir $LIBDIR"
mkdir -p "$LIBDIR"
for f in session.sh rollback.sh uninstall.sh; do
	install -o root -g root -m 0755 "$SCRIPT_DIR/$f" "$LIBDIR/$f"
done
# Root helper of the network settings (root-owned: the agent may run it, never change it).
if [ -f "$PKGROOT/bin/opensignage-netctl" ]; then
	install -o root -g root -m 0755 "$PKGROOT/bin/opensignage-netctl" "$LIBDIR/opensignage-netctl"
else
	warn "package without opensignage-netctl: the network settings of the WebGUI stay read-only"
fi
rm -f "$LIBDIR/set-hostname.sh" # host name helper of older versions (now set here)

# ---------------------------------------------------------------- configuration

[ -d "$CONFDIR" ] || record "dir $CONFDIR"
mkdir -p "$CONFDIR"
# set_conf KEY VALUE: replace or append KEY=VALUE in the existing agent.env.
set_conf() {
	say "Setting $1=$2 in $CONF"
	if grep -q "^$1=" "$CONF"; then
		sed -i "s|^$1=.*|$1=$2|" "$CONF"
	else
		printf '%s=%s\n' "$1" "$2" >>"$CONF"
	fi
}
if [ ! -f "$CONF" ]; then
	say "Writing $CONF"
	cat >"$CONF" <<EOF
# OpenSignage agent configuration. Restart after changes:
#   sudo systemctl restart opensignage-agent
# Keys: OS_SERVER, OS_AGENT_DATA_DIR, OS_WEBGUI_PORT, OS_PLAYER_PORT, OS_CHROMIUM,
#       OS_DISPLAY, OS_LOG_LEVEL, OS_SHARE_DIR, OS_DISCOVERY_PORT, OS_INSTALL_DIR,
#       OS_TRUSTED_KEYS (release keys in addition to the built-in ones, comma separated)
# Manual server address (empty = automatic discovery in the local network):
OS_SERVER=$SERVER
OS_AGENT_DATA_DIR=$DATA
OS_DISPLAY=:0
OS_LOG_LEVEL=info
EOF
	if [ -n "$TRUSTED_KEYS" ]; then printf 'OS_TRUSTED_KEYS=%s\n' "$TRUSTED_KEYS" >>"$CONF"; fi
	record "file $CONF"
else
	if [ -n "$SERVER" ]; then set_conf OS_SERVER "$SERVER"; fi
	if [ -n "$TRUSTED_KEYS" ]; then set_conf OS_TRUSTED_KEYS "$TRUSTED_KEYS"; fi
fi
if [ -n "$NAME" ]; then
	say "Setting OS_PLAYER_NAME=$NAME in $CONF"
	tmp=$(mktemp)
	grep -v '^OS_PLAYER_NAME=' "$CONF" >"$tmp" || true
	printf 'OS_PLAYER_NAME=%s\n' "$NAME" >>"$tmp"
	cat "$tmp" >"$CONF"
	rm -f "$tmp"
fi
chown root:root "$CONF"
chmod 0644 "$CONF"

# ---------------------------------------------------------------- host name osp-<name>

# The host name before the first installation is kept for the uninstaller.
grep -q '^hostname ' "$MANIFEST" 2>/dev/null || record "hostname $(hostname)"
if [ -n "$NAME" ]; then
	HOST="osp-$(osp_label "$NAME")"
	OLDHOST=$(hostname)
	if [ "$OLDHOST" != "$HOST" ]; then
		say "Setting the host name to $HOST"
		if ! { command -v hostnamectl >/dev/null 2>&1 && hostnamectl set-hostname "$HOST" 2>/dev/null; }; then
			printf '%s\n' "$HOST" >/etc/hostname
			hostname "$HOST"
		fi
		# /etc/hosts: swap the name on the 127.0.1.1 line (Debian convention), keep a domain
		# behind it (old.example.lan → new.example.lan); add the line or name when missing.
		tmp=$(mktemp)
		awk -v old="$OLDHOST" -v new="$HOST" '
			!done && $1 == "127.0.1.1" {
				line = $1; hit = 0
				for (i = 2; i <= NF; i++) {
					f = $i
					if (f == old) { f = new; hit = 1 }
					else if (index(f, old ".") == 1) { f = new substr(f, length(old) + 1); hit = 1 }
					line = line "\t" f
				}
				if (!hit) line = line "\t" new
				print line; done = 1; next
			}
			{ print }
			END { if (!done) print "127.0.1.1\t" new }
		' /etc/hosts >"$tmp" && cat "$tmp" >/etc/hosts
		rm -f "$tmp"
	fi
fi

# Rootless Xorg: let the service user start X; root rights only if the driver needs them.
if [ -d /etc/X11 ]; then
	if ! recorded "restore $XWRAPPER" && ! recorded "file $XWRAPPER"; then
		if [ -f "$XWRAPPER" ]; then
			cp -p "$XWRAPPER" "$BACKUP/Xwrapper.config"
			record "restore $XWRAPPER"
		else
			record "file $XWRAPPER"
		fi
	fi
	printf 'allowed_users=anybody\nneeds_root_rights=auto\n' >"$XWRAPPER"
fi

# Minimal permissions: the remote action "reboot device" and the root helper of the network
# settings (it accepts only fixed commands and checks every input).
say "Installing sudoers rule (reboot, network helper)"
{
	printf '%s ALL=(root) NOPASSWD: /usr/bin/systemctl reboot\n' "$SVCUSER"
	printf '%s ALL=(root) NOPASSWD: %s/opensignage-netctl\n' "$SVCUSER" "$LIBDIR"
} >"$SUDOERS.tmp"
chmod 0440 "$SUDOERS.tmp"
if command -v visudo >/dev/null 2>&1 && ! visudo -cf "$SUDOERS.tmp" >/dev/null; then
	rm -f "$SUDOERS.tmp"
	die "generated sudoers rule is invalid"
fi
mv -f "$SUDOERS.tmp" "$SUDOERS"
record "file $SUDOERS"

# ---------------------------------------------------------------- display manager / tty7

DM_UNIT=""
if [ -L "$UNITDIR/display-manager.service" ]; then
	DM_UNIT=$(basename "$(readlink -f "$UNITDIR/display-manager.service")")
fi
if [ -n "$DM_UNIT" ] && [ "$DM_UNIT" != "display-manager.service" ]; then
	say "Disabling graphical login $DM_UNIT (re-enabled by uninstall.sh)"
	systemctl stop "$DM_UNIT" 2>/dev/null || true
	systemctl disable "$DM_UNIT"
	record "dm $DM_UNIT"
fi
for u in getty@tty7.service autovt@tty7.service; do
	if [ "$(systemctl is-enabled "$u" 2>/dev/null || true)" != "masked" ]; then
		systemctl mask "$u" >/dev/null
		record "mask $u"
	fi
done

# ---------------------------------------------------------------- services

say "Installing systemd units"
for u in $UNITS; do
	install -o root -g root -m 0644 "$SCRIPT_DIR/$u" "$UNITDIR/$u"
	record "unit $u"
done
systemctl daemon-reload
systemctl reset-failed opensignage-agent.service 2>/dev/null || true

if [ -n "$WEBGUI_PW" ]; then
	say "Setting the client WebGUI password"
	printf '%s\n' "$WEBGUI_PW" | as_user "$PREFIX/current/bin/opensignage-agent" webgui-password -config "$CONF"
fi

systemctl enable opensignage-display.service opensignage-agent.service >/dev/null
say "Starting services"
systemctl restart opensignage-display.service
systemctl restart opensignage-agent.service

sleep 3
systemctl is-active --quiet opensignage-display.service || warn "opensignage-display.service is not active (journalctl -u opensignage-display)"
systemctl is-active --quiet opensignage-agent.service || warn "opensignage-agent.service is not active (journalctl -u opensignage-agent)"

# ---------------------------------------------------------------- summary

PORT=$(sed -n 's/^OS_WEBGUI_PORT=//p' "$CONF" | tail -n1)
[ -n "$PORT" ] || PORT=8443
echo
say "OpenSignage agent $VERSION installed."
unsupported_note
echo "    Host name: $(hostname)"
if [ -n "$WAS" ] && [ "$WAS" != "releases/$VERSION" ]; then
	echo "    Upgraded from $(basename "$WAS"); pairing, settings and content were kept."
fi
for ip in $(hostname -I 2>/dev/null); do
	case "$ip" in *:*) continue ;; esac
	echo "    Client WebGUI: https://$ip:$PORT/"
done
if [ -n "$WEBGUI_PW" ]; then
	echo "    Login with the password given via --webgui-password."
elif [ ! -f "$DATA/state/webgui.json" ] || grep -Eq '"mustChange": *true' "$DATA/state/webgui.json"; then
	echo "    Default password: opensignage (you must change it at the first login)."
fi
if [ -f "$DATA/state/server.json" ]; then
	echo "    The device is paired and keeps its server connection."
elif [ -n "$SERVER" ]; then
	echo "    Server: $SERVER – the screen shows a PIN; take over the device in the admin WebGUI."
else
	echo "    The device searches the server automatically and then shows a PIN on the screen."
fi
if [ "$REBOOT_NEEDED" -eq 1 ]; then
	echo "    Reboot the device once (sudo systemctl reboot): the boot settings changed (see above)."
fi
echo "    Uninstall: sudo $LIBDIR/uninstall.sh"
