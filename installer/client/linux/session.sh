#!/bin/sh
# OpenSignage kiosk X session (started by opensignage-display.service via xinit).
# Keeps the X server alive without a desktop or window manager; the agent starts
# Chromium itself and re-applies the saved display mode.
set -u

# Never blank or power down the screen.
xset s off || true
xset -dpms || true
xset s noblank || true

# Allow the service user's local clients (agent, Chromium) to connect.
xhost +SI:localuser:opensignage >/dev/null 2>&1 || true

exec sleep infinity
