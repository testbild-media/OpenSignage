#!/usr/bin/env bash
# Removes the OpenSignage server and reverts the system changes of the installer.
#   sudo ./uninstall-server.sh            keeps data (/var/lib/opensignage) and config
#   sudo ./uninstall-server.sh --purge    also deletes data and configuration
#
# Only changes listed in the root-only installer manifest are reverted, and only if they
# match the allowlist below (the manifest is never trusted blindly).
set -euo pipefail

PREFIX=/opt/opensignage
DATA=/var/lib/opensignage
CONF_DIR=/etc/opensignage
STATE_DIR=/var/lib/opensignage-installer
MANIFEST="$STATE_DIR/manifest"
PURGE=0
if [ "${1:-}" = "--purge" ]; then PURGE=1; fi
[ "$(id -u)" = 0 ] || { echo "please run as root" >&2; exit 1; }

systemctl stop opensignage-rollback.service >/dev/null 2>&1 || true
systemctl disable --now opensignage-server.service >/dev/null 2>&1 || true
systemctl reset-failed opensignage-server.service opensignage-rollback.service >/dev/null 2>&1 || true

remove_package() {
  # Remove only if nothing else would be removed with it.
  local extra
  extra="$(apt-get -s remove "$1" 2>/dev/null | awk '/^Remv /{print $2}' | grep -vx "$1" || true)"
  if [ -n "$extra" ]; then
    echo "note: keeping $1 – other packages depend on it: $(echo "$extra" | tr '\n' ' ')"
  else
    DEBIAN_FRONTEND=noninteractive apt-get remove -y -qq "$1" >/dev/null 2>&1 || echo "note: could not remove $1"
    echo "removed $1 (unused dependencies can be removed with: apt-get autoremove)"
  fi
}

if [ -f "$MANIFEST" ]; then
  # Revert in reverse order; ignore anything outside the allowlist.
  tac "$MANIFEST" | while read -r kind value; do
    case "$kind $value" in
      "unit opensignage-server.service"|"unit opensignage-rollback.service")
        rm -f "/etc/systemd/system/$value" ;;
      "ufw 80/tcp"|"ufw 443/tcp"|ufw\ [0-9]*/tcp|ufw\ [0-9]*/udp)
        if command -v ufw >/dev/null 2>&1; then ufw delete allow "$value" >/dev/null 2>&1 || true; fi ;;
      "file $CONF_DIR/server.env")
        if [ "$PURGE" = 1 ]; then rm -f "$value"; fi ;;
      "dir $PREFIX"|"dir $PREFIX/releases")
        rm -rf "$value" ;;
      "dir $DATA"|"dir $CONF_DIR")
        if [ "$PURGE" = 1 ]; then rm -rf "$value"; fi ;;
      "user opensignage")
        if [ "$PURGE" = 1 ]; then userdel opensignage 2>/dev/null || true; fi ;;
      "package ffmpeg")
        remove_package ffmpeg ;;
      *)
        echo "note: ignoring unexpected manifest entry: $kind $value" ;;
    esac
  done
fi
systemctl daemon-reload
rm -rf "$PREFIX"
if [ "$PURGE" = 1 ]; then
  rm -rf "$DATA" "$CONF_DIR" "$STATE_DIR"
  echo "OpenSignage server removed including all data."
else
  rm -rf "$STATE_DIR"
  echo "OpenSignage server removed. Data kept in $DATA (use --purge to delete it)."
fi
