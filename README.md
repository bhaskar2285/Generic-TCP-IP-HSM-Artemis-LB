# HSM Transparent Load Balancer

High-throughput transparent TCP load balancer for Thales payShield HSMs, built on ActiveMQ Artemis and Spring Boot.

## Overview

Client applications connect using the standard payShield TCP/IP protocol — no protocol changes required. The stack routes requests across multiple HSM nodes, handles failover, and scales horizontally.

```
Client (TCP)
    │
    ▼
Go EZNet  ──AMQP──▶  Artemis Cluster  ──AMQP──▶  Spring Boot LB  ──TCP──▶  Thales HSM
(TCP-AMQP bridge)    (active-active)               (HSM pool mgmt)
```

## Components

| Component | Technology | Role |
|-----------|------------|------|
| EZNet | Go | TCP-to-AMQP bridge, one per client-facing port |
| Message Broker | ActiveMQ Artemis 2.38 | Active-active cluster, request routing |
| Load Balancer | Spring Boot 3 | HSM connection pool, retry, circuit breaker |
| HSM Simulator | Go | Drop-in payShield simulator for testing |
| Monitoring | Prometheus + Grafana | Full-stack metrics and dashboards |

## Quick Start

Requirements: Docker 24+, Docker Compose v2

```bash
cd docker
docker compose up -d
```

Services started:
- `artemis-master` / `artemis-slave` — Artemis brokers (ports 61616, 61617)
- `lb-1` / `lb-2` — Load balancer instances (ports 8110, 8111)
- `go-eznet-1..5` — EZNet bridges (ports 9110–9114)
- `hsm-sim-1..5` — HSM simulators (internal)
- `prometheus` — Metrics (port 9090)
- `grafana` — Dashboards (port 3000, admin/admin)

## Configuration

Each LB instance is configured via `docker/config/lb-N/application.properties`.

Key settings:

```properties
# HSM nodes (id:host:port:weight)
hsm.lb.nodes=node1:HSM_IP:9000:1,node2:HSM_IP:9000:1

# Load balancing algorithm
hsm.lb.algorithm=ROUND_ROBIN

# JMS consumers
hsm.lb.jms.concurrent-consumers=400

# Socket pool per HSM
hsm.lb.pool.max-total=20
hsm.lb.pool.socket-timeout-ms=10000
```

EZNet is configured via command-line flags — see `go-eznet/main.go` or `docs/PRODUCTION_DEPLOYMENT.txt`.

## Supported HSM Commands

Phase 1 (core):   NO, BM, A0, NC, B2, RA, JA, GM
Phase 2 (data):   M0, M2, M4, M6, M8, A8, EI, KW, KU
AES-256 keyblock: A8kb, B8, CS, BU, A6kb

## Operations

**Health check:**
```bash
curl http://localhost:8110/actuator/health
```

**Node enable/disable (rolling HSM maintenance):**
```bash
curl -X POST http://localhost:8110/api/v1/hsm-lb/nodes/node1/disable
curl -X POST http://localhost:8110/api/v1/hsm-lb/nodes/node1/enable
```

**Circuit breaker reset:**
```bash
curl -X POST http://localhost:8110/api/v1/hsm-lb/nodes/node1/circuit-reset
```

**Run benchmark:**
```bash
cd tests
DUR=15 TPS_LADDER="500 1000 2000 3000" CMD=mix bash bench-hsm-commands-5go-eznet.sh
```

## Performance

Tested on Ubuntu 24.04, 5 HSM nodes, 5 EZNet instances, 2 LB instances:

| TPS | Pass Rate | p95 Latency |
|-----|-----------|-------------|
| 500 | 100% | ~200ms |
| 1000 | 100% | ~3s |
| 2000 | 100% | ~10s |
| 3000 | 99%+ | ~15s |

Ceiling is HSM node count. Each additional HSM node adds ~400–500 TPS capacity.

See `docs/PRODUCTION_DEPLOYMENT.txt` for full production sizing and tuning guide.

## Project Structure

```
docker/
  artemis/          Broker cluster configuration
  config/lb-N/      Per-instance LB application.properties
  hsm-sim/          Go HSM simulator source
  docker-compose.yml
go-eznet/           Go TCP-AMQP bridge source
src/                Spring Boot LB source (Java)
tests/              Benchmark scripts
docs/               Deployment and operations documentation
```
