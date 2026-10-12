# User guide

How to work with OpenSignage once the server and the players are installed
([installation](installation.md)). The web interface is available in 24 languages; the names
below are the English ones.

## Concepts

| Term | Meaning |
| --- | --- |
| **Zone** | a group of screens that show the same thing, e.g. "Bar", "Terrace", "Entrance". A zone is OPEN or CLOSED. |
| **Device** | one player with its screen. It belongs to one zone. |
| **Display group** | screens of a zone that form a row, e.g. a menu across three screens. Each screen has a **place** (1, 2, 3 …). |
| **Media** | uploaded images, videos and audio files. |
| **Presentation** | slides designed in the editor: text, images, videos, shapes, product lists, clock, ticker. |
| **Playlist** | the order in which presentations, single slides, images and videos play. |
| **Time schedule** | rules that decide by weekday and time which playlist runs, which prices apply and whether a ticker is shown. |
| **Product** | an item with category, price and status (available, sold out, not available). Product lists on the slides show them live. |

Nothing you edit reaches the screens until you **publish** it. Drafts are saved
automatically.

## From an empty server to the first screen

1. **Media** – upload images and videos (drag and drop, resumable).
2. **Presentations** – create a presentation, design the slides, **Publish**.
3. **Playlists** – add the presentation (or single slides, images, videos), set how long
   each entry plays, **Publish**.
4. **Zones** – create a zone.
5. **Devices** – take the player over and assign it to the zone.
6. **Time schedules** – create the **default rule** of the zone with the playlist. The
   screens of the zone start playing within seconds.

## Presentations and the editor

The editor works like Impress or PowerPoint in the browser. Each presentation has its own
design resolution (e.g. 1920×1080 or 1080×1920 for portrait screens); screens with a
different resolution scale it sharply (fit, fill or 1:1 per device).

- **Elements:** text with rich formatting, images, video, audio, shapes (rectangle,
  ellipse, line, arrow), product list, single product, clock, ticker.
- **Backgrounds:** colour, gradient, image or video. A background video keeps running across
  slides with the same video.
- **Transitions:** cut, fade, slide, zoom. Pinned elements (e.g. a logo) stay in place.
- **Tools:** undo/redo, copy/paste (also between presentations), grouping, alignment,
  snapping, rulers and guides, layers, context menus.
- **Product lists** show categories and products with name, quantity, description and price
  in up to four columns; their look comes from the presentation's typography. A product can
  show its brand logo instead of the name.
- **Version history:** every publish is kept; restore an earlier version into the draft.
- **Preview** shows the slides exactly as a screen will – the editor and the players use the
  same rendering engine.

## Products and prices

Products have a category (and optional subcategory), type, quantity, description, base
price and an optional logo image. **Duplicate** copies a product with all its values.
Prices that change by time (happy hour) are set in time schedule rules, not on the product.

Product status:

- **available** – shown normally;
- **sold out** – shown greyed out with a red bar;
- **not available** – not shown at all, without leaving a gap.

The status is global and can be overridden per zone. Operators switch between available and
sold out; only administrators set "not available".

## Playlists

Entries are presentations, single slides, images or videos. Each entry plays for a set time
or a set number of repeats. Playlists are drafts until you publish them.

**Playlists for several screens:** set "Screens" to 2–16. Every row is a step with a fixed
duration; each track holds a slide or a medium for one place of a display group (an empty
cell shows the same as track 1). The screen at place *n* plays track *n*, places without a
track of their own play track 1.

## Time schedules

A zone has one **default rule** (always active) and any number of rules with a **time
window** (weekdays, start and end; a window may cross midnight). **Global** rules apply to
all zones. A rule changes only what it sets:

- the playlist of all screens of the zone, or of single display groups (or of the screens
  without group);
- prices of single products;
- a **ticker** – show one (text and ticker template) or hide it.

For each of these things the first active rule wins in this order: zone window → global
window → default rule. Rules may overlap as long as they do not change the same thing. A
zone that is **CLOSED** shows its CLOSED playlist (a display group may have its own).
**Now** shows for each zone and display group what is playing and why.

Every player evaluates the schedule itself in the configured time zone (daylight saving
included), so switches happen on time even when the server is offline.

## Several screens in step

All screens of a zone switch slides at the same moment. Every player computes from the
clock which slide is due; a screen that starts later joins in the middle of the sequence,
videos jump to the matching position. The time comes from the server (measured every
minute, shown on the device page under "Clock"); when the server is gone, the players follow
one player of the zone in the LAN.

Set up a menu wall: **Zones → Screens and groups** → create a display group, give each
screen its place, then use a playlist with one track per screen.

## Live operation

The **operator view** (`https://<server>/control`) is made for tablets and phones at the bar:

- choose the zone;
- switch products between available and sold out;
- set the zone to OPEN or CLOSED.

Every switch asks for confirmation and reaches all screens of the zone in under a second.
Operators only see the zones they are allowed to use.

## Devices

The device page shows each screen's state: online, version, what it plays, CPU, memory,
temperature, disk, cache, clock, video decoding, display mode and a screenshot on request.

- **Remote actions:** sync now, reload the player, clean the cache (with a preview of what
  would be deleted), screenshot, restart the player, reboot the device, blackout.
- **Display:** resolution, refresh rate and rotation (also custom modes), chosen from the
  modes the screen reports; scaling fit, fill or 1:1.
- **Performance profile:** Automatic, Economy (e.g. Raspberry Pi 3: 720p videos), Standard
  (Raspberry Pi 4), Strong (PC). It decides which video variant a player downloads.
- **Plasma protection** (slight pixel shift) and **plasma regeneration** (white noise until
  stopped or restarted).
- **Problems:** offline, other version, sync failed, low disk space, under-voltage, graphics
  acceleration lost and more – shown on the dashboard and in the sidebar. Acknowledge a
  problem to hide it until it goes away and comes back.

Players heal themselves: a crashed browser restarts, a hanging video is skipped, a player
that lost its graphics acceleration restarts (at most three times an hour, then it raises
a problem).

Each player also has its own small web interface (`https://<player-ip>:8443`): status,
server address, pair again, actions. A central password for all players can be set under
Settings → Defaults.

## Media and fonts

- Videos are converted to H.264 for the players: full HD at the original frame rate (up to
  60 fps), for 50/60 fps videos also a full-HD version at half the frame rate (Raspberry
  Pi 4), and a 720p proxy at 25/30 fps for weak players. Replacing a file keeps its ID, so
  every presentation that uses it shows the new version.
- **Unused media** lists files nothing uses; deleted files stay in the **trash** for a set
  number of days.
- **Fonts:** 42 font families are bundled; administrators can upload more (TTF, OTF, WOFF,
  WOFF2) after confirming that they may use them. The fonts page shows where each font is used. All
  fonts work offline on every player.

## Users and roles

| Role | Can |
| --- | --- |
| Administrator | everything, including users, settings, backup, updates, fonts and "not available" |
| Operator | content, playlists, schedules, zones, devices and the operator view |
| User | the operator view for the zones assigned to them |

Repeated failed logins lock the login for a while; the last administrator cannot be removed.
The audit log records every change.

## Settings

- **General:** language (each user can choose their own), time zone, currency.
- **Defaults:** scaling and other defaults for new devices, central player password,
  bring devices with another version up to date automatically.
- **Certificate:** download the local CA or upload your own certificate.
- **Storage:** what uses the disk, unused media, old revisions.
- **Backup:** database, media (ZIP) or everything; restore with a controlled restart.
- **Updates:** new releases are shown (daily check), installed on click or from an uploaded
  offline bundle; the server updates itself first, then the players zone by zone.
