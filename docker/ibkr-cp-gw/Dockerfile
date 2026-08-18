# IBKR Client Portal Gateway (from third-party/ibkr/clientportal.gw.zip), plus
# a login bot (login.py) that drives a real browser through IBKR's login/2FA
# flow and keeps the session alive. entrypoint.sh runs the gateway under a
# respawn loop and the login bot in the foreground; the login bot restarts the
# gateway process (not the container) when the session can't be recovered.
# Host access via docker -p hits the gateway as a Docker bridge IP (e.g. 172.17.0.1), not 127.0.0.1;
# root/conf.yaml ips.allow is patched below so those clients are not rejected.
# Map container 5000 to host 3000 when running, e.g.:
#   docker build -f Dockerfile.ibkr-gateway -t ibkr-cp-gw .
#   docker run -p 3000:5000 ibkr-cp-gw

FROM eclipse-temurin:11-jre-jammy

RUN apt-get update \
    && apt-get install -y --no-install-recommends unzip procps python3 python3-pip xvfb \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/ibkr-gateway

COPY third-party/ibkr/clientportal.gw.zip /tmp/clientportal.gw.zip
RUN unzip -q /tmp/clientportal.gw.zip -d /opt/ibkr-gateway \
    && rm /tmp/clientportal.gw.zip \
    && chmod +x bin/run.sh \
    && sed -i '/^      allow:$/a\        - 172.*' root/conf.yaml \
    && sed -i '/^      allow:$/a\        - 10.*' root/conf.yaml

COPY docker/ibkr-cp-gw/requirements.txt /opt/ibkr-gateway/requirements.txt
RUN pip3 install --no-cache-dir -r requirements.txt \
    && python3 -m playwright install --with-deps chromium

COPY docker/ibkr-cp-gw/login.py /opt/ibkr-gateway/login.py
COPY docker/ibkr-cp-gw/entrypoint.sh /opt/ibkr-gateway/entrypoint.sh
RUN chmod +x /opt/ibkr-gateway/entrypoint.sh

EXPOSE 5000

ENTRYPOINT ["/opt/ibkr-gateway/entrypoint.sh"]
