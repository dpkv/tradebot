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

# A prior Xvfb instance's lock/socket can survive a container restart (the
# writable filesystem persists across --restart policy restarts), which
# would otherwise make this Xvfb fail to bind display :99.
rm -f /tmp/.X99-lock /tmp/.X11-unix/X99

Xvfb :99 -screen 0 1280x1024x24 &
export DISPLAY=:99

# Backgrounding Xvfb doesn't mean it's ready yet; wait for its socket before
# starting the browser, or the first launch races Xvfb's own startup.
for i in $(seq 1 50); do
    [ -e /tmp/.X11-unix/X99 ] && break
    sleep 0.1
done

exec python3 -u /opt/ibkr-gateway/login.py
