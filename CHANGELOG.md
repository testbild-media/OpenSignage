# Changelog

All notable changes to OpenSignage are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/).

## 1.4.2 – 2026-10-19

A login right after a network change on a player confirms it. Installations on 1.x update
from the web interface (Settings → Updates); the players need no installer for this.

### Fixed
- Network settings of the players: a login right after applying a change – before it took
  effect, which takes up to about 30 seconds on Debian when the Wi-Fi does not connect at
  once – found nothing to confirm and showed the old settings. The login now waits for the
  change and confirms it; if the change did not come up, the login leads to its error.

## 1.4.1 – 2026-10-19

Wi-Fi can be set up in advance on the players. Installations on 1.x update from the web
interface (Settings → Updates); on Raspberry Pi players run the client installer once more
for it (the change is in the network helper, which only the installer replaces).

### Changed
- Network settings of the players: a Wi-Fi network that is not in range can be set up in
  advance – e.g. over the LAN before the player goes to its place. It stays configured and
  the player joins it as soon as it is in range; the network page says so meanwhile. On
  Raspberry Pi OS such a change used to be undone (it already worked on Debian). A network
  in range that refuses the password is still undone at once.

## 1.4.0 – 2026-10-19

Network settings on the players (wired and Wi-Fi), defaults for new presentations and a
device page in two columns. Installations on 1.x update from the web interface (Settings →
Updates); for the network settings run the client installer once more on each player.

### Added
- Network settings in the player's web interface (*Network*): wired and Wi-Fi with DHCP or
  a fixed address (IP address, subnet mask, gateway, DNS servers), a scan for Wi-Fi
  networks, the country of the radio rules, signal strength, band and channel. A change is
  active at once and the screen shows the new address; the next login to the web interface
  makes it permanent, a restart without that login returns to the previous settings; a
  change that does not come up at all is undone at once. Players without a Wi-Fi adapter say
  so; missing network firmware is named and installed on request. Follows the system's
  network setup (ifupdown on Debian, NetworkManager on Raspberry Pi OS); updates and restarts
  of the agent never take the connection down. A Wi-Fi scan sets the radio rules of the
  country first, so 5 GHz networks are found before the first Wi-Fi setup; Wi-Fi power saving
  is off (players run on mains power; on USB sticks it causes dropouts). Tested on x86 with a
  USB stick with Realtek RTL8822BU on Debian 13 (driver in the kernel, firmware from
  `firmware-realtek`). Existing players need the installer once more for it.
- Device page: network of each player – wired or Wi-Fi, address, DHCP or fixed, Wi-Fi
  network and signal strength, a change waiting for its confirmation.
- Client installer: Wi-Fi packages (`wpasupplicant`, `iw`, `wireless-regdb`) and the network
  firmware the adapters miss (Debian `non-free-firmware`).
- Settings → Defaults → Presentations: format of new presentations, background and transition
  of new slides, font and color of new texts, clocks and tickers, and the product typography
  of new presentations (with the controls of the editor). A font named there counts as used
  and cannot be deleted.

### Changed
- Device page: two columns (what the device reports, what you do with it) instead of up to
  three narrow ones in which badges and buttons ran out of their cards.
- Supported systems: Debian 12/13 and Raspberry Pi OS; Ubuntu is no longer listed (it was
  never tested).
- Client uninstaller: network settings made in the web interface, network firmware and – while
  Wi-Fi is in use – the Wi-Fi packages stay, so the device stays reachable.

### Fixed
- The default slide duration of the settings applies to slides added in the editor too (they
  always got 10 seconds).

## 1.3.0 – 2026-10-19

Zone preview: what the screens of a zone show, now or at any moment, also without devices online.
Installations on 1.x update from the web interface (Settings → Updates).

### Added
- Zone preview: every screen of a zone as its player shows it – content, prices of the time
  schedules, ticker and what is sold out in the zone – now or at a chosen moment (the clock
  runs on from there), optionally as OPEN or CLOSED, also when no device is online. One tile
  per device (online state, place in the display group); display groups without device show
  one tile per screen of their playlist. Opened from the zone list or from *Running now* on
  the Time Schedules page. The server evaluates the schedule with the same function as the
  players.

### Fixed
- Product variants: the bar through a sold-out variant runs through the middle of the small
  letters of the font (it was too high).

## 1.2.0 – 2026-10-19

Prices for whole categories in time schedules and product variants. Installations on 1.x
update from the web interface (Settings → Updates); variants need the updated players.

### Added
- Time schedules: prices for whole categories and subcategories, e.g. "all long drinks 5 €"
  during happy hour. The price applies to every product of the category (also to products
  added later); a price for a subcategory or a single product in the same rule comes first.
  The server sends the players product prices, so players on older versions show them too.
- Product variants: the kinds of a product that share its price, e.g. the juices "Orange,
  Apple, Passion fruit". On the screens they stand below the name like the description,
  separated by commas. Each variant can be marked as sold out on its own – in the operator
  view with one tap – and only that variant is struck through; when every variant is sold
  out, the whole product is. Product lists and product elements show the variants unless
  switched off in their fields. A description written as a list can be taken over as
  variants with one click. Players need the update to show variants.

## 1.1.0 – 2026-10-18

User rights: operators can be responsible for selected zones only. Installations on 1.0.x update
from the web interface (Settings → Updates); existing operators keep access to all zones.

### Added
- User rights: operators can be limited to selected zones, each with view or edit access.
  The content of a zone (its playlists and their presentations) follows automatically;
  content that also runs in other zones is read only and can be copied for the own zone
  ("Copy for my zone"). Separate rights per user for creating presentations and playlists,
  media (view and use, upload, manage), products (view, edit) and controlling the devices
  of the own zones. The server checks every request; the web interface shows only what
  the user may do. Existing operators keep access to all zones.

## 1.0.2 – 2026-10-17

### Changed
- Presentation list: the thumbnail of a presentation under the pointer changes slides every
  750 ms instead of every 250 ms.

## 1.0.1 – 2026-10-17

Improvements to the editor and the presentations from the first productive use. Installations
on 1.0.0 update from the web interface (Settings → Updates).

### Added
- Product typography: bold, italic and underline for category headings, subcategory
  headings, product names, descriptions and prices – in the presentation typography and per
  product list or product.
- Product lists: category and subcategory headings can be hidden while keeping their space
  ("Space only"), so groups stay apart without a visible heading.
- Presentation list: while the pointer rests on a presentation, its thumbnail cycles through
  all shown slides (every 250 ms, with a slide counter).

### Changed
- Version history of presentations: publishes by the same person less than 60 minutes apart
  form one editing phase, shown as one row that can be expanded. After 10 days only the last
  version of each phase is kept (and only the last 50 phases), so fine-tuning on the screens
  no longer floods the history. Versions can be given a name; named versions are kept for
  good, and a filter shows only them.

### Fixed
- Editor: changing the presentation typography (category heading, product name,
  description, price) now updates the product lists on the canvas at once, not only after
  reopening the presentation.
- Presentation list: the thumbnails show product lists with the presentation's own
  typography and uploaded fonts instead of a placeholder style.
- Slide thumbnails (presentation list and editor): transparent images such as logos keep
  their transparency instead of showing on a grey box.

## 1.0.0 – 2026-10-13

The first stable release. It contains everything listed under 1.0.0-rc.1 below, without
functional changes. Installations on 1.0.0-rc.1 update from the web interface
(Settings → Updates).

### Documentation
- Player hardware: x86 PCs with Intel graphics are recommended, the Raspberry Pi 4 is the
  minimum.

## 1.0.0-rc.1 – 2026-10-12

The first public release of OpenSignage, self-hosted digital signage for bars, restaurants
and events (local server, Raspberry Pi and x86 players, no cloud). Everything below is new.

### Server & administration
- One server for Linux amd64/arm64 (Debian 12/13, Ubuntu 22.04+, Raspberry Pi OS 64-bit),
  running as a systemd service under its own unprivileged user.
- Admin WebGUI always on ports 80/443; HTTP redirects to HTTPS. HTTPS works without a
  domain: each server has its own local certificate authority (download at `/ca.crt`,
  trusted once per tablet or computer, still valid after an IP change), or you upload your
  own certificate. Players use a separate pinned identity, so a new WebGUI certificate never
  locks them out.
- Setup wizard on first visit (no default login) or headless setup with `--admin`. Users have
  the roles administrator, operator or user (users see only their zones). The last
  administrator is protected, repeated failed logins lock the login for a while, and the
  audit log records every change.
- Backup and restore (database, media ZIP or full backup; validated, with a controlled
  restart). Storage dashboard, unused media and a trash that can empty itself; uploads that
  would fill the disk are refused before they start.
- Problems page (offline, version differs, sync failed, low disk, under-voltage, GPU lost
  and more), shown in the sidebar and on the dashboard. You can acknowledge a problem; it is
  shown again when it goes away and comes back.
- Device defaults: one central password for all player WebGUIs, a scaling mode for new
  devices, and an option to bring devices with a different version up to date automatically.

### Editor & content
- Presentation editor in the browser, like Impress/PowerPoint: free design resolution, rich
  text, shapes, images, videos, audio, product lists, single products, clock and ticker.
  Backgrounds can be a colour, gradient, image or video. Transitions are cut, fade, slide and
  zoom; pinned elements stay in place during a transition.
- Undo/redo, copy/paste, grouping, alignment, snapping, context menus, rulers and guides; a
  one-row toolbar with panel tabs (Presentation · Slide · Element · Layers).
- Autosave never changes the screens: you publish or discard a draft. A version history
  lets you restore an earlier version into the draft.
- The editor and the player use the same rendering engine: the screen shows exactly what
  the editor shows. A 1280×720 design stays sharp on a 1920×1080 screen.
- Media manager for images, videos and audio, with folders and resumable uploads; replacing
  a file keeps its ID. Videos are converted to H.264: a 1080p variant at the source frame
  rate (up to 60 fps) for x86 players, for 50/60 fps videos a 1080p variant at half the frame
  rate for the Raspberry Pi 4, and a 720p proxy at 25/30 fps for weak players.
- 42 bundled font families (offline), plus your own uploaded fonts in the editor and on
  every player. Font lists are sorted by name; the fonts page shows where each font is
  used.
- Products with categories and subcategories, type, quantity, description and price, and a
  duplicate function. A brand logo can replace the name: its letters are as high as the
  font's letters and sit on the baseline. Each product has its own logo size.

### Playlists & time schedules
- Playlists of presentations, single slides, images and videos. Each entry plays for a set
  time or a set number of repeats. Drag and drop, search across media, draft and publish.
- Time schedules: zone and global windows by weekday and time (they may cross midnight),
  plus a default rule per zone. They switch playlists, prices and tickers in the configured
  time zone, daylight saving time included.
- Every player evaluates the schedules itself, so switches happen on time even when the
  server is offline. Players use the server's clock, so a player without a real-time clock
  (e.g. a Raspberry Pi) still switches on time.
- Tickers come from schedule rules (e.g. happy hour prices with a ticker) or from a ticker
  element; ticker templates set their look.
- The "Now" view shows what is playing in each zone and display group.

### Several screens in step
- All screens of a zone switch slides at the same moment, with or without the server. A
  screen that starts later joins mid-sequence, and its video jumps to the matching position.
- Display groups with numbered places; playlists with up to 16 tracks (place n plays track
  n), e.g. a menu across three screens. Rules change single things: the playlist for all
  screens or one group, prices, or showing/hiding the ticker. Each group can have its own
  playlist for CLOSED.
- The players take the server's clock, measured every minute. Without the server, they
  follow a time leader in the LAN (7445/udp, secured with the zone key).
- Slide switches land within a few tens of milliseconds of the planned moment. A
  frame-exact video across several screens (video wall) is not a goal.

### Live operation
- Operator view `/control` for desktop, tablet and phone. Operators switch products between
  available and sold out and set the zone to OPEN or CLOSED. Every switch asks for
  confirmation. CLOSED shows the zone's closed playlist.
- All screens of the zone update in under a second: sold-out items are greyed out with a
  red bar; unavailable items disappear without leaving a gap.
- Only administrators can mark an item "not available", globally or per zone. Product
  status is global and can be overridden per zone.
- From the device page: blackout and remote actions (sync, reload the player, clean the
  cache with a preview, screenshot, restart, reboot).

### Players
- Agent for Raspberry Pi OS (Lite included), Debian and Ubuntu. Chromium runs as a kiosk in
  a minimal X session without a desktop; a cold start needs no login.
- The installer asks for the player name and sets the host name to `osp-<name>`. The
  administration suggests that name when you take the player over.
- Finds the server in the LAN (7444/udp) within seconds and shows its PIN, IP addresses and
  the server fingerprint. Across networks a server address or host name works – through a
  VPN or directly over the internet, e.g. with the server in a data centre.
- The display mode (resolution, refresh rate, rotation, custom modes, up to 4K) is set
  remotely and survives restarts. Scaling: fit, fill or 1:1.
- Player WebGUI on 8443 (status, server address, pair again, actions). The default password
  `opensignage` must be changed at the first login.
- Offline-safe: downloads are atomic, resumable and checksum-verified. Players keep playing
  without the server or the internet and restart offline with their last content. The kiosk
  browser opens no connections to the internet.
- Hardware video decoding by default: V4L2 on the Raspberry Pi, VA-API with Vulkan on x86
  (tested with Intel graphics). After repeated decode errors the player falls back to
  software decoding.
- Plasma protection (slight pixel shift) and plasma regeneration (white noise).
- Supported: Raspberry Pi 4 and small x86 PCs with Intel graphics. Raspberry Pi 2/3 are not
  supported: the installer warns, and the device page shows the board model and the known
  issue.

### Updates & installation
- One-line installer for server and player (`curl` or `wget`). It downloads the newest
  release for the machine (amd64, arm64, armv7) and checks the Ed25519 signature and the
  checksum before it runs the package installer.
- Packages contain ready-made binaries; the installers take the rest from the distribution
  (ffmpeg on the server; X, Chromium, fonts and video drivers on the player). The player
  uninstaller removes exactly what its installer added.
- Settings → Updates: a daily check only shows new releases, and nothing installs until you
  click. Releases come from GitHub or an uploaded offline bundle. The server and every player
  check the release signature themselves.
- Server updates back up the database first and roll back on their own if the self-test
  fails or the new version does not start. The server then updates the players one per zone
  at a time, with a health check and automatic rollback. Players that were offline get a
  warning and can be brought up to date with one click.
- After a server update, open admin and operator pages reload and ask for a new login.

### Reliability & self-healing
- A crashed browser restarts, a hanging video is skipped, a video with decode errors
  reloads, and a memory watchdog restarts Chromium when it uses too much memory.
- When Chromium loses the GPU, the player restarts (at most 3 times per hour), then raises
  the alert "graphics acceleration lost". The device page shows what really renders.
- Logs are bounded (rotating log files; the X server log does not grow). A misbehaving
  device cannot affect the server or other devices.

### Languages
- 24 EU languages for the administration, the operator view, the player WebGUI and the
  screens. Each user picks a language; the server language sets price and date formats on
  the screens. Automated tests check every key in every language.
