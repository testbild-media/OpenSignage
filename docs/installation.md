# Installation

OpenSignage has two parts: **one server** in your network (administration, content, updates)
and **one player per screen** (a Raspberry Pi 4 or a small x86 PC with Chromium in kiosk
mode). Both run on Linux and are installed from signed release packages – nothing has to be
built.

## Quick start: one line per machine

```bash
# the server
curl -fsSL https://raw.githubusercontent.com/testbild-media/OpenSignage/main/install.sh | sh -s -- server

# every player (the name is shown in the administration)
curl -fsSL https://raw.githubusercontent.com/testbild-media/OpenSignage/main/install.sh | sh -s -- player --name "Bar north"
```

Without curl use `wget -qO- <same URL> | sh -s -- server`. The script downloads the newest
release for the machine (amd64, arm64 or armv7), **checks its Ed25519 signature** (the same
key the server and the players trust for updates) and the package checksum, and only then
runs the installer from the package (with sudo when not run as root).

| Option | Meaning |
| --- | --- |
| `--version X` | install exactly release X |
| `--download-only` | download and verify only, print the unpacked directory |
| anything else | passed to the package installer, e.g. `--name`, `--server ADDRESS`, `--admin USER` |

The rest of this page explains the installation by hand and what the installers do.

## 1. Server

Requirements: Debian 12/13, Ubuntu 22.04 or newer, or Raspberry Pi OS (64-bit); amd64 or
arm64; systemd; 1 GB free disk space plus room for your media. These ports must be free:

| Port | Purpose |
| --- | --- |
| 80/tcp, 443/tcp | web interface (80 redirects to 443 and serves `/ca.crt`) |
| 7443/tcp | device channel to the players |
| 7444/udp | discovery: players find the server in the LAN |

```bash
tar xzf opensignage-<version>-linux-<arch>.tar.gz
cd opensignage-<version>-linux-<arch>
sudo ./install-server.sh
```

The installer installs `ffmpeg` from the distribution, creates the system user
`opensignage`, installs the version to `/opt/opensignage/releases/<version>` and starts the
service `opensignage-server`. At the end it prints the address of the web interface, e.g.
`https://192.168.1.10/`.

- **First administrator:** the first visit opens the setup wizard; there is no default
  login. Without a browser (headless; the wizard is then locked):
  `sudo ./install-server.sh --admin admin` (asks for the password), or later
  `sudo -u opensignage /opt/opensignage/current/bin/opensignage-server create-admin -u admin`.
- **Forgotten password:**
  `sudo -u opensignage /opt/opensignage/current/bin/opensignage-server reset-password -u admin`
- **Configuration:** `/etc/opensignage/server.env` (ports of the device channel and the
  discovery, log level). The web interface always runs on 80/443. Restart the service after
  a change: `sudo systemctl restart opensignage-server`.
- **Logs:** `journalctl -u opensignage-server` and `/var/lib/opensignage/logs/`.
- **Uninstall:** `sudo ./uninstall-server.sh` keeps the data, `--purge` removes everything.

## 2. Trust the server certificate (tablets and computers)

HTTPS works without a domain: at installation the server creates its own local certificate
authority (CA) and with it the certificate of the web interface. Install the CA **once** per
device that opens the web interface; it stays valid when the server's IP address changes.

1. Open `http://<server-ip>/ca.crt` (or "Download server certificate" on the login page, or
   Settings → Certificate).
2. **iPad / iPhone:** open the file in Safari → Settings → General → VPN & Device
   Management → install the profile. Then Settings → General → About → Certificate Trust
   Settings → enable "OpenSignage Local CA".
3. **Android:** Settings → Security → Encryption & credentials → Install a certificate → CA
   certificate.
4. **Windows:** double-click → Install Certificate → Local Machine → "Trusted Root
   Certification Authorities".
5. **macOS:** double-click → keychain "System" → open the certificate → "Always Trust".

You can also upload your own certificate (internal CA, purchased, VPN provider) under
Settings → Certificate. Players are not affected by this: they use a separate key of the
server that they pin when they are paired (port 7443).

## 3. Players

Requirements: Raspberry Pi OS Lite (64-bit) on a Raspberry Pi 4, or Debian/Ubuntu on an x86
PC; a screen connected via HDMI. No desktop is needed – the player starts its own minimal
graphics session.

```bash
sudo ./install-client.sh                          # finds the server in the LAN by itself
sudo ./install-client.sh --server 192.168.1.10    # fixed server address (other subnet, VPN)
sudo ./install-client.sh --name "Bar north"       # player name without asking
```

The installer asks for the **name of the player**. The host name becomes `osp-<name>`
(e.g. "Bar north" → `osp-bar-north`), and the administration suggests the name when you
take the player over. Pressing Enter keeps the current host name; updates keep the name. To
rename later, run `install-client.sh --name …` again. `sudo ./uninstall-client.sh` removes
exactly what the installer added.

After the start the screen shows a **PIN**, its IP addresses and the address of its own web
interface (`https://<player-ip>:8443`, default password `opensignage`, which must be changed
at the first login).

Ports on the player: 8443/tcp (its web interface) and 7445/udp – the players of a zone
exchange the time over this port when the server cannot be reached (time only, secured with
a key of the zone); a firewall on the players must allow it in the LAN.

**Hardware video decoding** is on by default ("Automatic"): V4L2 on the Raspberry Pi, VA-API
with Vulkan on x86 with Intel or AMD graphics. After repeated decoding errors or player
crashes "Automatic" switches to software decoding until the next restart and shows a warning
in the administration; "On" is never overridden, "Off" always decodes in software. The
installer adds the drivers the distribution offers (`intel-media-va-driver` or
`mesa-va-drivers`, `mesa-vulkan-drivers`; skip with `--no-video-drivers`).

## 4. Take a player over

Web interface → **Devices** → the new screen appears under "New screens waiting to be taken
over" → **Take over** → enter the PIN shown on the screen, choose a name and a zone. For
safety, compare the server key shown in the dialog with the one on the screen.

If the server is not found automatically (VLANs, Wi-Fi with client isolation, VPN), enter
the server's address in the player's web interface under "Server address". If only the
address of the same server changes later, the player stays paired and keeps its content; for
a different server choose "Pair again" there.

## 5. Server outside the local network

OpenSignage is built for a server on site, but the server can also run in a data centre or
a cloud VM – for example one server for several locations. The players then connect over
the internet, through a VPN or directly.

- **Reachable ports on the server:** 443/tcp (web interface) and 7443/tcp (players); 80/tcp
  is optional (redirect and `/ca.crt`). Discovery (7444/udp) only works inside one LAN and is
  not needed.
- **Players:** give them the server's address or host name, optionally with a port:
  `sudo ./install-client.sh --server signage.example.com`, or later in the player's web
  interface under "Server address".
- **Security:** the device channel is TLS with the server key pinned when the player is paired,
  and every player has its own token; a new player only gets content after an administrator
  took it over with the PIN from its screen. For a web interface that is reachable from the
  internet, upload a certificate for its domain (Settings → Certificate) and limit access
  where you can (firewall, VPN).
- **When the connection breaks**, the players keep playing their local copy, including time
  schedules and prices; the players of a zone in the same LAN keep their clocks in step
  with each other.

## 6. Supported hardware

| Player | Status |
| --- | --- |
| Raspberry Pi 4 Model B (Raspberry Pi OS 64-bit Lite) | **supported** – hardware video decoding (V4L2) |
| x86 PC with Intel graphics (tested: Intel Core i3-6100U, HD Graphics 520) | **supported** – hardware video decoding (VA-API), up to 4K |
| x86 virtual machine | works – rendering in software |
| Raspberry Pi 3 Model B (and 2) | **not supported** – see below; it may work with simple content |

AMD and Nvidia graphics and the Raspberry Pi 5 are not tested. The server runs on any of the
systems listed in section 1, including a Raspberry Pi (arm64).

### Raspberry Pi notes

- Use the 64-bit Raspberry Pi OS; Lite is enough. `/boot/firmware/config.txt` must contain
  `dtoverlay=vc4-kms-v3d` (the default of current releases); for 4K on the Pi 4 add
  `hdmi_enable_4kp60=1`.
- **Raspberry Pi 3 and 2 are not supported.** Their hardware video decoder fails about every
  15–20 minutes when videos change often (the player recovers by itself). The installer and
  the device page point this out. If you use one anyway: use the official 5.1 V / 2.5 A power
  supply – under-voltage makes Chromium lose the GPU – and prefer 1280×720 as display mode
  (or `dtoverlay=vc4-kms-v3d,cma-320`), because video memory runs short at 1080p.
- **Videos with 50/60 fps:** the Raspberry Pi 4 cannot show 1080p50/60 smoothly. The server
  therefore also creates a full-HD version at half the frame rate (50 → 25, 60 → 30), which
  the Pi 4 plays automatically. x86 players play the full frame rate when the screen runs
  at 50/60 Hz.
- Without a real-time clock the player takes the server's time, so time schedules switch on
  time even without internet access.

## 7. Updates

Later versions are installed from the web interface (Settings → Updates): new releases are
only shown, nothing installs until you click. Without internet, upload the offline bundle
`opensignage-<version>-bundle.tar` there. The server backs up its database before the update
and returns to the previous version by itself if the new one fails; then it updates the
players one per zone at a time – the players never need internet access. The server and
every player check the release signature themselves.
