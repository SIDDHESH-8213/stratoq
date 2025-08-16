package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/segmentio/kafka-go"

	"github.com/SIDDHESH-8213/stratoq/internal/db"
	kaf "github.com/SIDDHESH-8213/stratoq/internal/kafka"
)

type jobRow struct {
	ID          string
	Status      string
	Attempts    int
	MaxAttempts int
	CallbackURL string
	Payload     []byte
	Priority    int16
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	rand.Seed(time.Now().UnixNano())

	pool, err := db.NewPool(ctx)
	if err != nil {
		log.Fatalf("db pool: %v", err)
	}
	defer pool.Close()

	group := env("KAFKA_GROUP_ID", "stratoq-workers")
	readerHigh := kaf.NewReader("jobs_high", group)
	readerMed := kaf.NewReader("jobs_medium", group)
	readerLow := kaf.NewReader("jobs_low", group)
	defer readerHigh.Close()
	defer readerMed.Close()
	defer readerLow.Close()

	// fan-in messages from three topics
	msgCh := make(chan kafka.Message, 1024)
	errCh := make(chan error, 1)

	go consume(ctx, readerHigh, msgCh, errCh)
	go consume(ctx, readerMed, msgCh, errCh)
	go consume(ctx, readerLow, msgCh, errCh)

	httpTimeout := 15 * time.Second
	client := &http.Client{Timeout: httpTimeout}

	workerID := env("WORKER_ID", fmt.Sprintf("worker-%d", os.Getpid()))
	log.Printf("worker started (id=%s)", workerID)

	for {
		select {
		case <-ctx.Done():
			log.Printf("worker shutting down...")
			return
		case err := <-errCh:
			log.Printf("kafka error: %v", err)
			time.Sleep(2 * time.Second)
		case msg := <-msgCh:
			jobID := string(msg.Value)
			if err := processJob(ctx, pool, client, workerID, jobID); err != nil {
				log.Printf("process job %s error: %v", jobID, err)
			}
		}
	}
}

func consume(ctx context.Context, r *kafka.Reader, out chan<- kafka.Message, errs chan<- error) {
	for {
		m, err := r.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			errs <- err
			time.Sleep(time.Second)
			continue
		}
		out <- m
		if err := r.CommitMessages(ctx, m); err != nil {
			errs <- err
		}
	}
}

func processJob(ctx context.Context, pool *pgxpool.Pool, client *http.Client, workerID, jobID string) error {
	// Lock and load the job
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var row jobRow
	err = tx.QueryRow(ctx, `
		SELECT id, status, attempts, max_attempts, callback_url, payload, priority
		FROM jobs
		WHERE id=$1
		FOR UPDATE SKIP LOCKED
	`, jobID).Scan(&row.ID, &row.Status, &row.Attempts, &row.MaxAttempts, &row.CallbackURL, &row.Payload, &row.Priority)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // job already gone or locked by another worker
		}
		return fmt.Errorf("select job: %w", err)
	}

	if row.Status == "succeeded" || row.Status == "dead" {
		// nothing to do
		return tx.Commit(ctx)
	}

	attemptNo := row.Attempts + 1
	// Mark running & create attempt record
	var attemptID int64
	if _, err := tx.Exec(ctx, `UPDATE jobs SET status='running', updated_at=now() WHERE id=$1`, row.ID); err != nil {
		return fmt.Errorf("update running: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO job_attempts(job_id, attempt_no, worker_id, status)
		VALUES ($1,$2,$3,'running')
		RETURNING id
	`, row.ID, attemptNo, workerID).Scan(&attemptID); err != nil {
		return fmt.Errorf("insert attempt: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Perform the callback outside of the transaction
	shouldRetry, permFail, errStr := doCallback(client, row.ID, attemptNo, row.CallbackURL, row.Payload)
	// Open a new tx to update final state
	tx2, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx2.Rollback(ctx) }()

	switch {
	case !shouldRetry && !permFail:
		// Success
		if _, err := tx2.Exec(ctx, `
			UPDATE jobs SET status='succeeded', attempts=$2, updated_at=now(), next_run_at=NULL WHERE id=$1
		`, row.ID, attemptNo); err != nil {
			return fmt.Errorf("update success: %w", err)
		}
		if _, err := tx2.Exec(ctx, `UPDATE job_attempts SET status='succeeded', finished_at=now() WHERE id=$1`, attemptID); err != nil {
			return fmt.Errorf("attempt success: %w", err)
		}
	case permFail:
		// Permanent failure → dead
		if _, err := tx2.Exec(ctx, `
			UPDATE jobs SET status='dead', attempts=$2, updated_at=now(), next_run_at=NULL WHERE id=$1
		`, row.ID, attemptNo); err != nil {
			return fmt.Errorf("update dead: %w", err)
		}
		if _, err := tx2.Exec(ctx, `
			UPDATE job_attempts SET status='failed', error=$2, finished_at=now() WHERE id=$1
		`, attemptID, errStr); err != nil {
			return fmt.Errorf("attempt fail: %w", err)
		}
	default:
		// Transient failure → retry with backoff
		backoff := computeBackoff(attemptNo)
		next := time.Now().Add(backoff)
		if _, err := tx2.Exec(ctx, `
			UPDATE jobs SET status='failed', attempts=$2, updated_at=now(), next_run_at=$3 WHERE id=$1
		`, row.ID, attemptNo, next); err != nil {
			return fmt.Errorf("update retry: %w", err)
		}
		if _, err := tx2.Exec(ctx, `
			INSERT INTO schedules(job_id, due_at, released) VALUES ($1,$2,false)
		`, row.ID, next); err != nil {
			return fmt.Errorf("insert schedule: %w", err)
		}
		if _, err := tx2.Exec(ctx, `
			UPDATE job_attempts SET status='failed', error=$2, finished_at=now() WHERE id=$1
		`, attemptID, errStr); err != nil {
			return fmt.Errorf("attempt fail: %w", err)
		}
		// If attempts exceeded, mark dead
		if attemptNo >= row.MaxAttempts {
			if _, err := tx2.Exec(ctx, `UPDATE jobs SET status='dead', updated_at=now(), next_run_at=NULL WHERE id=$1`, row.ID); err != nil {
				return fmt.Errorf("mark dead: %w", err)
			}
		}
	}

	return tx2.Commit(ctx)
}

func doCallback(client *http.Client, jobID string, attempt int, url string, payload []byte) (shouldRetry bool, permFail bool, errStr string) {
	body := map[string]any{
		"job_id":  jobID,
		"attempt": attempt,
	}
	// merge payload if provided
	if len(payload) > 0 {
		var pl map[string]any
		if json.Unmarshal(payload, &pl) == nil {
			body["payload"] = pl
		}
	}

	bs, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bs))
	if err != nil {
		return true, false, fmt.Sprintf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("StratoQ-Job-ID", jobID)
	req.Header.Set("StratoQ-Attempt", fmt.Sprintf("%d", attempt))

	resp, err := client.Do(req)
	if err != nil {
		return true, false, fmt.Sprintf("http error: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	// 2xx = success
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return false, false, ""
	}
	// 408/429/5xx → transient
	if resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return true, false, fmt.Sprintf("transient status %d", resp.StatusCode)
	}
	// other 4xx → permanent
	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return false, true, fmt.Sprintf("permanent status %d", resp.StatusCode)
	}

	// default retry
	return true, false, fmt.Sprintf("unexpected status %d", resp.StatusCode)
}

func computeBackoff(attempt int) time.Duration {
	base := time.Duration(2<<uint(attempt-1)) * time.Second // 2s * 2^(attempt-1)
	if base > 60*time.Second {
		base = 60 * time.Second
	}
	jitter := time.Duration(rand.Intn(500)) * time.Millisecond
	return base + jitter
}

func env(k, def string) string {
	if v := os.Getenv(k); strings.TrimSpace(v) != "" {
		return v
	}
	return def
}
