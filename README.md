# StratoQ

StratoQ is a lightweight **distributed job queue** built with Go, PostgreSQL, and Kafka.  
It’s designed as a **minimal but production-sane MVP**, perfect for learning, prototyping, or extending into a full system.

---

# ✨ Features

- **Job Enqueue API**  
  - Idempotent (via `Idempotency-Key` header)  
  - Supports delayed jobs  
  - Priority queues (`high`, `medium`, `low`)  

- **Workers**  
  - Execute jobs by calling user-provided HTTP `callback_url`  
  - At-least-once semantics with safe row locking  
  - Handles success, retry, and permanent failure automatically  

- **Scheduler**  
  - Releases delayed jobs  
  - Retries failed jobs with exponential backoff + jitter  
  - Dead-letter handling for maxed-out retries  

- **State Management**  
  - PostgreSQL stores job state, attempts, and schedules  
  - Full job lifecycle tracking: `pending → running → succeeded/failed/dead`  

- **Infra Ready**  
  - Kafka (Bitnami single-node KRaft)  
  - PostgreSQL 15  
  - Docker Compose for local setup  
  - Graceful shutdown of API, worker, and scheduler  

---

# 📦 Project Structure

```
stratoq/
  cmd/
    api/          # REST API service
    worker/       # Worker service
    scheduler/    # Scheduler service
  internal/
    db/           # DB pool
    kafka/        # Kafka reader/writer
    domain/       # Core types
  migrations/     # SQL migrations
  deploy/         # docker-compose.yaml
  Makefile
  README.md
  LICENSE
```

---

# 🚀 Quickstart

## 1. Clone the repository
```bash
git clone https://github.com/SIDDHESH-8213/stratoq.git
cd stratoq
```

## 2. Start dependencies
```bash
docker compose -f deploy/docker-compose.yaml up -d
```

This runs:
- **Postgres** → on `localhost:5432` (user: `dev`, pass: `dev`, db: `stratoq`)  
- **Kafka** → on `localhost:9092`  

## 3. Run migrations
```bash
cat migrations/001_init.sql | docker exec -i <postgres-container-name> psql -U dev -d stratoq
cat migrations/002_add_callback_url.sql | docker exec -i <postgres-container-name> psql -U dev -d stratoq
```

## 4. Run services
In separate terminals:

```bash
# API
DATABASE_URL=postgres://dev:dev@localhost:5432/stratoq?sslmode=disable KAFKA_BROKERS=localhost:9092 go run ./cmd/api

# Worker
DATABASE_URL=postgres://dev:dev@localhost:5432/stratoq?sslmode=disable KAFKA_BROKERS=localhost:9092 go run ./cmd/worker

# Scheduler
DATABASE_URL=postgres://dev:dev@localhost:5432/stratoq?sslmode=disable KAFKA_BROKERS=localhost:9092 go run ./cmd/scheduler
```

---

# 🔗 API Usage

## Health Check
```bash
curl http://localhost:8080/v1/healthz
# → ok
```

## Enqueue a Job
```bash
curl -X POST http://localhost:8080/v1/jobs   -H "Content-Type: application/json"   -H "Idempotency-Key: my-key-123"   -d '{
        "priority": 7,
        "max_attempts": 3,
        "callback_url": "http://localhost:9000/worker",
        "payload": {"msg": "hello"}
      }'
```

Response:
```json
{"job_id":"<uuid>","status":"pending"}
```

## Get Job Status
```bash
curl http://localhost:8080/v1/jobs/<uuid>
```

---

# 👩‍💻 Example Worker

You provide a simple HTTP server. StratoQ workers will call it:

```js
// example-worker.js
const express = require("express");
const app = express();
app.use(express.json());

app.post("/worker", (req, res) => {
  const { job_id, attempt, payload } = req.body;
  console.log("Got job:", job_id, "attempt:", attempt, "payload:", payload);

  // Simulate work
  if (Math.random() < 0.7) {
    return res.status(200).json({ ok: true }); // success
  } else {
    return res.status(500).json({ error: "random fail" }); // retry
  }
});

app.listen(9000, () => console.log("Worker listening on :9000"));
```

- `2xx` → job marked as **succeeded**  
- `408/429/5xx` → **retry later** (exponential backoff)  
- `4xx (non-retriable)` → job marked as **dead**  

---

# 📊 Metrics

StratoQ exposes `/metrics` endpoint (Prometheus-compatible).  
Grafana dashboards can be layered on top — see `deploy/` for hints.  

---

# 🗂 Database Schema

Key tables:
- **jobs** → core job state  
- **job_attempts** → history of each try  
- **schedules** → delayed & retry queue  
- **idempotency_keys** → ensures enqueue is safe  

Migrations: `migrations/001_init.sql`, `002_add_callback_url.sql`.

---

# 🛡 Guarantees

- At-least-once delivery  
- Idempotent enqueue  
- Exponential backoff retries  
- Dead-letter after `max_attempts`  
- Safe for multiple workers (row locking with `SKIP LOCKED`)  

---

# 📖 Development

```bash
make up         # start infra
make down       # stop infra
make api        # run API
make worker     # run worker
make scheduler  # run scheduler
```

---

# 📦 Packaging & Release

- **Docker images**: build with `docker build`  
- **GitHub Actions**: add `.github/workflows/release.yml` to push to GHCR/DockerHub  
- **Distribute**: publish repo, others just run `docker compose` + binaries  

---

# 📝 License

MIT License.  
Contributions welcome — feel free to fork, PR, or open issues.

---

# 🎯 Why StratoQ?

This project demonstrates My:
- Core backend skills: distributed queues, idempotency, retries, DB + Kafka integration  
- Clean and modular Go code  
- Infrastructure awareness: Docker, Kafka, PostgreSQL  
- Production-thinking (graceful shutdowns, backoff, metrics)  

