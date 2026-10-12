#!/bin/sh
# OpenSignage client uninstaller: reverts every change listed in the install manifest
# (/var/lib/opensignage-agent-install/install-manifest), in reverse order. Only entries
# for the paths the installer itself manages are accepted.
#
# Usage: sudo ./uninstall.sh [--keep-data] [--keep-packages] [--yes]
set -eu

DATA=/var/lib/opensignage-agent
STATE=/var/lib/opensignage-agent-install
MANIFEST=$STATE/install-manifest
BACKUP_DIR=$STATE/backup
UNITDIR=/etc/systemd/system
KEEP_DATA=0
KEEP_PKGS=0
YES=0

say()  { printf '%s\n' "==> $*"; }
warn() { printf '%s\n' "WARNING: $*" >&2; }
die()  { printf '%s\n' "ERROR: $*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--keep-data) KEEP_DATA=1; shift ;;
	--keep-packages) KEEP_PKGS=1; shift ;;
	-y|--yes) YES=1; shift ;;
	-h|--help) echo "Usage: sudo $0 [--keep-data] [--keep-packages] [--yes]"; exit 0 ;;
	*) die "unknown option: $1" ;;
	esac
done

[ "$(id -u)" -eq 0 ] || die "please run as root (sudo $0)"
if [ ! -f "$MANIFEST" ]; then
	# Older installers kept the manifest in the data directory.
	if [ -f "$DATA/install-manifest" ] && [ ! -L "$DATA/install-manifest" ]; then
		MANIFEST=$DATA/install-manifest
		BACKUP_DIR=$DATA/backup
	else
		die "install manifest not found – nothing to revert"
	fi
fi

if [ "$YES" -ne 1 ] && [ -t 0 ]; then
	printf 'Remove OpenSignage agent and revert all system changes? [y/N] '
	read -r answer
	case "$answer" in y|Y|yes|j|J|ja) ;; *) echo "aborted"; exit 1 ;; esac
fi

# Read the manifest before anything is deleted (newest entries first).
ENTRIES=$(awk '{ lines[NR] = $0 } END { for (i = NR; i >= 1; i--) print lines[i] }' "$MANIFEST")
SAVED=$(mktemp -d)
trap 'rm -rf "$SAVED"' EXIT
if [ -d "$BACKUP_DIR" ]; then
	for f in "$BACKUP_DIR"/*; do
		if [ -f "$f" ] && [ ! -L "$f" ]; then cp -p "$f" "$SAVED/"; fi
	done
fi

say "Stopping services"
for u in opensignage-agent.service opensignage-display.service opensignage-agent-rollback.service; do
	systemctl stop "$u" 2>/dev/null || true
	systemctl disable "$u" 2>/dev/null || true
done

allowed_file() {
	case "$1" in
	/etc/opensignage/agent.env|/etc/sudoers.d/opensignage-agent|/etc/X11/Xwrapper.config) return 0 ;;
	esac
	return 1
}
allowed_dir() {
	case "$1" in
	/opt/opensignage-agent|/usr/lib/opensignage-agent|/var/lib/opensignage-agent|/etc/opensignage|/var/lib/opensignage-agent-install) return 0 ;;
	esac
	return 1
}

PKGS=""
DIRS=""
USER_TO_REMOVE=""
DM_TO_ENABLE=""
set -f # manifest entries are never globbed
for line in $(printf '%s\n' "$ENTRIES" | tr ' ' '|'); do
	kind=${line%%|*}
	rest=${line#*|}
	[ "$rest" != "$line" ] || rest=""
	arg1=${rest%%|*}
	arg2=${rest#*|}
	[ "$arg2" != "$rest" ] || arg2=""
	case "$kind" in
	unit)
		case "$arg1" in
		opensignage-*.service) rm -f "$UNITDIR/$arg1"; say "removed unit $arg1" ;;
		*) warn "ignoring manifest entry: unit $arg1" ;;
		esac
		;;
	mask)
		case "$arg1" in
		getty@tty7.service|autovt@tty7.service)
			systemctl unmask "$arg1" >/dev/null 2>&1 || warn "could not unmask $arg1"
			say "unmasked $arg1" ;;
		*) warn "ignoring manifest entry: mask $arg1" ;;
		esac
		;;
	dm)
		case "$arg1" in
		*[!A-Za-z0-9@._-]*|'') warn "ignoring manifest entry: dm $arg1" ;;
		*.service) DM_TO_ENABLE="$DM_TO_ENABLE $arg1" ;;
		esac
		;;
	file)
		if allowed_file "$arg1"; then rm -f "$arg1"; say "removed $arg1"; else warn "ignoring manifest entry: file $arg1"; fi
		;;
	restore)
		if [ "$arg1" = /etc/X11/Xwrapper.config ] && [ -f "$SAVED/Xwrapper.config" ]; then
			cp -p "$SAVED/Xwrapper.config" "$arg1"
			say "restored $arg1"
		else
			warn "cannot restore $arg1"
		fi
		;;
	bootcma)
		case "$arg1:$arg2" in
		/boot/firmware/config.txt:*[!0-9]*|/boot/config.txt:*[!0-9]*|*:)
			warn "ignoring manifest entry: bootcma $arg1 $arg2" ;;
		/boot/firmware/config.txt:*|/boot/config.txt:*)
			if [ -f "$arg1" ] && grep -qx "dtoverlay=vc4-kms-v3d,cma-$arg2" "$arg1"; then
				sed -i "s/^dtoverlay=vc4-kms-v3d,cma-$arg2\$/dtoverlay=vc4-kms-v3d/" "$arg1"
				say "restored the GPU memory pool in $arg1 (active after a reboot)"
			else
				warn "the graphics overlay in $arg1 was changed meanwhile – left as it is"
			fi
			;;
		*) warn "ignoring manifest entry: bootcma $arg1" ;;
		esac
		;;
	hostname)
		case "$arg1" in
		'' | -* | *[!A-Za-z0-9.-]*) warn "ignoring manifest entry: hostname $arg1" ;;
		*)
			cur=$(hostname)
			if [ "$cur" != "$arg1" ]; then
				if ! { command -v hostnamectl >/dev/null 2>&1 && hostnamectl set-hostname "$arg1" 2>/dev/null; }; then
					printf '%s\n' "$arg1" >/etc/hostname
					hostname "$arg1" || true
				fi
				# Same rule as install.sh: swap the name on the 127.0.1.1 line, keep a domain.
				tmp=$(mktemp)
				awk -v old="$cur" -v new="$arg1" '
					!done && $1 == "127.0.1.1" {
						line = $1
						for (i = 2; i <= NF; i++) {
							f = $i
							if (f == old) f = new
							else if (index(f, old ".") == 1) f = new substr(f, length(old) + 1)
							line = line "\t" f
						}
						print line; done = 1; next
					}
					{ print }
				' /etc/hosts >"$tmp" && cat "$tmp" >/etc/hosts
				rm -f "$tmp"
				say "restored the host name $arg1"
			fi
			;;
		esac
		;;
	group)
		if [ "$arg1" = opensignage ] && ! printf '%s\n' "$ENTRIES" | grep -qx "user opensignage"; then
			gpasswd -d "$arg1" "$arg2" >/dev/null 2>&1 || true
			say "removed $arg1 from group $arg2"
		fi
		;;
	user)
		if [ "$arg1" = opensignage ]; then USER_TO_REMOVE=$arg1; else warn "ignoring manifest entry: user $arg1"; fi
		;;
	dir)
		if allowed_dir "$arg1"; then
			case " $DIRS " in *" $arg1 "*) ;; *) DIRS="$DIRS $arg1" ;; esac
		else
			warn "ignoring manifest entry: dir $arg1"
		fi
		;;
	pkg)
		case "$arg1" in
		*[!a-z0-9.+-]*|'') warn "ignoring manifest entry: pkg $arg1" ;;
		*) PKGS="$PKGS $arg1" ;;
		esac
		;;
	*)
		warn "unknown manifest entry: $kind"
		;;
	esac
done

set +f
systemctl daemon-reload
systemctl reset-failed opensignage-display.service opensignage-agent.service opensignage-agent-rollback.service 2>/dev/null || true

for d in $DIRS; do
	if [ "$d" = /var/lib/opensignage-agent ] && [ "$KEEP_DATA" -eq 1 ]; then
		rm -f "$d/install-manifest"
		say "kept data directory $d"
		continue
	fi
	rm -rf "$d"
	say "removed $d"
done

if [ -n "$USER_TO_REMOVE" ]; then
	pkill -u "$USER_TO_REMOVE" 2>/dev/null || true
	sleep 1
	userdel "$USER_TO_REMOVE" 2>/dev/null || warn "could not remove user $USER_TO_REMOVE"
	getent group "$USER_TO_REMOVE" >/dev/null 2>&1 && groupdel "$USER_TO_REMOVE" 2>/dev/null || true
	say "removed user $USER_TO_REMOVE"
fi

if [ -n "$PKGS" ]; then
	if [ "$KEEP_PKGS" -eq 1 ]; then
		say "kept packages:$PKGS"
	else
		# Never remove anything the installer did not add: if purging our packages would
		# take other packages with it (installed later and depending on them), keep all.
		# shellcheck disable=SC2086
		EXTRA=$(apt-get -s purge $PKGS 2>/dev/null | awk '/^Purg /{print $2}' | sed 's/:.*//' | while read -r p; do
			case " $PKGS " in *" $p "*) ;; *) printf '%s ' "$p" ;; esac
		done)
		if [ -n "$EXTRA" ]; then
			warn "removing the installed packages would also remove: $EXTRA– keeping all packages"
		else
			say "Removing packages installed by OpenSignage:$PKGS"
			# purge: the packages did not exist before, so neither should their config files.
			# shellcheck disable=SC2086
			DEBIAN_FRONTEND=noninteractive apt-get purge -y $PKGS || warn "package removal failed"
		fi
	fi
fi

for dm in $DM_TO_ENABLE; do
	systemctl enable "$dm" >/dev/null 2>&1 || warn "could not re-enable $dm"
	systemctl start "$dm" 2>/dev/null || warn "could not start $dm"
	say "re-enabled $dm"
done

say "OpenSignage agent removed."
