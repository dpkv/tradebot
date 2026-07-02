#!/bin/sh
# Runs the periodic E*TRADE autologin under a virtual display (Xvfb) with a
# VNC server attached, since headless=false is required -- E*TRADE's Akamai
# bot detection reliably blocks headless=true (see
# etrade/AUTOLOGIN_PLAN.md). VNC exists because "remember this device"
# turned out to depend on more than shared cookies (confirmed 2026-07-02):
# a login challenge inside this container's Chromium has no other way to
# be completed by a human, since Xvfb itself has no viewer. Connect from
# macOS with the built-in Screen Sharing app -- see Makefile.etrade's
# docker-etrade-vnc target (VNC_PORT there must match the one here, e.g.
# via the same VNC_PORT env var passed through docker-compose.etrade.yml).
#
# Starts Xvfb directly rather than via xvfb-run: xvfb-run's own
# readiness-wait logic was observed hanging indefinitely in this image
# (Xvfb itself started fine, but xvfb-run never handed off to the wrapped
# command). Polling for the X11 socket ourselves is simple and was verified
# working when driven manually during development.
set -e

DISPLAY_NUM=99
VNC_PORT="${VNC_PORT:-5900}"

Xvfb :$DISPLAY_NUM -screen 0 1280x1024x24 -nolisten tcp &
export DISPLAY=:$DISPLAY_NUM

for i in $(seq 1 50); do
    if [ -e /tmp/.X11-unix/X$DISPLAY_NUM ]; then
        break
    fi
    sleep 0.1
done

# VNC_PASSWORD is optional -- the port is bound to 127.0.0.1 only in
# docker-compose.etrade.yml, so -nopw is the default. Set VNC_PASSWORD if
# that port mapping is ever changed to something less trusted.
if [ -n "$VNC_PASSWORD" ]; then
    x11vnc -display :$DISPLAY_NUM -forever -shared -rfbport "$VNC_PORT" -passwd "$VNC_PASSWORD" &
else
    x11vnc -display :$DISPLAY_NUM -forever -shared -rfbport "$VNC_PORT" -nopw &
fi

exec tradebot setup etrade --auto --periodic --headless=false "$@"
