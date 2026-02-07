# Distributed TLS-ALPN-01 Challenge Test Environment

This Docker Compose setup tests the distributed ACME TLS-ALPN-01 challenge
behavior when multiple Caddy instances share certificate storage via
caddy-tlsconsul behind a load balancer.

## Architecture

```
                    ┌──────────────┐
                    │    Pebble    │  (ACME CA, 3x multi-perspective validation)
                    │   :14000     │
                    └──────┬───────┘
                           │ validates via challtestsrv DNS → LB
                           ▼
                    ┌──────────────┐
                    │   HAProxy    │  (L4 TLS passthrough, round-robin)
                    │ 10.30.50.20  │
                    └──┬──┬──┬──┬──┘
                       │  │  │  │
              ┌────────┘  │  │  └────────┐
              │     ┌─────┘  └─────┐     │
              ▼     ▼              ▼     ▼
         ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐
         │ Caddy1 │ │ Caddy2 │ │ Caddy3 │ │ Caddy4 │
         │ .50.31 │ │ .50.32 │ │ .50.33 │ │ .50.34 │
         └───┬────┘ └───┬────┘ └───┬────┘ └───┬────┘
             └─────┬─────┴─────┬───┘           │
                   ▼           └───────────────┘
             ┌──────────┐
             │  Consul  │  (shared KV cert storage)
             │ .50.10   │
             └──────────┘
```

## Quick Start

```bash
# Full test: build, start, trigger 5 domain challenges, report stats
./run-test.sh

# Just build the Caddy image
./run-test.sh build

# Watch Caddy logs with challenge annotations
./run-test.sh logs

# Check Consul KV entries
./run-test.sh consul

# Clean up everything
./run-test.sh clean

# Trigger more domains (default: 5)
NUM_DOMAINS=10 ./run-test.sh
```

## What It Tests

The Caddyfile disables HTTP-01, forcing TLS-ALPN-01 only. Each domain
request through the LB triggers on-demand TLS certificate issuance:

1. The Caddy node that receives the request starts ACME issuance
2. It generates a TLS-ALPN-01 challenge cert and stores it in Consul
3. Pebble validates by connecting through the LB (3 perspectives)
4. Each validation request may hit a different Caddy node
5. Non-originating nodes load the challenge cert from Consul (`distributed:true`)

## Expected Output

```
Challenge serves:
  Distributed (from Consul):  15    <-- non-originating nodes served via Consul
  Local (from memory):        0     <-- originating node (might be 0 if all go distributed)

Failures:
  Incorrect validation cert:  0
  No challenge info found:    0

Certificates obtained:        5

Per-node breakdown:
  caddy1: local=0 distributed=0 obtained=5   <-- won the Consul lock, issued all 5
  caddy2: local=0 distributed=5 obtained=0   <-- served challenges from Consul
  caddy3: local=0 distributed=5 obtained=0
  caddy4: local=0 distributed=5 obtained=0
```

## Key Log Patterns

In Caddy logs, look for `"served key authentication certificate"`:

- `"distributed":false` -- challenge cert from **local memory** (originating node)
- `"distributed":true` -- challenge cert from **Consul** (distributed to other node)
- `"Incorrect validation certificate"` -- **BUG**: wrong cert served
- `"no information found to solve challenge"` -- **BUG**: cert not in storage

## Components

| Service      | IP           | Purpose                           |
|-------------|--------------|-----------------------------------|
| consul      | 10.30.50.10  | Shared KV cert storage            |
| challtestsrv| 10.30.50.3   | Mock DNS (resolves all to LB)     |
| pebble      | 10.30.50.2   | Local ACME CA (3x validation)     |
| lb          | 10.30.50.20  | L4 TLS passthrough, round-robin   |
| caddy1-4    | 10.30.50.31-34 | Caddy with caddy-tlsconsul      |

## After Making Plugin Changes

```bash
docker compose build --no-cache caddy1 caddy2 caddy3 caddy4
docker compose down -v
./run-test.sh
```
