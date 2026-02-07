#!/bin/sh
# Wait for pebble to be ready, then start Caddy.
# The pebble minica root CA is already baked into the Docker image
# via update-ca-certificates in the Dockerfile.
set -e

PEBBLE_URL="https://pebble:14000/dir"
MAX_RETRIES=30
RETRY_INTERVAL=2

echo "Waiting for pebble ACME server to become ready..."

i=0
while [ $i -lt $MAX_RETRIES ]; do
    if curl -sk "$PEBBLE_URL" -o /dev/null 2>/dev/null; then
        echo "Pebble is ready."
        break
    fi
    i=$((i + 1))
    echo "Attempt $i/$MAX_RETRIES - pebble not ready yet, retrying in ${RETRY_INTERVAL}s..."
    sleep $RETRY_INTERVAL
done

if [ $i -eq $MAX_RETRIES ]; then
    echo "ERROR: pebble did not become ready after $MAX_RETRIES attempts"
    exit 1
fi

echo "Starting Caddy..."
exec caddy run --config /etc/caddy/Caddyfile
