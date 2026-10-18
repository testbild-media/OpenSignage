# OpenSignage – Architecture

How OpenSignage is built: components, ports, data flows, the contracts between server,
agent and player, storage and the server modules. Keep this file in sync with the code
(same commit). What the software does is described in the [README](README.md), how to
install and use it in [docs/](docs/README.md).

## 1. Components

```
                 ┌──────────────────────── Server (Go, one binary) ──────────────────────────┐
 Browser ──443──►│ web: admin SPA, /control, admin API /api/v1, admin WS /api/v1/ws        │
 (Admin/Tablet)  │ core: auth · users · audit · settings · media · content · products ·     │
                 │       zones · schedules · deploy builder · live hub · devices · updates  │
 Player ─7443───►│ device API /device/v1 (+ WS), pinned identity cert                       │
 Player ─7444/udp│ discovery responder                                                       │
                 │ SQLite (WAL) + blobs/ + fonts/ + backups/                                 │
                 └───────────────────────────────────────────────────────────────────────────┘

 ┌──────────── Client device ────────────────────────────────────────────────────────────┐
 │ opensignage-agent (Go, systemd)                                                       │
 │   ├─ server link: discovery, pairing, WS (live/actions), sync (HTTPS, Range)          │
 │   ├─ local state: JSON files (atomic), blob cache                                     │
 │   ├─ scheduler: evaluates time schedules locally (shared Go package)                  │
 │   ├─ loopback server 127.0.0.1:7600 → player app, assets, fonts, WS to player         │
 │   ├─ client WebGUI :8443 (HTTPS, server-rendered HTML)                                 │
 │   ├─ kiosk supervisor: Chromium child process, CDP, watchdog with backoff             │
 │   ├─ display provider (X11/xrandr; a fake one for tests)                              │
 │   └─ updater (install side by side, health check, self rollback)                      │
 │ opensignage-display.service: Xorg + xinit (no desktop), logind session on tty7        │
 │ Chromium kiosk → http://127.0.0.1:7600/ (player app = @opensignage/render)            │
 └───────────────────────────────────────────────────────────────────────────────────────┘
```

Repository layout:

| Path | Content |
| --- | --- |
| `cmd/opensignage-server`, `cmd/opensignage-agent` | binaries |
| `cmd/osrelease` | release tool (build matrix, pack, sign, verify, keygen) |
| `internal/contracts` | Go contract types (source of truth) + validation |
| `internal/schedule` | time schedule evaluation (server and agent) |
| `internal/server/...` | server modules |
| `internal/agent/...` | agent modules |
| `internal/common/...` | logging, atomic files, tls, discovery, i18n, version |
| `web/` | npm workspace: `packages/contracts`, `packages/render`, `packages/i18n`, `packages/ui`, `apps/admin` (admin + editor + control), `apps/player` |
| `locales/<lang>/<ns>.json` | translations, read by Go and TS |
| `fonts/` | bundled fonts (woff2), `manifest.json`, licenses |
| `branding/` | logos |
| `installer/` | Linux server/client installers + uninstallers, systemd units |
| `testdata/contracts/` | JSON fixtures validated by Go and TS tests |
| `tools/` | font bundle builder, translation checks, license checks |
| `scripts/` | build and release helpers |
| `docs/` | installation, user guide, development, editor design, branding, translation glossary |

## 2. Ports and addresses

| Port | Where | Purpose |
| --- | --- | --- |
| 80/tcp | server | redirect to 443, plus `/ca.crt` |
| 443/tcp | server | admin WebGUI, `/control`, admin API, admin WS (cert: local CA leaf or uploaded) |
| 7443/tcp | server | device API + device WS (cert: device identity, pinned) |
| 7444/udp | server | discovery responder |
| 8443/tcp | client | client WebGUI (HTTPS; plain HTTP on same port → redirect) |
| 7445/udp | client | LAN time exchange of a zone's players without server (HMAC) |
| 127.0.0.1:7600 | client | player app + local assets + player WS (never exposed) |

Ports 80/443 are fixed for the server WebGUI. Other ports are configurable in the config
file (restart required; shown read-only in the UI).

The server may run outside the players' network (data centre, cloud VM). Then 443 and 7443
must be reachable; discovery (7444/udp) is LAN-only and players use a manual address or host
name (`OS_SERVER`, client installer `--server`). Nothing in the device link depends on being
in the same network.

## 3. Data flows

**Live (seconds):** operator → `POST /api/v1/live/...` → transaction (state + audit) →
live hub computes full zone live state → WS `live.state` to each connected device of the
zone and `live.changed` to admin sockets → agent persists `live-state.json` → player WS →
renderer updates affected rows only.

**Content (minutes):** publish/edit/delete → deploy builder recomputes the zone bundle(s)
→ if the canonical hash changed, a new immutable deployment row → WS `deploy.available`
(or `deploy.cleared`) → agent fetches manifest, checks disk, downloads missing blobs with
Range, verifies SHA-256 → atomic switch of `deployment-state.json` → reports via
`deploy.progress` → player receives the new bundle via loopback WS (only when the hash
changed).

**Schedules:** the agent evaluates the ruleset of the active deployment every 15 s and at
each window boundary using corrected time (server offset) in the configured IANA zone. The
server evaluates the same rules for the admin UI ("what is running now").

**Pairing:** agent (no credentials) discovers server or uses manual address → TOFU fetch of
the identity cert SPKI → `POST /device/v1/pair/request` (hardware ID, name, version, IPs) →
server returns PIN + pairing ID → player shows PIN, IPs, short fingerprint → admin claims
in UI (name, zone, scale mode) → agent polls `GET /device/v1/pair/status` → receives device
ID + token (once) → wipes previous server data, stores token + pinned SPKI → connects WS.
Delete/re-pair on the server → token revoked → WS rejected with `revoked` → agent wipes
and returns to pairing. Re-pair keeps the device row (same hardware ID → same device).

## 4. Process rules

- Every connection handler and background job is isolated (`recover`, logged); a bad
  device or request can never stop the server.
- All writes that belong together run in one SQL transaction.
- Every long-running structure is bounded (maps with caps, rings, retention counts).
- Config is validated at start; invalid config aborts with a clear message.
- Graceful shutdown on SIGTERM: stop accepting, close WS with code 1001, flush, exit.
- Logs: `log/slog` JSON, rotating files (10 MB × 5) in the data dir, plus stderr.

## 5. Contracts

Go types in `internal/contracts` are the source of truth; TS mirrors live in
`web/packages/contracts`. Both validate the fixtures in `testdata/contracts`.

### 5.1 Common

- IDs: UUIDv7 strings. Timestamps: RFC 3339 UTC strings. Money: `{ "amount": 1250,
  "currency": "EUR" }` (minor units). Durations: milliseconds (integers).
- Version: SemVer `MAJOR.MINOR.PATCH[-pre]`, parsed strictly (no `v` prefix, no
  whitespace, pre-release/build identifiers only `[0-9A-Za-z-]` separated by single dots,
  ≤ 64 characters; release versions carry no build metadata) because versions name
  directories. `protocol` (integer, device link) = 1.
- Product status: `"AVAILABLE" | "UNAVAILABLE" | "SOLD_OUT"` end-to-end, never reduced.
- Zone state: `"OPEN" | "CLOSED"`. Roles: `"admin" | "operator" | "user"`.
- Errors (HTTP): `{ "error": { "code": "snake_case", "message": "English fallback",
  "params": {…} } }`; UI translates `code` via `errors.<code>`.

### 5.2 Slide document (`contracts/document.go`)

```jsonc
Presentation {
  id, name, width, height,                  // design pixels, 16..7680
  typography: { heading: TextStyle, item: TextStyle, description: TextStyle, price: TextStyle },
  slides: Slide[]
}
Slide {
  id, name, hidden: bool,
  duration: { mode: "fixed", ms } | { mode: "video" , maxMs },   // "video" = until background video ends
  background: { type: "color", color }
            | { type: "gradient", angle, stops: [{ color, at }] }      // linear, 2..5 stops
            | { type: "image", assetId, fit: "contain"|"cover"|"fill", color }
            | { type: "video", assetId, fit, color, muted: true },
  transition: { type: "cut"|"fade"|"slide"|"zoom", ms, direction: "left"|"right"|"up"|"down" },
  elements: Element[]                       // array order = z-order (bottom → top)
}
ElementBase { id, type, name, x, y, w, h, rotation, opacity (0..1), locked, hidden,
              pinned (fixed during transitions), groupId? }
Element =
  text      { doc: RichDoc, style: TextStyle, valign: "top"|"middle"|"bottom",
              padding: [t,r,b,l], fill?: color, autoShrink: bool }
| image     { assetId, fit: "contain"|"cover"|"fill" }
| video     { assetId, fit, loop: bool, muted: bool }
| audio     { assetId, loop: bool, volume: 0..1 }
| shape     { shape: "rect"|"ellipse"|"line"|"arrow", fill?: color,
              stroke: { color, width, dash: "solid"|"dashed"|"dotted" }, radius }
| productList { source: { kind: "all"|"categories"|"products", ids: [] },
              columns: 1..4, fields: { name, quantity, description, price: bool },
              categoryHeadings: bool, subcategoryHeadings: bool, scale: 0.25..4,
              categorySpace?, subcategorySpace?: bool (a hidden heading keeps its space),
              rowGap, columnGap, priceAlign: "right"|"left", overrides?: {…TextStyle partials} }
| product   { productId, fields, scale }
| clock     { format: "HH:mm"|"HH:mm:ss"|"h:mm a"|"h:mm:ss a", date: "none"|"short"|"long",
              style: TextStyle, align, valign }
| ticker    { source: "text"|"zone", text, style: TextStyle, fill?: color, speed (design px/s),
              direction: "left"|"right" }
TextStyle { font (family id), size, color, bold, italic, underline, strike,
            highlight?: color, letterSpacing, lineHeight, align: "left"|"center"|"right"|"justify",
            outline?: { color, width }, shadow?: { color, dx, dy } }   // no blur, by design
RichDoc { paragraphs: [{ align?, bullet?: "none"|"disc"|"number",
                         runs: [{ text, marks?: Partial<TextStyle subset> }] }] }
```

Validation rules (server rejects otherwise): ≤ 1 video element per slide in addition to a
video background; ≤ 1 audio element; ≤ 200 elements per slide; colors `#rrggbb` or
`#rrggbbaa`; fonts must exist (bundled or uploaded); assetIds must exist and have the right
kind; numeric ranges as listed.

### 5.3 Playlist

```jsonc
Playlist { id, name, items: [ PlaylistItem ], screens?: 2..16 }
PlaylistItem { id, kind: "presentation"|"slide"|"media", refId,
               presentationId? (for "slide"),
               play: { mode: "duration", ms } | { mode: "repeat", count },   // exactly one
               fit?: "contain"|"cover"|"fill",                               // media only
               cells?: [ { kind: "slide"|"media", refId, presentationId?, fit? } | null ] }
```
Images: only `duration`. Single slides: their own slide duration × `repeat` or `duration`.
Several screens: `screens` tracks; `cells` hold tracks 2…n (`null` = like
track 1). Every step is a slide or a medium with a fixed `duration` (shorter videos loop);
the screen at place n plays track n, a place beyond the tracks track 1.

### 5.4 Time schedule rule

```jsonc
Rule { id, revision, name, scope: "zone"|"global"|"default", zoneId?,
       window?: { days: [1..7] (ISO, 1=Mon, day of window start), start: "HH:MM", end: "HH:MM" },
       playlistId?: string, groupPlaylists?: [{ groupId | "ungrouped", playlistId }],
       prices: [{ productId | categoryId, price: Money }],
       ticker?: { hide?, text, templateId?, template?: TickerTemplate (resolved in manifests) }, enabled }
```
A rule changes only what it sets – the playlist of all screens, of single
display groups of its zone (or the screens without group), prices, showing or hiding the
ticker. Per thing the first active rule in the order below wins; for a device a rule
counts when it sets a playlist for its group or for all screens (the group entry wins
inside a rule). Windows of the same scope may overlap unless both rules change the same
thing (`window_overlap` with `change`). The evaluation also returns the anchor of the
synchronous playback (`since`): the start of the supplying rule's window, midnight for a
default rule, the zone's `stateChangedAt` for CLOSED; CLOSED uses the group's own closed
playlist when it has one.
Ticker: the first active rule with a ticker in the same order as playlists
(zone window → global window → default) shows it over the zone's screens; none while the
zone is CLOSED. A rule needs a playlist, a price or a ticker.
Resolution (first match wins): 0) zone CLOSED with closed playlist, 1) active zone window
with valid playlist, 2) active global window with valid playlist, 3) zone default rule with
valid playlist, 4) nothing → cleared. Price per product: zone window → global window →
default rule → base price. A deleted/archived playlist does not count; the rest of the rule
(prices, ticker) still applies. `end <= start` crosses midnight.
A category price applies to every product of the category and of its subcategories in the
server currency (other currencies keep their price); inside a rule the product's own price
comes before its subcategory's, which comes before its category's. The server expands category
prices into product prices (`schedule.ExpandPrices`) for manifests, `now` and the overlap check,
so devices only ever see product prices; manifests are rebuilt when products or categories
change. A category with schedule prices cannot be deleted (`category_in_rules`).

### 5.5 Deployment manifest (`GET /device/v1/deployments/{id}`)

```jsonc
Manifest {
  id, zoneId, hash (sha256 of canonical JSON without id/hash/builtAt), builtAt,
  minAgentVersion,                        // = server version; mismatch → not sent
  timezone,
  closedPlaylistId?,
  groups?: [{ id, name, closedPlaylistId? }],   // display groups of the zone
  rules: Rule[],                          // zone + global + default rules of the zone
  playlists: Playlist[],                  // all reachable playlists (published)
  presentations: Presentation[],          // published snapshots referenced by playlists
  products: { imageAssetIds: [] },        // product images to cache
  assets: [{ id, revision, kind: "image"|"video"|"audio"|"font", sha256, size, mime,
             variant: "orig"|"v1080"|"v1080h"|"v720"|"i1920", url }],
  fonts: [{ family, files: [{ url, sha256, size, weight, style }] }]   // uploaded fonts only
}
```
Cleared target: WS `deploy.cleared { reason }`. Agent state file keeps `active`,
`previous`, `pending`.

### 5.6 Live state (`live.state` payload)

```jsonc
LiveState {
  zone: { id, name, state: "OPEN"|"CLOSED", stateChangedAt? },   // anchor of CLOSED content
  ticker: { text: "", visible: false, template },  // always empty: tickers come from schedule rules (kept for older players)
  categories: [{ id, parentId?, name, sort }],
  products: [{ id, name, description, categoryId, type: "drink"|"food"|"other",
               quantity?: { value, unit: "ml"|"cl"|"l"|"g"|"kg"|"pcs" },
               price: Money,                       // base price; schedule prices from rules
               status: "AVAILABLE"|"UNAVAILABLE"|"SOLD_OUT",   // effective (zone override → global)
               imageAssetId?, showImageInsteadOfName: bool, sort }],
  serverTime
}
TickerTemplate { font, size, color, background, speed, direction, height, position: "top"|"bottom" }
```

### 5.7 Device link messages (WS `/device/v1/ws`, JSON)

Envelope: `{ "t": "<type>", "id": "<uuid>"?, "p": {…} }`.

Server → agent: `welcome { deviceId, serverVersion, protocol, serverTime, versionMatch }`,
`device.config { name, zoneId?, scaleMode: "contain"|"cover"|"none", blackout, display:
{ width, height, refresh, rotation, custom }, perf: "auto"|"low"|"standard"|"high",
hwVideo: "auto"|"on"|"off" (auto = on for Raspberry Pi with its own Chromium, else off),
plasmaShift, webguiPasswordHash?, groupId?, position?, clockKey? }` (display group, place
and the zone's key for the LAN time exchange), `live.state`,
`deploy.available { id, hash }`, `deploy.cleared`, `action { id, name, params }`,
`update.offer { version, url, sha256, size }` (the agent installs it only when sha256/size
equal the values of the signed release metadata, §7.1 `update/release`), `revoked`,
`time.pong { seq, t0, server (unix µs) }` (answer to `time.ping`).

Agent → server: `hello { version, protocol, hardwareId, activeDeploymentId,
activeSource: { kind: "playlist"|"closed"|"cleared", playlistId?, ruleId? }, ips }`,
`status { metrics… }` (every 30 s, at once after a blackout/display change was applied),
`deploy.progress { id, state:
"preparing"|"ready"|"active"|"failed", done, total, error? }`, `action.result { id, ok,
error?, data? }`, `update.progress { version, state, error? }`, `time.ping { seq, t0 }`
(clock measurement: a burst of five every minute, the shortest round trip wins).

Well-known status keys (all optional, older agents send fewer): `version, os, arch, ips,
mode, player, playerRestarts, activeSource, activeDeploymentId, cpu, ramUsed, ramTotal,
temperature, power?, video { hw, path?: "v4l2"|"vaapi-vulkan", fallback?: "decode_errors",
fallbackAt?, gpu?: "hardware"|"software"|"lost", gpuCrashes?, gpuRecoveries?, gpuLostAt? }
(gpu = what Chromium really renders with: a player that lost the GPU is restarted, at most
3 times per hour, then it reports "lost" → alert gpu_lost), uptime, diskTotal, diskFree, cacheBytes, logsBytes, sync { state, done,
total, error? }, update?, display { output, width, height, refresh, rotation, modes:
[{ width, height, refresh: [Hz…], preferred?, current? }] }, applied { blackout, display,
displayMode, displayBusy?, displayError? }` (what the agent made of the last
`device.config`: the admin shows a change as pending until it is confirmed) and `caps`
(e.g. `clean_cache_preview`: `action clean_cache` accepts `params { dryRun: true }` and
only reports `{ deleted, freedBytes, partial, partialBytes, cacheBytes }`; the server sends
a dry run only to agents with this capability), `clock { source: "server"|"peer"|"local",
offsetMs, rttMs, leader?, ageS }` (time used for synchronous playback), `model` (hardware
model, e.g. "Raspberry Pi 3 Model B Rev 1.2"; the admin marks unsupported boards).

Unknown types are ignored and logged. Max message size 1 MiB (server side), 8 MiB (agent
side for live state). Invalid messages are counted per connection; > 20 invalid in 60 s →
disconnect (other devices unaffected).

### 5.8 Player link (loopback WS `/ws`, agent ↔ player)

Agent → player: `mode { kind: "searching"|"pairing"|"standby"|"playing"|"cleared"|
"blackout"|"updating", pin?, ips, webguiUrl, fingerprint?, notice? }`,
`bundle { hash, manifest }` (only on change), `source { playlistId | null, reason, anchor?,
position }` (anchor: ms epoch of the timeline start – every screen plays (now − anchor) mod
length on the corrected clock; position: the track of a playlist for several screens),
`live { LiveState, prices }` (prices = schedule-effective prices map), `config
{ scaleMode, perf, plasmaShift, showIpOverlay }`, `cmd { name: "reload"|"blackout"|… }`,
`versionNotice { text? }`.
Player → agent: `ping { heap, fps, item }` (every 5 s), `log { level, msg }`.

### 5.9 Release

```
opensignage-<v>-linux-amd64.tar.gz | -linux-arm64 | -linux-armv7
release.json  { version, date, notes, packages: [{ os, arch, file, sha256, size }], minUpgradeFrom }
SHA256SUMS, SHA256SUMS.sig (Ed25519 over SHA256SUMS, base64)
opensignage-<v>-bundle.tar (all of the above, for offline upload)
```
Package layout: `VERSION`, `bin/opensignage-server`, `bin/opensignage-agent`,
`share/fonts/…`, `install/…`.

Trust: the server accepts releases signed by a built-in key or a key the admin added in
the settings. Devices never rely on the server for trust: before installing an offered
package the agent fetches the signed metadata (`GET /device/v1/update/release`), verifies
`SHA256SUMS.sig` with its built-in keys plus `OS_TRUSTED_KEYS` of its own `agent.env`
(client installer `--trusted-key`), requires `release.json` to name exactly the offered
version and takes checksum and size of its package only from the signed data (otherwise
`update.progress failed signature_invalid`). Keys added in the server settings therefore
do not extend device trust; a release signed only with such a key must also be given to
the devices via `--trusted-key`/`OS_TRUSTED_KEYS`.

## 6. Storage layout

Server (`/var/lib/opensignage`): `db/opensignage.db`, `blobs/sha256/xx/<hash>`,
`variants/…`, `thumbs/…`, `fonts/custom/…`, `tmp/`, `backups/`, `updates/`, `logs/`,
`tls/`, `server.lock` (exclusive flock: held by `serve` for its lifetime and by
`rollback` while it works). Binaries: `/opt/opensignage/releases/<v>/`, symlinks
`current`, `previous`.
Config: `/etc/opensignage/server.env` (validated).

Client (`/var/lib/opensignage-agent`): `state/*.json`, `cache/blobs/<sha256>`, `logs/`,
`chromium/` (profile). Binaries: `/opt/opensignage-agent/releases/<v>/` + symlinks.
Config: `/etc/opensignage/agent.env`.

## 7. Server modules and ownership

Each module is a package `internal/server/<name>` with a constructor `New(app *core.App)`
and is registered in `internal/server/app/mod_<name>.go` via `app.Register(...)` in an
`init()`. Interfaces a module may implement: `Routes(*core.Router)` (WebGUI API on 443),
`DeviceRoutes(*http.ServeMux)` (device channel on 7443), `Run(ctx)` (background loop,
restarted on panic). Pattern to copy: `internal/server/users`.

| Module | Owns (writes) | Publishes events |
| --- | --- | --- |
| `users` | users, sessions, user_zones, audit (view) | users |
| `system` | settings, certificates, backup/restore, storage dashboard | settings |
| `media` | media_*, blobs, uploads, font_faces | media, content |
| `content` | presentations*, playlists*, content_refs | content |
| `catalog` | categories, products, zone_product_status, zones, ticker_templates, schedule_* | products, zones, live, content |
| `devices` | devices, pairing_requests, deployments*, device_deployments, live hub, device link | devices |
| `updates` | releases, update_runs, device_updates, device_platforms, settings.trustedKeys, `<data>/updates/` (incl. `update-state.json`) | updates |

Rules: a module reads any table but writes only its own. Cross-module reactions go through
the event bus (`core.Bus`): e.g. `catalog` publishes `live{zoneId}` after a product status
change, `devices` recomputes and pushes the live state; `content`/`media`/`catalog`
publish `content` after publish/delete, `devices` rebuilds affected deployments
(debounced 500 ms). Every write that changes visible state writes an audit entry in the
same transaction (`core.Audit`).

API conventions (443): JSON, `/api/v1/<resource>`, list responses `{ "<plural>": [...] }`,
optimistic concurrency via `revision` (409 `conflict` when stale), validation errors 422
`invalid_field {field, reason}`, all handlers via `core.Router.Role(pattern, minRole, h)`.
Role `user` may only touch zones in `Principal.ZoneIDs` (`p.CanZone`).

Rights (`core/rights.go`, `access`): an operator has scope `all` (everything) or `zones`
(user_zones with `view`|`edit` per zone, plus `can_create`, `media_right`
`use`|`upload`|`manage`, `products_right` `view`|`edit`, `devices_right`). Routes registered
with `Router.Role` refuse operators limited to zones (default deny); `Router.Scoped` lets them
through to handlers that check their rights. Content rights follow usage: a playlist runs in
the zones of its zone/default rules, display group playlists and closed playlists (a global
rule: everywhere), a presentation where its playlists run (draft or published); it is
editable when it runs only in zones the operator may edit, or was created by the operator
(`created_by`) and runs nowhere. `POST /api/v1/{presentations|playlists}/{id}/copy-for-zone
{zoneId}` gives a zone its own copy (published content, the zone's rules repointed).
Media: `uploaded_by`/`created_by` decide what `upload` may rename, move or replace.

### 7.1 Device channel REST (port 7443)

| Endpoint | Auth | Purpose |
| --- | --- | --- |
| `POST /device/v1/pair/request` `{hardwareId, hostname, version, os, arch, ips, screen}` | none | → `{pairingId, pin, secret, expiresAt}`; PIN 6 digits, valid 24 h, re-request refreshes |
| `GET /device/v1/pair/status?id=&secret=` | secret | → `{state: pending|claimed|expired, deviceId?, token?}` (token exactly once) |
| `GET /device/v1/ws` | Bearer | device link WebSocket (§5.7) |
| `GET /device/v1/deployments/{id}` | Bearer | manifest (only deployments of the device's zone) |
| `GET /device/v1/blobs/{sha256}` | Bearer | blob download with Range; only blobs of the device's deployments |
| `POST /device/v1/screenshots/{requestId}` | Bearer | JPEG upload answering a screenshot action |
| `GET /device/v1/update/package?version=&os=&arch=` | Bearer | release package with Range (stable across versions) |
| `GET /device/v1/update/release?version=` | Bearer | signed metadata of a ready release: `{version, release, sums, sig}` = `release.json`, `SHA256SUMS`, `SHA256SUMS.sig` byte for byte (base64); 404 `no_package` otherwise |

Device token: 32 random bytes hex, stored as SHA-256 in `devices.token_hash`. Revoked
(`NULL`) → WS closes with code 4401 and HTTP returns 401 `revoked`; the agent then wipes
server data and re-enters pairing.

### 7.2 Media variants

| Kind | Variants | Rule |
| --- | --- | --- |
| image | `orig`, `i1920` (if wider/taller than 1920), `i512` (product images and slide thumbnails, keeps transparency), `thumb` (320 px JPEG for the media library, transparency on grey) | originals > 3840 px are downscaled into `orig` on ingest; SVG kept as is |
| video | `v1080` (H.264, yuv420p, ≤1920×1080, the source frame rate up to 60 fps – level 4.1, 4.2 above 30 fps –, AAC, faststart; remux of the original when it already conforms), `v1080h` (≤1920×1080 at half the frame rate, 25/30 fps; made when the source is larger than 720p and faster than 30 fps), `v720` (proxy, ≤1280×720, 25/30 fps: 50 → 25, 60 → 30; made when the source is larger than 720p or faster than 30 fps), `poster` (JPEG), `thumb` | players with perf `low` (Raspberry Pi 3) use `v720`, `standard` (Raspberry Pi 4/5) `v1080h` if present, else `v1080`, `high` (x86) `v1080` (the Raspberry Pi 4 drops frames with 1080p50/60). Videos processed by a version without `v1080h` are processed again once at the next server start |
| audio | `a` (AAC in M4A, or the original when MP3/AAC) | |

## 8. Updates

- *Settings → Updates* checks the GitHub releases of the update source (hint only, also
  daily), downloads a release (all packages, resumable) or accepts the offline bundle, and
  verifies `SHA256SUMS.sig` against the built-in and admin-added keys before anything is used.
- **Server:** database backup (`VACUUM INTO`, `backups/pre-update-*.db`) and config copy →
  package extracted to `releases/<v>` → `update-state.json` → `previous`/`current` switched →
  restart. The new version migrates and runs a self-test; on failure (or more than two
  starts) it restores the backup and switches back itself. If it crashes before that,
  systemd's `OnFailure` runs `<previous>/bin/opensignage-server rollback` as the service user
  (the command refuses to run as root and only accepts an update state that names the
  configured database, a backup in `<data>/backups/` and releases of its own installation).
  `serve` and `rollback` share the exclusive lock `<data>/server.lock`: the rollback waits
  for a restarted server (up to 2 minutes, then it gives up without changes) and the server
  waits for a running rollback (up to 10 minutes); a server process that waited while its
  version was rolled back exits, so the restored version starts.
- **Players:** the server offers each device its package (`update.offer`, package served from
  the update store with Range), one device per zone at a time; a failure or a device that
  does not return with the new version within 10 minutes stops the rollout. The agent first
  fetches the signed metadata (`GET /device/v1/update/release`), verifies it with its own
  keys (§5.9) and installs only the package whose checksum and size the signed data names –
  side by side, with a health check and its own rollback.
- The server follows asset URLs and redirects only via https to `github.com`,
  `api.github.com` and `*.githubusercontent.com`.
- The rollback paths can be exercised with test builds (`-tags=opensignage_selftest_fail`:
  the self-test always fails; `-tags=opensignage_crash_on_start`: crashes on every start).
  Such builds are never published.
