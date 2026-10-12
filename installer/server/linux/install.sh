#!/usr/bin/env bash
# OpenSignage server installer for Debian/Ubuntu/Raspberry Pi OS (amd64, arm64, armhf).
#
# Run from an extracted release package as root:
#   sudo ./install-server.sh [--admin USER]
#
# Without --admin the first administrator is created in the browser (setup wizard).
# With --admin the password is read from stdin (headless installs).
# Running it again with a newer package upgrades in place: database snapshot, switch to
# the new release, health check, automatic switch-back if the new version does not start.
#
# Every system change is recorded in a root-only manifest so that uninstall-server.sh can
# revert exactly those changes.
#
# Ownership: the release tree /opt/opensignage and the data directory belong to the
# service user (the server installs updates itself). Root never writes into them or
# follows links inside them: release files are staged in a root-owned temporary
# directory and copied, renamed and linked as the service user (as_svc); the database
# restore on the failure path runs as the service user too.
set -euo pipefail

PKG_DIR="$(cd "$(dirname "$0")" && pwd)"
PREFIX=/opt/opensignage
DATA=/var/lib/opensignage
CONF_DIR=/etc/opensignage
CONF="$CONF_DIR/server.env"
UNIT_DIR=/etc/systemd/system
SVC_USER=opensignage
STATE_DIR=/var/lib/opensignage-installer # root-only, not writable by the service user
MANIFEST="$STATE_DIR/manifest"
ADMIN_USER=""

say() { printf '\033[1;33m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31mError:\033[0m %s\n' "$*" >&2; exit 1; }
record() { grep -qxF "$*" "$MANIFEST" 2>/dev/null || echo "$*" >>"$MANIFEST"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --admin) ADMIN_USER="${2:-}"; shift 2 ;;
    --yes|-y) shift ;;
    -h|--help) sed -n '2,14p' "$0"; exit 0 ;;
    *) die "unknown option $1" ;;
  esac
done

# --- capability checks (not version checks) -----------------------------------------------
[ "$(id -u)" = 0 ] || die "please run as root (sudo)"
command -v systemctl >/dev/null || die "systemd is required"
command -v apt-get >/dev/null || die "apt-get is required (Debian, Ubuntu, Raspberry Pi OS)"
command -v runuser >/dev/null || die "runuser (util-linux) is required"
[ -x "$PKG_DIR/bin/opensignage-server" ] || die "bin/opensignage-server not found next to this script"
"$PKG_DIR/bin/opensignage-server" version >/dev/null 2>&1 || die "the server binary does not run on this machine (wrong architecture $(uname -m)?)"
VERSION="$(cat "$PKG_DIR/VERSION" 2>/dev/null || true)"
[ -n "$VERSION" ] || VERSION="$("$PKG_DIR/bin/opensignage-server" version | awk '{print $2}')"
case "$VERSION" in *[!0-9A-Za-z.+-]*|"") die "invalid version $VERSION" ;; esac
free_mb=$(df -Pm / | awk 'NR==2 {print $4}')
[ "${free_mb:-0}" -ge 1024 ] || die "at least 1 GB free disk space is required (free: ${free_mb} MB)"

conf_value() { # value of KEY from server.env (if set), else default
  local v=""
  [ -f "$CONF" ] && v="$(sed -n "s/^[[:space:]]*$1[[:space:]]*=[[:space:]]*//p" "$CONF" | tail -1 | tr -d '"'"'")"
  echo "${v:-$2}"
}
DEVICE_PORT="$(conf_value OS_DEVICE_PORT 7443)"
DISCOVERY_PORT="$(conf_value OS_DISCOVERY_PORT 7444)"

port_owner() { ss -ltnpH "sport = :$1" 2>/dev/null | grep -v opensignage-ser || true; }
for p in 80 443 "$DEVICE_PORT"; do
  owner="$(port_owner "$p")"
  [ -z "$owner" ] || die "port $p is already in use: $owner (the web interface must run on ports 80/443)"
done

install -d -m 0700 -o root -g root "$STATE_DIR"
touch "$MANIFEST"
chmod 0600 "$MANIFEST"

# --- packages ------------------------------------------------------------------------------
if ! dpkg -s ffmpeg >/dev/null 2>&1; then
  say "Installing ffmpeg"
  DEBIAN_FRONTEND=noninteractive apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq ffmpeg
  record "package ffmpeg"
fi

# --- user and directories -----------------------------------------------------------------
if ! id "$SVC_USER" >/dev/null 2>&1; then
  say "Creating system user $SVC_USER"
  useradd --system --home-dir "$DATA" --shell /usr/sbin/nologin "$SVC_USER"
  record "user $SVC_USER"
fi
as_svc() { runuser -u "$SVC_USER" -- "$@"; }
# $PREFIX and $DATA live in root-owned parents; everything below them is the service
# user's and is created by it.
for d in "$PREFIX" "$DATA" "$CONF_DIR"; do
  if [ ! -d "$d" ]; then mkdir -p "$d"; record "dir $d"; fi
done
chown "$SVC_USER:$SVC_USER" "$DATA" "$PREFIX"
chmod 0750 "$DATA"
if [ ! -d "$PREFIX/releases" ]; then as_svc mkdir "$PREFIX/releases"; record "dir $PREFIX/releases"; fi

# --- configuration (never overwritten) ------------------------------------------------------
if [ ! -f "$CONF" ]; then
  cat >"$CONF" <<'EOF'
# OpenSignage server configuration. Changes take effect after:
#   sudo systemctl restart opensignage-server
# The web interface always runs on ports 80 (redirect) and 443 (HTTPS); data lives in
# /var/lib/opensignage. After changing a port, run install-server.sh again so the
# firewall rules (if ufw is active) follow.
#OS_DEVICE_PORT=7443
#OS_DISCOVERY_PORT=7444
#OS_LOG_LEVEL=info
EOF
  chmod 0644 "$CONF"
  record "file $CONF"
fi

# --- release files (side by side) ---------------------------------------------------------
say "Installing OpenSignage $VERSION"
REL="$PREFIX/releases/$VERSION"
OLD=""
if [ -L "$PREFIX/current" ]; then OLD="$(readlink -f "$PREFIX/current")"; fi
if [ "$OLD" = "$(readlink -f "$REL" 2>/dev/null || true)" ]; then OLD=""; fi # same version re-install
# Stage in a root-owned directory the service user can read, then copy as that user.
STAGE="$(mktemp -d /tmp/opensignage-stage.XXXXXX)"
trap 'rm -rf "$STAGE"' EXIT
cp -a "$PKG_DIR/bin" "$PKG_DIR/share" "$STAGE/"
if [ -d "$PKG_DIR/install" ]; then cp -a "$PKG_DIR/install" "$STAGE/"; fi
echo "$VERSION" >"$STAGE/VERSION"
chmod 0755 "$STAGE"
chmod -R a+rX "$STAGE"
as_svc sh -eu -c 'rm -rf "$1.tmp"; mkdir "$1.tmp"; cp -R "$2/." "$1.tmp/"' sh "$REL" "$STAGE"

# Snapshot the database before switching (upgrade case).
SNAPSHOT=""
DB="$DATA/db/opensignage.db"
HAD_DB=0
if [ -f "$DB" ]; then HAD_DB=1; fi # decides the hints at the end (the server creates the DB on start)
if [ -n "$OLD" ] && [ -f "$DB" ]; then
  SNAPSHOT="$DATA/backups/pre-install-$(basename "$OLD")-$(date -u +%Y%m%d-%H%M%S).db"
  as_svc install -d -m 0750 "$DATA/backups"
  say "Backing up the database to $SNAPSHOT"
  as_svc "$OLD/bin/opensignage-server" backup-db --config "$CONF" "$SNAPSHOT" \
    || die "database backup failed; nothing was changed"
fi

switch_to() { # atomic symlink switch as the service user: switch_to <release dir>
  as_svc sh -eu -c 'ln -sfn "$1" "$2/current.tmp"; mv -T "$2/current.tmp" "$2/current"' sh "$1" "$PREFIX"
}
as_svc sh -eu -c 'rm -rf "$1"; mv "$1.tmp" "$1"' sh "$REL"
if [ -n "$OLD" ]; then
  as_svc sh -eu -c 'ln -sfn "$1" "$2/previous.tmp"; mv -T "$2/previous.tmp" "$2/previous"' sh "$OLD" "$PREFIX"
fi
switch_to "$REL"

# --- systemd units --------------------------------------------------------------------------
say "Installing systemd services"
cat >"$UNIT_DIR/opensignage-server.service" <<EOF
[Unit]
Description=OpenSignage server
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=120
StartLimitBurst=5
OnFailure=opensignage-rollback.service

[Service]
User=$SVC_USER
Group=$SVC_USER
ExecStart=$PREFIX/current/bin/opensignage-server serve --config $CONF
Restart=always
RestartSec=2
# Automatic restarts do not pass through "failed", so OnFailure (rollback unit) only runs
# when the start limit is hit (systemd >= 254; older versions ignore the key).
RestartMode=direct
TimeoutStopSec=30
# Ports 80/443 without running as root.
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=$DATA $PREFIX
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
EOF
# When the server keeps crashing (start limit hit), this unit runs the PREVIOUS release's
# rollback as the unprivileged service user (it undoes a failed update if one is pending),
# waits a little and starts the server again, so a crash loop never leaves the venue
# without a server for good. Only the two systemctl calls run privileged ("+").
cat >"$UNIT_DIR/opensignage-rollback.service" <<EOF
[Unit]
Description=OpenSignage server recovery (rollback of a failed update, then restart)

[Service]
Type=oneshot
User=$SVC_USER
Group=$SVC_USER
NoNewPrivileges=true
ExecStart=/bin/sh -c 'if [ -x $PREFIX/previous/bin/opensignage-server ]; then $PREFIX/previous/bin/opensignage-server rollback --config $CONF || true; fi; sleep 20'
ExecStartPost=+/bin/systemctl reset-failed opensignage-server.service
ExecStartPost=+/bin/systemctl start --no-block opensignage-server.service
EOF
record "unit opensignage-server.service"
record "unit opensignage-rollback.service"
systemctl daemon-reload
systemctl enable opensignage-server.service >/dev/null 2>&1
systemctl reset-failed opensignage-server.service >/dev/null 2>&1 || true
systemctl restart opensignage-server.service

healthy() {
  for _ in $(seq 1 40); do
    if systemctl is-active --quiet opensignage-server && ss -ltnH 'sport = :443' | grep -q .; then return 0; fi
    sleep 1
  done
  return 1
}

if ! healthy; then
  journalctl -u opensignage-server -n 30 --no-pager || true
  if [ -n "$OLD" ]; then
    say "The new version did not start – switching back to $(basename "$OLD")"
    systemctl stop opensignage-server.service || true
    if [ -n "$SNAPSHOT" ] && [ -f "$SNAPSHOT" ]; then
      as_svc sh -eu -c 'rm -f "$1-wal" "$1-shm"; install -m 0640 "$2" "$1"' sh "$DB" "$SNAPSHOT"
    fi
    switch_to "$OLD"
    as_svc rm -f "$PREFIX/previous"
    systemctl reset-failed opensignage-server.service >/dev/null 2>&1 || true
    systemctl start opensignage-server.service
    healthy || die "the previous version did not start either; see journalctl -u opensignage-server"
    die "upgrade to $VERSION failed; version $(basename "$OLD") is running again with its data"
  fi
  die "the server did not start"
fi

# --- firewall (only if ufw is active; only rules that did not exist before) ------------------
if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
  for rule in 80/tcp 443/tcp "$DEVICE_PORT/tcp" "$DISCOVERY_PORT/udp"; do
    if ! ufw status | awk '{print $1}' | grep -qx "$rule"; then
      ufw allow "$rule" >/dev/null && record "ufw $rule"
    fi
  done
fi

# --- headless admin --------------------------------------------------------------------------
if [ -n "$ADMIN_USER" ]; then
  if runuser -u "$SVC_USER" -- "$PREFIX/current/bin/opensignage-server" create-admin -u "$ADMIN_USER" --config "$CONF"; then
    say "Administrator $ADMIN_USER created"
  else
    echo "note: administrator not created (users already exist?). Use reset-password if needed."
  fi
fi

IPS="$(hostname -I 2>/dev/null | tr ' ' '\n' | grep -E '^[0-9]+\.' || true)"
echo
say "OpenSignage $VERSION is running."
for ip in $IPS; do echo "    https://$ip/"; done
echo
if [ -n "$OLD" ]; then
  echo "    Upgraded from $(basename "$OLD"); data and settings were kept."
  echo "    The previous version stays installed for a rollback."
elif [ "$HAD_DB" = 1 ]; then
  echo "    Existing data and settings were kept."
elif [ -n "$ADMIN_USER" ]; then
  echo "    Log in as $ADMIN_USER. The browser warns about the certificate until the local CA"
  echo "    is trusted: download it from http://<address>/ca.crt (see Settings → Certificate)."
else
  echo "    Open the address in a browser and create the first administrator."
  echo "    The browser warns about the certificate until the local CA is trusted:"
  echo "    download it from http://<address>/ca.crt (see Settings → Certificate)."
fi
