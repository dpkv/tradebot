# Build stage
FROM golang:1.23-alpine AS builder

# Install the binary
RUN go install github.com/bvk/tradebot@latest

# Runtime stage
FROM alpine:latest

# Install ca-certificates for HTTPS requests
RUN apk --no-cache add ca-certificates

# Copy the binary from builder
COPY --from=builder /go/bin/tradebot /usr/local/bin/tradebot

# Make sure the binary is executable
RUN chmod +x /usr/local/bin/tradebot

# Set the entrypoint
ENTRYPOINT ["tradebot", "run"]
