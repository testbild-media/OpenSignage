#!/usr/bin/env bash
# Builds release packages: web apps once, Go binaries per architecture.
#   scripts/build.sh [VERSION] [ARCH...]      ARCH: amd64 arm64 armv7 (default: all)
# Output: dist/opensignage-<version>-linux-<arch>/ and .tar.gz
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${1:-0.1.0-dev.$(date -u +%Y%m%d%H%M)}"
shift || true
ARCHES="${*:-amd64 arm64 armv7}"
COMMIT="$(git -C "$ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)"

echo "==> web apps"
(cd "$ROOT/web" && npx vite build apps/player >/dev/null && npx vite build apps/admin >/dev/null)
touch "$ROOT/internal/server/web/static/.gitkeep" "$ROOT/internal/agent/player/static/.gitkeep"

LDFLAGS="-s -w -X opensignage/internal/common/version.Version=$VERSION -X opensignage/internal/common/version.Commit=$COMMIT"
mkdir -p "$ROOT/dist"
for arch in $ARCHES; do
  goarch="$arch"; goarm=""
  [ "$arch" = armv7 ] && { goarch=arm; goarm=7; }
  out="$ROOT/dist/opensignage-$VERSION-linux-$arch"
  echo "==> linux/$arch"
  rm -rf "$out"
  mkdir -p "$out/bin" "$out/share" "$out/install"
  for cmd in opensignage-server opensignage-agent opensignage-netctl; do
    [ -f "$ROOT/cmd/$cmd/main.go" ] || continue
    GOOS=linux GOARCH=$goarch GOARM=$goarm CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$out/bin/$cmd" "$ROOT/cmd/$cmd"
  done
  cp -a "$ROOT/fonts" "$out/share/fonts"
  cp -a "$ROOT/installer/server/linux/install.sh" "$out/install-server.sh"
  cp -a "$ROOT/installer/server/linux/uninstall.sh" "$out/uninstall-server.sh"
  if [ -d "$ROOT/installer/client/linux" ]; then
    mkdir -p "$out/install/client"
    cp -a "$ROOT/installer/client/linux/." "$out/install/client/"
    # Wrappers at the package root; the scripts need their unit files next to them.
    printf '#!/bin/sh
exec "$(dirname "$0")/install/client/install.sh" "$@"
' >"$out/install-client.sh"
    printf '#!/bin/sh
exec "$(dirname "$0")/install/client/uninstall.sh" "$@"
' >"$out/uninstall-client.sh"
  fi
  cp "$ROOT/LICENSE" "$ROOT/THIRD_PARTY_LICENSES.md" "$out/"
  echo "$VERSION" >"$out/VERSION"
  chmod 0755 "$out"/*.sh "$out"/bin/*
  python "$ROOT/scripts/pack.py" "$out" "$out.tar.gz" >/dev/null
done
echo "==> done: $VERSION"
