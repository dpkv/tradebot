#!/usr/bin/env bash
# Runs the CP Gateway under a respawn loop and the login bot in the foreground.
# login.py restarts the gateway by killing the GatewayStart java process; this
# loop notices the exit and relaunches bin/run.sh, so only the gateway process
# restarts rather than the whole container.
set -euo pipefail

run_gateway_loop() {
    cd /opt/ibkr-gateway
    while true; do
        bin/run.sh root/conf.yaml
        echo "CP Gateway exited (code $?), restarting in 2s..."
        sleep 2
    done
}

run_gateway_loop &

Xvfb :99 -screen 0 1280x1024x24 &
export DISPLAY=:99

exec python3 -u /opt/ibkr-gateway/login.py
