# Build stage - compile from local source
FROM golang:1.24.11-alpine AS builder

WORKDIR /src

# Copy go.mod and go.sum first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the binary for Linux
RUN CGO_ENABLED=0 go build

# Runtime stage
FROM alpine:latest

# ca-certificates for HTTPS; tzdata so TZ=Region/City works (Alpine has no zoneinfo otherwise).
RUN apk --no-cache add ca-certificates tzdata

# Copy the binary from builder
COPY --from=builder /src/tradebot /usr/local/bin/tradebot

# Make sure the binary is executable
RUN chmod +x /usr/local/bin/tradebot

# Set the entrypoint
ENTRYPOINT ["tradebot", "run"]

