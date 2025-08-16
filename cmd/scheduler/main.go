package main

import (
	"context"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/SIDDHESH-8213/stratoq/internal/db"
	"github.com/SIDDHESH-8213/stratoq/internal/domain"
	kaf "github.com/SIDDHESH-8213/stratoq/internal/kafka"
)

type schedItem struct {
	ID       int64
	JobID    string
	Priority int16
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.NewPool(ctx)
	if err != nil {
		log.Fatalf("db pool: %v", err)
	}
	defer pool.Close()

	log.Printf("scheduler started")

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("scheduler shutting down...")
			return
		case <-ticker.C:
			if err := releaseBatch(ctx, pool); err != nil {
				log.Printf("release error: %v", err)
				time.Sleep(time.Second)
			}
		}
	}
}

func releaseBatch(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT s.id, s.job_id, j.priority
		FROM schedules s
		JOIN jobs j ON j.id = s.job_id
		WHERE s.released=false AND s.due_at <= now()
		ORDER BY s.id
		FOR UPDATE SKIP LOCKED
		LIMIT 100
	`)
	if err != nil {
		return fmt.Errorf("query schedules: %w", err)
	}
	defer rows.Close()

	var items []schedItem
	for rows.Next() {
		var it schedItem
		if err := rows.Scan(&it.ID, &it.JobID, &it.Priority); err != nil {
			return err
		}
		items = append(items, it)
	}
	if rows.Err() != nil {
		return rows.Err()
	}
	if len(items) == 0 {
		return tx.Commit(ctx)
	}

	for _, it := range items {
		// Mark job pending and schedule released
		if _, err := tx.Exec(ctx, `UPDATE jobs SET status='pending', next_run_at=NULL, updated_at=now() WHERE id=$1`, it.JobID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE schedules SET released=true WHERE id=$1`, it.ID); err != nil {
			return err
		}
		// Publish to Kafka
		topic := domain.TopicForPriority(it.Priority)
		if err := kaf.PublishJobID(ctx, topic, it.JobID); err != nil {
			log.Printf("kafka publish (scheduler) failed for job %s: %v", it.JobID, err)
		}
	}

	return tx.Commit(ctx)
}
