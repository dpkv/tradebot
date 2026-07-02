#!/bin/sh
# Runs the periodic E*TRADE autologin under a virtual display (Xvfb), since
# headless=false is required -- E*TRADE's Akamai bot detection reliably
# blocks headless=true (see etrade/AUTOLOGIN_PLAN.md). xvfb-run allocates a
# free display number and waits for the X server socket before starting the
# wrapped command.
set -e

exec xvfb-run --auto-servernum --server-args="-screen 0 1280x1024x24" \
    tradebot setup etrade --auto --periodic --headless=false "$@"
