# IBKR Client Portal Gateway (from third-party/ibkr/clientportal.gw.zip).
# Host access via docker -p hits the gateway as a Docker bridge IP (e.g. 172.17.0.1), not 127.0.0.1;
# root/conf.yaml ips.allow is patched below so those clients are not rejected.
# Map container 5000 to host 3000 when running, e.g.:
#   docker build -f Dockerfile.ibkr-gateway -t ibkr-cp-gw .
#   docker run -p 3000:5000 ibkr-cp-gw

FROM eclipse-temurin:11-jre-jammy

RUN apt-get update \
    && apt-get install -y --no-install-recommends unzip \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/ibkr-gateway

COPY third-party/ibkr/clientportal.gw.zip /tmp/clientportal.gw.zip
RUN unzip -q /tmp/clientportal.gw.zip -d /opt/ibkr-gateway \
    && rm /tmp/clientportal.gw.zip \
    && chmod +x bin/run.sh \
    && sed -i '/^      allow:$/a\        - 172.*' root/conf.yaml \
    && sed -i '/^      allow:$/a\        - 10.*' root/conf.yaml

EXPOSE 5000

ENTRYPOINT ["bin/run.sh", "root/conf.yaml"]
