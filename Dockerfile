# ──────────────────────────────────────────────────────────────
# Stage 1: Build the Go binary
# ──────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git gcc musl-dev

WORKDIR /build

# Cache module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy source and build
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /haruki-builder .

# ──────────────────────────────────────────────────────────────
# Stage 2: Final runtime image
# ──────────────────────────────────────────────────────────────
FROM alpine:3.20

# Install runtime dependencies
RUN apk add --no-cache \
    git \
    ca-certificates \
    tzdata \
    caddy \
    tini

# Copy the Go binary
COPY --from=builder /haruki-builder /usr/local/bin/haruki-builder

# Copy Caddy config
COPY Caddyfile /etc/caddy/Caddyfile

# Create data directories
RUN mkdir -p /data/repo /data/serve

# Create entrypoint script
RUN cat > /entrypoint.sh << 'SCRIPT'
#!/bin/sh
set -e

echo "=== Starting Haruki Static Server ==="
echo "Starting time: $(date -u '+%Y-%m-%d %H:%M:%S UTC')"

# Start the builder (clones, compresses, watches) in background
echo "Starting builder..."
haruki-builder &
BUILDER_PID=$!

# Wait for initial clone and compression to complete
# by watching for the serve directory to be populated
echo "Waiting for initial build to complete..."
TIMEOUT=600
ELAPSED=0
while [ ! -f /data/serve/.git_synced ] && [ $ELAPSED -lt $TIMEOUT ]; do
# Check if builder is still running
if ! kill -0 $BUILDER_PID 2>/dev/null; then
echo "ERROR: Builder process died unexpectedly"
exit 1
fi
sleep 2
ELAPSED=$((ELAPSED + 2))
done

# Give a moment for compression to start
sleep 5

# Start Caddy
echo "Starting Caddy web server..."
caddy run --config /etc/caddy/Caddyfile &
CADDY_PID=$!

echo "=== All services started ==="
echo "Builder PID: $BUILDER_PID"
echo "Caddy PID: $CADDY_PID"

# Handle signals gracefully
cleanup() {
echo "Shutting down..."
kill $BUILDER_PID 2>/dev/null || true
kill $CADDY_PID 2>/dev/null || true
wait
exit 0
}

trap cleanup SIGTERM SIGINT

# Wait for any process to exit
wait -n $BUILDER_PID $CADDY_PID
EXIT_CODE=$?

echo "A process exited with code $EXIT_CODE, shutting down..."
cleanup
SCRIPT

RUN chmod +x /entrypoint.sh

# Expose port 80
EXPOSE 80

# Resource limits via labels (for documentation)
LABEL maintainer="haruki-builder" \
    description="Auto-updating static file server with precompression"

# Use tini as init system for proper signal handling
ENTRYPOINT ["tini", "--"]
CMD ["/entrypoint.sh"]