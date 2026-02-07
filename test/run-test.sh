#!/bin/bash
# Test script for distributed TLS-ALPN-01 challenge behavior.
#
# Starts 4 Caddy instances behind HAProxy with Consul shared storage,
# then triggers certificate issuance for multiple domains to observe
# how challenges are distributed across nodes.
#
# With 4 nodes, only 1/4 chance the validator hits the originating node.
# Expected failure rate (before fix): ~75% if distribution doesn't work.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"
NUM_DOMAINS=${NUM_DOMAINS:-10}
NODES=4

cleanup() {
    echo ""
    echo "--- Stopping services ---"
    docker compose down 2>/dev/null || true
}

case "${1:-start}" in
    clean)
        echo "--- Cleaning up ---"
        docker compose down -v --remove-orphans 2>/dev/null || true
        echo "Done."
        exit 0
        ;;
    build)
        echo "--- Building Caddy with local caddy-tlsconsul plugin ---"
        docker compose build --no-cache caddy1
        echo "Done."
        exit 0
        ;;
    logs)
        docker compose logs -f caddy1 caddy2 caddy3 caddy4
        exit 0
        ;;
    consul)
        echo "--- Consul KV entries ---"
        curl -s http://localhost:8500/v1/kv/caddytls?keys 2>/dev/null | python3 -m json.tool 2>/dev/null || echo "No entries"
        exit 0
        ;;
    test)
        # Run only the challenge-trigger phase (assumes services are already up)
        ;;
    start|"")
        echo "=== Distributed TLS-ALPN-01 Challenge Test ==="
        echo "    $NODES Caddy nodes behind HAProxy"
        echo "    $NUM_DOMAINS domains to test"
        echo ""

        echo "--- Building and starting services ---"
        docker compose up -d --build --wait
        echo ""
        docker compose ps
        echo ""
        ;;
    *)
        echo "Usage: $0 [start|test|build|clean|logs|consul]"
        echo ""
        echo "  start   - Build, start all services, and trigger challenges (default)"
        echo "  test    - Trigger challenges only (services must be running)"
        echo "  build   - Build Caddy image only"
        echo "  clean   - Stop and remove everything"
        echo "  logs    - Follow Caddy logs"
        echo "  consul  - Show Consul KV entries"
        echo ""
        echo "Environment variables:"
        echo "  NUM_DOMAINS=N  - Number of domains to test (default: 10)"
        exit 1
        ;;
esac

echo ""
echo "--- Waiting for all Caddy nodes to be ready ---"
for node in caddy1 caddy2 caddy3 caddy4; do
    echo -n "  $node... "
    for attempt in $(seq 1 30); do
        if docker compose exec -T "$node" curl -sf http://localhost:8080/health >/dev/null 2>&1; then
            echo "ready"
            break
        fi
        if [ "$attempt" -eq 30 ]; then
            echo "TIMEOUT"
        fi
        sleep 1
    done
done
# Brief pause for HAProxy to detect all backends on :443
sleep 3
echo ""
echo "--- Triggering certificate issuance for $NUM_DOMAINS domains ---"
echo "    (each domain triggers a fresh TLS-ALPN-01 challenge)"
echo ""

RESULTS_DIR=$(mktemp -d)

# Phase 1: Sequential requests
echo "Phase 1: Sequential requests"
SUCCESS=0
FAIL=0
for i in $(seq 1 "$NUM_DOMAINS"); do
    domain="test${i}.example.com"
    echo -n "  Requesting cert for $domain... "
    HTTP_CODE=$(curl -sk --resolve "$domain:8443:127.0.0.1" \
        --max-time 120 -o /dev/null -w '%{http_code}' \
        "https://$domain:8443/" 2>&1) || true
    if [ "$HTTP_CODE" = "200" ]; then
        echo "OK (HTTP $HTTP_CODE)"
        SUCCESS=$((SUCCESS + 1))
    else
        echo "FAIL (HTTP $HTTP_CODE)"
        FAIL=$((FAIL + 1))
    fi
    sleep 2
done

# Phase 2: Parallel burst - fire 4 requests simultaneously to create contention
echo ""
echo "Phase 2: Parallel burst (4 simultaneous requests)"
PARALLEL_START=$((NUM_DOMAINS + 1))
PARALLEL_END=$((NUM_DOMAINS + 4))
for i in $(seq "$PARALLEL_START" "$PARALLEL_END"); do
    domain="test${i}.example.com"
    (
        HTTP_CODE=$(curl -sk --resolve "$domain:8443:127.0.0.1" \
            --max-time 120 -o /dev/null -w '%{http_code}' \
            "https://$domain:8443/" 2>&1) || true
        echo "$HTTP_CODE" > "$RESULTS_DIR/$domain"
    ) &
done
wait

for i in $(seq "$PARALLEL_START" "$PARALLEL_END"); do
    domain="test${i}.example.com"
    HTTP_CODE=$(cat "$RESULTS_DIR/$domain" 2>/dev/null || echo "000")
    if [ "$HTTP_CODE" = "200" ]; then
        echo "  $domain: OK (HTTP $HTTP_CODE)"
        SUCCESS=$((SUCCESS + 1))
    else
        echo "  $domain: FAIL (HTTP $HTTP_CODE)"
        FAIL=$((FAIL + 1))
    fi
done
rm -rf "$RESULTS_DIR"

TOTAL=$((NUM_DOMAINS + 4))

echo ""
echo "--- Results: $SUCCESS/$TOTAL succeeded, $FAIL/$TOTAL failed ---"
echo ""

echo "--- Challenge Distribution Report ---"
echo ""

# Collect log data
LOGFILE=$(mktemp)
docker compose logs caddy1 caddy2 caddy3 caddy4 2>&1 > "$LOGFILE"

# Count distributed vs local challenge serves
DISTRIBUTED_TRUE=$(grep -c '"distributed":true' "$LOGFILE" || true)
DISTRIBUTED_FALSE=$(grep -c '"distributed":false' "$LOGFILE" || true)
CERT_OBTAINED=$(grep -c 'certificate obtained successfully' "$LOGFILE" || true)
CHALLENGE_FAILED=$(grep -c 'challenge failed' "$LOGFILE" || true)
INCORRECT_CERT=$(grep -c 'Incorrect validation certificate' "$LOGFILE" || true)
NO_INFO=$(grep -c 'no information found to solve challenge' "$LOGFILE" || true)

echo "Challenge serves:"
echo "  Distributed (from Consul):  $DISTRIBUTED_TRUE"
echo "  Local (from memory):        $DISTRIBUTED_FALSE"
echo ""
echo "Certificates obtained:        $CERT_OBTAINED"
echo "Challenges failed:            $CHALLENGE_FAILED"
echo "Incorrect validation cert:    $INCORRECT_CERT"
echo "No challenge info found:      $NO_INFO"
echo ""

# Show which nodes served which challenges
echo "Per-node challenge serves:"
for node in caddy1 caddy2 caddy3 caddy4; do
    local_count=$(grep "$node" "$LOGFILE" | grep -c '"distributed":false' || true)
    remote_count=$(grep "$node" "$LOGFILE" | grep -c '"distributed":true' || true)
    cert_count=$(grep "$node" "$LOGFILE" | grep -c 'certificate obtained successfully' || true)
    echo "  $node: local=$local_count distributed=$remote_count obtained=$cert_count"
done

echo ""
echo "Expected behavior with $NODES nodes:"
echo "  With fix:    100% of challenges should succeed"
echo "  Without fix: ~$((100 * (NODES - 1) / NODES))% of attempts need distributed serving"
echo ""

rm -f "$LOGFILE"

echo "--- Test complete ---"
echo ""
echo "To see full logs: $0 logs"
echo "To check Consul:  $0 consul"
echo "To clean up:      $0 clean"
