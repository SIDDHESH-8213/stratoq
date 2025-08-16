# StratoQ

A minimal, production-sane distributed job queue (Go) with:
- **API** to enqueue jobs (idempotent), support delayed jobs, fetch job status
- **Worker** that POSTs to your HTTP callback URL
- **Scheduler** that releases delayed & retry jobs
- **Kafka** as the broker, **PostgreSQL** for state
- Retries with exponential backoff + jitter, dead-letter behavior
- Priority topics: `jobs_high`, `jobs_medium`, `jobs_low`
- Simple Docker Compose for local dev

## Quickstart (Local Dev)

Prereqs: Docker, Docker Compose, Go 1.22+

```bash
# 1) Start infra
docker compose -f deploy/docker-compose.yaml up -d

# 2) Export DATABASE_URL for psql / app
export DATABASE_URL=postgres://dev:dev@localhost:5432/stratoq?sslmode=disable

# 3) Apply migrations
psql $DATABASE_URL -f migrations/001_init.sql
psql $DATABASE_URL -f migrations/002_add_callback_url.sql

# 4) (Optional) Ensure topics with 6 partitions each
# The broker has auto-create ON, but that defaults to 1 partition.
docker exec -it stratoq-kafka bash -lc "/opt/bitnami/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic jobs_high --partitions 6 --replication-factor 1"
docker exec -it stratoq-kafka bash -lc "/opt/bitnami/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic jobs_medium --partitions 6 --replication-factor 1"
docker exec -it stratoq-kafka bash -lc "/opt/bitnami/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic jobs_low --partitions 6 --replication-factor 1"

# 5) Run API
KAFKA_BROKERS=127.0.0.1:9092 PORT=8080 go run ./cmd/api

# 6) Run Worker
KAFKA_BROKERS=127.0.0.1:9092 KAFKA_GROUP_ID=stratoq-workers go run ./cmd/worker

# 7) Run Scheduler
KAFKA_BROKERS=127.0.0.1:9092 go run ./cmd/scheduler
```

### Enqueue a job

```bash
curl -s -X POST localhost:8080/v1/jobs  -H 'Content-Type: application/json'  -H 'Idempotency-Key: demo-123'  -d '{
  "callback_url": "http://localhost:9090/echo",
  "payload": {"hello":"world"},
  "priority": 9,
  "max_attempts": 5,
  "delay_seconds": 0,
  "queue": "default"
 }' | jq
```

### Get job status

```bash
curl -s localhost:8080/v1/jobs/<job_id> | jq
```

## Callback semantics

StratoQ performs an HTTP POST to your `callback_url` like:

```json
{
  "job_id": "<uuid>",
  "attempt": 1,
  "payload": { ... }
}
```

Headers:

```
StratoQ-Job-ID: <uuid>
StratoQ-Attempt: <n>
```

- 2xx → success (job marked `succeeded`)
- 408/429/5xx → transient failure (scheduled for retry with backoff)
- Other 4xx → permanent failure (job marked `dead`)

Backoff: `2s * 2^(attempt-1) + jitter(0-500ms)`, capped at 60s.

## Metrics

API exposes Prometheus metrics at `/metrics` (minimal counters in MVP).

## Env Vars

- `DATABASE_URL` (required): e.g. `postgres://dev:dev@localhost:5432/stratoq?sslmode=disable`
- `KAFKA_BROKERS` (default `127.0.0.1:9092`)
- `PORT` (API, default `8080`)
- `KAFKA_GROUP_ID` (worker, default `stratoq-workers`)
- `WORKER_ID` (worker, optional human-readable ID)

## Guarantees

- At-least-once processing (workers may retry; make your callback idempotent)
- Idempotent enqueue via `Idempotency-Key` (optional)
- Delayed jobs & scheduled retries
- Dead-letter in DB (`status=dead`) when attempts exhausted or permanent 4xx

## Troubleshooting

- Kafka advertised listeners are `PLAINTEXT://localhost:9092`. If you run from another container, adjust `KAFKA_CFG_ADVERTISED_LISTENERS`.
- Ensure migrations applied before running services.
- If topics auto-create with 1 partition, explicitly create them with 6 partitions for better parallelism (see step 4).

## Example callback server (Node/Express)

```js
const express = require('express');
const app = express();
app.use(express.json());
app.post('/echo', (req, res) => {
  console.log('Job callback payload:', req.body);
  // Return 200 to succeed; try 500 to trigger retries.
  res.status(200).json({ ok: true });
});
app.listen(9090, () => console.log('callback listening on 9090'));
```
