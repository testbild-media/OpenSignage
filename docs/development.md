# Development

Only needed to work on OpenSignage itself – installing it never requires building
anything ([installation](installation.md)).

## Requirements

- Go (version in `go.mod`)
- Node.js 22 or newer with npm
- ffmpeg/ffprobe (only for the media tests that use the real tools; they are skipped otherwise)
- for the font bundle: Python 3.9+, git, `pip install fonttools brotli`

## Repository layout

The components, ports, contracts and modules are described in
[ARCHITECTURE.md](../ARCHITECTURE.md). In short:

| Path | Content |
| --- | --- |
| `cmd/opensignage-server`, `cmd/opensignage-agent` | the two programs |
| `cmd/osrelease` | release tool: assemble, sign, verify, keygen |
| `internal/server/…`, `internal/agent/…` | server modules, agent (player) modules |
| `internal/contracts`, `internal/schedule`, `internal/common` | shared types, time schedule evaluation, helpers |
| `web/` | npm workspace: `packages/contracts`, `packages/render` (slide renderer used by editor and player), `packages/i18n`, `packages/ui`, `apps/admin` (administration, editor, operator view), `apps/player` |
| `locales/<lang>/<namespace>.json` | translations (24 languages), read by Go and TypeScript |
| `fonts/` | bundled fonts, built by `tools/fonts` |
| `installer/` | Linux installers, uninstallers and systemd units |
| `tools/` | font bundle builder, translation helpers, license checks |
| `scripts/` | build and release helpers |

## Build and run

```bash
cd web
npm ci
npm run build          # player and admin apps → embedded into the Go binaries
cd ..
go build ./cmd/...
```

`scripts/build.sh <version> [amd64 arm64 armv7]` builds the web apps once and one Linux
package per architecture (`dist/opensignage-<version>-linux-<arch>.tar.gz`, with correct
Unix permissions via `scripts/pack.py`, also on Windows).

For a local server outside the default paths and ports:

```bash
OS_DEV=1 OS_DATA_DIR=build/localdata OS_SHARE_DIR=. OS_DEV_HTTP_PORT=8080 OS_DEV_HTTPS_PORT=8443 \
OS_DEVICE_PORT=7543 OS_DISCOVERY_PORT=7544 ./opensignage-server serve --config none.env
```

The admin app can also be served by Vite with hot reload (`cd web && npx vite apps/admin`).

## Checks

```bash
go vet ./... && go test ./...
cd web && npm run typecheck && npm run lint && npm test
```

The Go tests include the translation checks (every key in every language, same
placeholders, every key used in the code exists) and the license checks (every bundled font,
image and dependency has a free license that is listed in
[THIRD_PARTY_LICENSES.md](../THIRD_PARTY_LICENSES.md)). Fixtures in `testdata/contracts` are
validated by both the Go and the TypeScript contract types.

## Translations

English (`locales/en`) is the source. Every new key must be added to all 24 languages; the
terminology and style rules per language are in the
[translation glossary](i18n/glossar.md). `tools/i18n/write_locale.py` writes one namespace for
all languages from a spec file, `tools/i18n/merge_json.py` resolves merge conflicts in locale
files.

## Fonts

The bundle in `fonts/` is generated – do not edit it by hand. Add a family to
`tools/fonts/families.json` and run `python tools/fonts/build_fonts.py`; details, the license
checks and the rules for fonts with a Reserved Font Name are in
[tools/fonts/README.md](../tools/fonts/README.md). Only SIL OFL 1.1 and Apache 2.0 fonts are
accepted.

## Releases and signing

1. `scripts/build.sh <version>` – the packages for amd64, arm64 and armv7.
2. `go run ./cmd/osrelease assemble -dir dist -version <version> -key <private key> [-notes notes.md]`
   writes `dist/release-<version>/`: `release.json`, `SHA256SUMS`, its Ed25519 signature
   `SHA256SUMS.sig` and the offline bundle `opensignage-<version>-bundle.tar`.
3. Publish the packages, `release.json`, `SHA256SUMS` and `SHA256SUMS.sig` as assets of a
   GitHub release `v<version>` in the update repository (`scripts/publish_release.py`).

`osrelease keygen -out <file>` creates a key pair. The private key is never committed. The
public key is built into the binaries (`internal/common/release.TrustedKeys`) and embedded in
the one-line installer `install.sh`; change them together. Administrators can add trusted
keys in the server settings – they only extend what the server accepts: every player verifies
a release itself with its built-in keys plus `OS_TRUSTED_KEYS` in its own
`/etc/opensignage/agent.env` (client installer option `--trusted-key`).

How an update runs (server backup and self-rollback, then the players zone by zone with a
health check) is described in [ARCHITECTURE.md](../ARCHITECTURE.md) (§5.9) and in the
[user guide](user-guide.md#settings).
