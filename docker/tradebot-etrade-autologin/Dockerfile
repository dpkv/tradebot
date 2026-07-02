# Build stage - compile from local source. Uses the same Alpine builder as
# docker/tradebot/Dockerfile for the tradebot binary; only the runtime stage
# differs, since this image additionally needs a real (virtual) display and
# Chromium for Playwright's headless=false browser automation.
FROM golang:1.24.11-alpine AS builder

WORKDIR /src

# Copy go.mod and go.sum first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the tradebot binary for Linux, plus the playwright-go CLI (used
# below, at build time, to install Chromium -- keeping this a plain `go
# build` from our own go.mod means the driver/browser versions always match
# what etrade/autologin actually links against).
RUN CGO_ENABLED=0 go build -o /out/tradebot .
RUN CGO_ENABLED=0 go build -o /out/playwright github.com/playwright-community/playwright-go/cmd/playwright

# Runtime stage. debian:bookworm-slim (not Alpine) because Playwright's
# Chromium download only officially supports glibc, not Alpine's musl --
# see etrade/AUTOLOGIN_PLAN.md.
FROM debian:bookworm-slim

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata \
        xvfb \
        xauth \
        x11vnc \
    && rm -rf /var/lib/apt/lists/*

# x11vnc lets a human watch/control the virtual display from outside the
# container -- needed because "remember this device" turned out to depend
# on more than shared cookies (see etrade/AUTOLOGIN_PLAN.md): a login
# challenge inside this container's Chromium can't be completed any other
# way, since Xvfb itself has no viewer. EXPOSE here is documentation only
# (it doesn't control what actually gets published); the real port is
# entrypoint.sh's VNC_PORT env var (default 5900, same default as this
# ARG) together with docker-compose.etrade.yml's `ports:` mapping -- all
# three must agree if ever changed from the default.
ARG VNC_PORT=5900
EXPOSE $VNC_PORT

COPY --from=builder /out/tradebot /usr/local/bin/tradebot
COPY --from=builder /out/playwright /usr/local/bin/playwright
RUN chmod +x /usr/local/bin/tradebot /usr/local/bin/playwright

# Bake Chromium + its runtime shared-library dependencies into the image at
# build time (--with-deps apt-get installs those deps), so containers don't
# re-download the browser on every start.
RUN playwright install --with-deps chromium && rm /usr/local/bin/playwright

COPY docker/tradebot-etrade-autologin/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
