package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/SIDDHESH-8213/stratoq/internal/db"
	"github.com/SIDDHESH-8213/stratoq/internal/domain"
	"github.com/SIDDHESH-8213/stratoq/internal/kafka"
)

var (
	metricEnqueued = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "stratoq",
		Subsystem: "api",
		Name:      "jobs_enqueued_total",
		Help:      "Total jobs enqueued via API",
	})
)

func main() {
	prometheus.MustRegister(metricEnqueued)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx)
	if err != nil {
		log.Fatalf("db pool: %v", err)
	}
	defer pool.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/v1/jobs", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handleEnqueue(ctx, pool, w, r)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/v1/jobs/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/jobs/")
		if id == "" {
			http.Error(w, "missing id", http.StatusBadRequest)
			return
		}
		handleGetJob(ctx, pool, w, r, id)
	})

	addr := ":" + getEnv("PORT", "8080")
	srv := &http.Server{
		Addr:              addr,
		Handler:           logReq(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	log.Printf("API listening on %s", addr)

	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("shutting down API...")
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutCtx)
}

func handleEnqueue(ctx context.Context, pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var req domain.EnqueueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}

	if req.Queue == "" {
		req.Queue = "default"
	}
	if req.Priority == 0 {
		req.Priority = 5
	}
	if req.MaxAttempts <= 0 || req.MaxAttempts > 20 {
		req.MaxAttempts = 5
	}
	if req.CallbackURL == "" {
		http.Error(w, "callback_url is required", http.StatusBadRequest)
		return
	}
	if !validCallback(req.CallbackURL) {
		http.Error(w, "callback_url must be http or https", http.StatusBadRequest)
		return
	}
	if req.Payload == nil {
		req.Payload = map[string]any{}
	}

	idempotencyKey := r.Header.Get("Idempotency-Key")
	// Fast path: if Idempotency-Key is present, try to fetch existing
	if idempotencyKey != "" {
		if job, ok := getJobByIdempotency(ctx, pool, idempotencyKey); ok {
			job.IdempotentHit = true
			writeJSON(w, http.StatusOK, job)
			return
		}
	}

	jobID := uuid.New().String()
	now := time.Now().UTC()
	status := "pending"
	if req.DelaySeconds > 0 {
		status = "pending" // still pending, but not yet published
	}
	payloadBytes, _ := json.Marshal(req.Payload)

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		http.Error(w, "db begin failed", http.StatusInternalServerError)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Insert job
	_, err = tx.Exec(ctx, `
		INSERT INTO jobs (id, idempotency_key, queue, priority, payload, callback_url, status, attempts, max_attempts, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,0,$8,$9,$9)
	`, jobID, nullIfEmpty(idempotencyKey), req.Queue, req.Priority, payloadBytes, req.CallbackURL, status, req.MaxAttempts, now)
	if err != nil {
		// Unique violation on idempotency key: fetch and return
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.ConstraintName == "jobs_idempotency_key_key" {
			if job, ok := getJobByIdempotency(ctx, pool, idempotencyKey); ok {
				job.IdempotentHit = true
				writeJSON(w, http.StatusOK, job)
				return
			}
		}
		http.Error(w, fmt.Sprintf("insert job failed: %v", err), http.StatusInternalServerError)
		return
	}

	// Delay handling: insert schedule if requested
	if req.DelaySeconds > 0 {
		due := now.Add(time.Duration(req.DelaySeconds) * time.Second)
		if _, err := tx.Exec(ctx, `INSERT INTO schedules (job_id, due_at, released) VALUES ($1,$2,false)`, jobID, due); err != nil {
			http.Error(w, fmt.Sprintf("schedule insert failed: %v", err), http.StatusInternalServerError)
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "db commit failed", http.StatusInternalServerError)
		return
	}

	// Publish immediately only if no delay
	if req.DelaySeconds == 0 {
		topic := domain.TopicForPriority(req.Priority)
		if err := kafka.PublishJobID(ctx, topic, jobID); err != nil {
			// Best effort: update next_run_at to retry via scheduler soon
			log.Printf("kafka publish failed for job %s: %v", jobID, err)
		}
	}

	metricEnqueued.Inc()

	// Return job representation
	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":           jobID,
		"queue":        req.Queue,
		"priority":     req.Priority,
		"status":       status,
		"attempts":     0,
		"max_attempts": req.MaxAttempts,
		"next_run_at":  nil,
		"created_at":   now,
		"updated_at":   now,
		"idempotent":   false,
	})
}

func handleGetJob(ctx context.Context, pool *pgxpool.Pool, w http.ResponseWriter, r *http.Request, id string) {
	row := pool.QueryRow(ctx, `
		SELECT id, queue, priority, status, attempts, max_attempts, next_run_at, created_at, updated_at, callback_url, payload
		FROM jobs WHERE id=$1
	`, id)

	var job domain.Job
	var payloadBytes []byte
	var cb *string
	if err := row.Scan(&job.ID, &job.Queue, &job.Priority, &job.Status, &job.Attempts, &job.MaxAttempts, &job.NextRunAt, &job.CreatedAt, &job.UpdatedAt, &cb, &payloadBytes); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	job.CallbackURL = cb
	if len(payloadBytes) > 0 {
		_ = json.Unmarshal(payloadBytes, &job.Payload)
	}
	writeJSON(w, http.StatusOK, job)
}

func getJobByIdempotency(ctx context.Context, pool *pgxpool.Pool, key string) (domain.Job, bool) {
	row := pool.QueryRow(ctx, `
		SELECT id, queue, priority, status, attempts, max_attempts, next_run_at, created_at, updated_at, callback_url, payload
		FROM jobs WHERE idempotency_key=$1
	`, key)

	var job domain.Job
	var payloadBytes []byte
	var cb *string
	if err := row.Scan(&job.ID, &job.Queue, &job.Priority, &job.Status, &job.Attempts, &job.MaxAttempts, &job.NextRunAt, &job.CreatedAt, &job.UpdatedAt, &cb, &payloadBytes); err != nil {
		return domain.Job{}, false
	}
	job.CallbackURL = cb
	if len(payloadBytes) > 0 {
		_ = json.Unmarshal(payloadBytes, &job.Payload)
	}
	return job, true
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}

func validCallback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func logReq(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
