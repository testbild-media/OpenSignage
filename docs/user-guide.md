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
  in up to four columns; their look comes from the presentation's typography (font, size,
  colour, bold, italic, underline), which each list can adjust for itself.
  Category and subcategory headings are shown, hidden, or hidden with their space kept
  ("Space only"), so groups stay apart without a visible heading. A product can show its
  brand logo instead of the name.
- **Version history:** every publish is a version; restore an earlier version into the draft.
  Publishes by the same person less than 60 minutes apart form one editing phase and are
  shown as one row (expand it to see the single versions). After 10 days only the last
  version of each phase is kept, and only the last 50 phases. Give a version a name (e.g.
  "Summer menu 2027") to keep it for good.
- **Preview** shows the slides exactly as a screen will – the editor and the players use the
  same rendering engine.

## Products and prices

Products have a category (and optional subcategory), type, quantity, description, base
price and an optional logo image. **Duplicate** copies a product with all its values.
Prices that change by time (happy hour) are set in time schedule rules, not on the product.

**Variants** are the kinds of a product that share its price – the juices "Orange, Apple,
Passion fruit" of one juice product, "Bitter Lemon, Tonic" of a tonic. On the screens they
stand below the name like the description, separated by commas. Each variant has its own
status: a sold-out variant is struck through on its own, and when every variant is sold out
the whole product is. In the product dialog, Enter adds the next variant and a pasted list
("Orange, Apple, Guava") becomes several variants; a description written as such a list can
be taken over as variants with one click. Product lists and product elements show the
variants unless *Variants* is switched off in their fields.

Product status:

- **available** – shown normally;
- **sold out** – shown greyed out with a red bar;
- **not available** – not shown at all, without leaving a gap.

The status is global and can be overridden per zone. Operators switch between available and
sold out; only administrators set "not available". Variants have the same statuses; in the
operator view each variant is a button below its product.

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
- prices of single products or of whole categories – "all long drinks 5 €": the price applies
  to every product of the category and its subcategories, also to products added later; a
  price for a subcategory or a single product in the same rule comes first;
- a **ticker** – show one (text and ticker template) or hide it.

A time window runs **every week** or only **between two dates** (*Period*) – e.g. a happy hour
on one Saturday in two weeks: weekday Saturday, *From* and *Until* that date. One date may
stay open (from a day on, or up to a day). A window counts on the days of the period on which
it starts, in the time zone of the server. The rules table shows the period, *planned* or
*expired*, and a link to the preview of the zone at the next time of the rule, so a happy hour
can be checked today. The week view above the table goes forward and back by week (arrows)
and shows a dated rule only in its weeks.

For each of these things the first active rule wins in this order: zone window → global
window → default rule. Rules may overlap as long as they do not change the same thing. A
zone that is **CLOSED** shows its CLOSED playlist (a display group may have its own).
**Now** shows for each zone and display group what is playing and why.

**Preview** (Zones → eye icon, or *Preview* in the *Running now* box) shows every screen of a
zone as its player shows it – content, prices of the time schedules, ticker and what is sold
out in the zone – also when no device is online. Choose *Now* or a moment, e.g. Monday 18:00,
to check a happy hour before it starts; from there the clock runs on and time windows switch
as on the screens. *OPEN* / *CLOSED* shows the zone in the other state. Every device of the
zone has its own tile (with its place in its display group); a display group without device
shows one tile per screen of its playlist. Sound is off in the preview.

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
- **Network:** wired or Wi-Fi, address and whether it comes from DHCP or is fixed, the Wi-Fi
  network and its signal strength; a network change that waits for its confirmation is
  marked.
- **Performance profile:** Automatic, Economy (e.g. Raspberry Pi 3: 720p videos), Standard
  (Raspberry Pi 4), Strong (PC). It decides which video variant a player downloads.
- **Plasma protection** (slight pixel shift) and **plasma regeneration** (white noise until
  stopped or restarted).
- **Problems:** errors (player crashed, disk almost full) and warnings (offline, other
  version, failed update with its reason, sync failed, low disk space, under-voltage,
  graphics acceleration lost and more) – all on the dashboard and the problems page; the
  sidebar shows errors only, so screens switched off at night raise no alarm. Acknowledge
  a problem to hide it until it goes away and comes back.

Players heal themselves: a crashed browser restarts, a hanging video is skipped, a player
that lost its graphics acceleration restarts (at most three times an hour, then it raises
a problem).

Each player also has its own small web interface (`https://<player-ip>:8443`): status,
server address, pair again, display, network, actions, logs and update. A central password
for all players can be set under Settings → Defaults.

**Network of a player** (its web interface → *Network*):

- Every wired and Wi-Fi adapter with its state, address, gateway, DNS servers and – for
  Wi-Fi – network, signal strength, band and channel. Without a Wi-Fi adapter the page says
  so; a supported USB Wi-Fi stick appears as soon as it is plugged in.
- **Wi-Fi:** *Search networks* lists the networks around with signal, security and band – a
  network on 2.4 and 5 GHz shows the signal on both; the player joins on the band it finds
  best. *Choose* fills in the network. Enter the password (WPA2 or WPA3; open networks without
  one) and the country of the radio rules (e.g. DE) – it decides which channels the player
  may use. A hidden network is entered by hand. *Switch Wi-Fi off* ends a connection set up
  here. WEP and enterprise networks (802.1X) are not supported.
- **Address:** *Automatic (DHCP)* or *Fixed address* with IP address, subnet mask (e.g.
  255.255.255.0 or /24), gateway and up to two DNS servers (the form suggests the values in
  use). The settings are for IPv4; IPv6 stays automatic.
- **Apply:** the change is active at once and you are logged out. The screen of the player
  shows its new address until you log in there again – that login saves the change. If you
  cannot reach the player any more, restart it: without that login it starts with the
  previous settings. Applying takes a few seconds, up to about two minutes when a Wi-Fi
  network does not connect at once; a login in that time leads to the network page, which
  follows the change and then offers *Save now*. A change that does not come up at all (for
  example no answer from DHCP, or the Wi-Fi network refuses the password) is undone at once;
  after the next login the page says why.
- **Wi-Fi in advance:** a Wi-Fi network that is not in range yet can be set up beforehand –
  e.g. over the LAN in the office, then the player goes to its place without cable. Confirm
  the change with a login over the LAN; the player joins the network as soon as it is in
  range (also after a router restart or a power cut), and the page says so meanwhile.
- When the firmware of an adapter is missing, the page names it and installs it on request.

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
| Operator, access "All zones" | content, playlists, schedules, zones, devices and the operator view |
| Operator, access "Selected zones" | only the zones and rights set for them (below) |
| User | the operator view for the zones assigned to them |

**Operators for selected zones** – for example someone responsible for one bar and its
screens. Per user:

- **Zones**, each with *View* or *Edit*. *View* shows the zone, its devices (status,
  screenshot), schedule and the presentations and playlists that run there. *Edit* also
  changes the zone and default rules of the zone, its closed playlist and ticker template,
  operates it live (sold out, open/closed) and changes its content.
- **Content of a zone** is what runs there: the playlists of its rules, display groups and
  closed playlist, and the presentations in them. A presentation or playlist can be changed
  when it runs only in zones the user may edit (or when the user created it and it runs
  nowhere yet). Content that also runs in other zones opens read only; **Copy for my zone**
  gives the zone its own copy (shared playlists are copied too and the zone's rules point to
  the copy). The screens show the same until the copy is changed and published.
- **Create presentations and playlists**: yes or no.
- **Media**: *View and use*; *Upload* (also upload, create folders, rename, move and replace
  own files); *Manage* (everything, including deleting and the trash).
- **Products**: *View* or *Edit* (products, categories and base prices apply to every zone).
- **Control devices** of the own zones: restart, reload, screen off.

Global rules, creating and deleting zones, display groups, ticker and presentation templates,
pairing and device settings stay with administrators and operators of all zones. The server
checks every request; what a user may not do is also hidden in the web interface.

Repeated failed logins lock the login for a while; the last administrator cannot be removed.
The audit log records every change.

## Settings

- **General:** language (each user can choose their own), time zone, currency.
- **Defaults:** start values of new playlist entries; of new presentations (format, slide
  duration, background and transition of new slides, font and color of new texts, clocks
  and tickers, product typography – existing presentations and templates keep theirs);
  scaling and other defaults for new devices, central player password, bring devices with
  another version up to date automatically.
- **Certificate:** download the local CA or upload your own certificate.
- **Storage:** what uses the disk, unused media, old revisions.
- **Backup:** database, media (ZIP) or everything; restore with a controlled restart.
- **Updates:** new releases are shown (daily check), installed on click or from an uploaded
  offline bundle; the server updates itself first, then the players zone by zone. A player
  whose new version does not run returns to its previous version by itself; the problems
  list says why ("Update failed").
