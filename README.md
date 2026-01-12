# GeoScale 🌍

A distributed 6DoF spatial data ingestion pipeline, built to learn how streaming systems work at scale.

Simulates a fleet of autonomous vehicles sending real-time telemetry (GPS + orientation) through Kafka into PostgreSQL, with Kubernetes orchestration.

## Architecture

```
┌──────────────────┐       ┌─────────────────────┐       ┌──────────────────┐
│  Go Producer     │       │  Kafka              │       │  Go Consumers    │
│                  │       │  topic:              │       │  (consumer group │
│  5 goroutines    │──────▶│  spatial-events      │──────▶│  geoscale-workers│
│  (1 per vehicle) │       │                     │       │                  │
│                  │       │  ┌─────────────┐    │       │  Pod 0 ← Part 0 │
│  av-000          │       │  │ Partition 0 │    │       │  Pod 1 ← Part 1 │
│  av-001          │       │  │ Partition 1 │    │       │  Pod 2 ← Part 2 │
│  av-002          │       │  │ Partition 2 │    │       │                  │
│  av-003          │       │  └─────────────┘    │       │  validate →      │
│  av-004          │       │                     │       │  measure latency │
│                  │       │  key=vehicle_id      │       │  insert → PG     │
└──────────────────┘       └─────────────────────┘       └────────┬─────────┘
                                                                  │
                                                                  ▼
                                                         ┌──────────────────┐
                                                         │  PostgreSQL      │
                                                         │  spatial_events  │
                                                         └──────────────────┘
```

## What I Learned

- **Kafka internals**: partitioning, consumer groups, offset management, KRaft mode
- **Go concurrency**: goroutines, channels, context cancellation, sync.WaitGroup
- **Data pipeline patterns**: at-least-once delivery, latency measurement, validation at boundaries
- **Infrastructure as code**: Docker Compose, multi-stage Docker builds, Kubernetes deployments
- **Connection pooling**: Go's database/sql pool, prepared statements, connection lifecycle

## Tech Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.22 |
| Message Broker | Apache Kafka (KRaft mode) |
| Database | PostgreSQL 16 |
| Containerization | Docker, multi-stage builds |
| Orchestration | Kubernetes (Minikube) |
| Kafka Client | segmentio/kafka-go |

## Project Structure

```
├── producer/
│   ├── main.go          # AV fleet simulator (goroutine per vehicle)
│   ├── Dockerfile       # Multi-stage → ~10MB scratch image
│   └── go.mod
├── consumer/
│   ├── main.go          # Kafka→Postgres worker with validation
│   ├── Dockerfile
│   └── go.mod
├── k8s/
│   ├── producer-deployment.yaml   # 1 replica
│   └── consumer-deployment.yaml   # 3 replicas (1 per partition)
├── scripts/
│   ├── init.sql              # Postgres schema
│   └── create-topics.sh      # Kafka topic creation
└── docker-compose.yml        # Local dev: Kafka + Postgres
```

## Quick Start

### Prerequisites
- [Go 1.22+](https://go.dev/dl/)
- [Docker Desktop](https://www.docker.com/products/docker-desktop/)

### 1. Start the infrastructure

```bash
docker compose up -d
```

> `-d` = **detached mode**. Runs containers in the background so you get your terminal back. Without it, the terminal gets locked showing container logs and Ctrl+C would stop everything.

Wait ~15 seconds for Kafka to become healthy, then verify:

```bash
docker compose ps -a
```

You should see `kafka` (healthy), `postgres` (healthy), and `kafka-init` (exited 0).

### 2. Start the consumer (Terminal 1)

```bash
cd consumer && go run main.go
```

It'll say "waiting for messages..." — that's expected. The consumer is connected to Kafka and PostgreSQL, ready to process incoming data.

### 3. Start the producer (Terminal 2)

```bash
cd producer && go run main.go
```

The producer starts 5 goroutines (one per simulated vehicle) and sends 6DoF telemetry at 2 Hz each. The consumer terminal will immediately start printing latency measurements:

```
[av-002] inserted | p=0 off=8  | latency=162.949ms
[av-001] inserted | p=0 off=1  | latency=168.724ms
[av-000] inserted | p=2 off=0  | latency=178.324ms
```

### 4. Verify data in PostgreSQL

After running for a few seconds, Ctrl+C both programs and check the database:

```bash
docker compose exec postgres psql -U geoscale -d geoscale \
  -c "SELECT vehicle_id, COUNT(*) FROM spatial_events GROUP BY vehicle_id ORDER BY vehicle_id;"
```

### 5. Inspect messages directly in Kafka

```bash
# See the raw JSON messages stored in the topic
docker compose exec kafka /opt/kafka/bin/kafka-console-consumer.sh \
  --bootstrap-server localhost:9092 \
  --topic spatial-events \
  --from-beginning \
  --max-messages 5 \
  --property print.key=true \
  --property print.partition=true
```

> `--from-beginning` reads ALL stored messages. Without it, only NEW messages (arriving after you start the consumer) are shown. Kafka stores messages on disk — they don't disappear after being consumed.

### 6. Measure pipeline latency from the database

```bash
docker compose exec postgres psql -U geoscale -d geoscale -c "
  SELECT
    MIN(ingested_at - event_timestamp) as min_latency,
    AVG(ingested_at - event_timestamp) as avg_latency,
    MAX(ingested_at - event_timestamp) as max_latency
  FROM spatial_events;"
```

### Teardown

```bash
docker compose down      # stop containers, keep data
docker compose down -v   # stop containers AND delete all data (fresh start)
```

> `-v` deletes Docker volumes (Kafka logs + Postgres data). Use it when you want a completely clean slate.

## Kubernetes Deployment

```bash
# Build images
docker build -t geoscale-producer ./producer
docker build -t geoscale-consumer ./consumer

# Load into Minikube
minikube image load geoscale-producer
minikube image load geoscale-consumer

# Deploy (3 consumer replicas for partition parallelism)
kubectl apply -f k8s/
```

## Demo

### Full pipeline running — producer, consumer, and Kafka messages side by side:

![Pipeline running with producer sending data, consumer inserting into Postgres with latency measurements, and Kafka console showing raw messages](docs/pipeline-running.png)

### Even data distribution across all vehicles in PostgreSQL:

![PostgreSQL query showing 16 events per vehicle, evenly distributed](docs/postgres-data.png)

## Design Decisions

- **vehicle_id as Kafka message key** — guarantees all telemetry from one vehicle lands in the same partition, preserving time-ordering per vehicle
- **Quaternions for orientation** — avoids gimbal lock that Euler angles suffer from
- **3 partitions, 3 consumer replicas** — each consumer handles exactly 1 partition for max parallelism
- **Environment variables for config** — same binary works locally and in K8s without code changes
- **Prepared statements** — single parse/plan per query, prevents SQL injection
- **At-least-once delivery** — consumer commits offsets after DB insert, so crashes cause redelivery (not data loss)

## Measured Results

| Metric | Value |
|--------|-------|
| Pipeline latency (avg) | ~119ms |
| Messages dropped | 0 |
| Events per vehicle | Evenly distributed |
| Docker image size | ~10MB (scratch base) |
