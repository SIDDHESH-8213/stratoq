package kafka

import (
	"context"
	"os"
	"strings"
	"time"

	kf "github.com/segmentio/kafka-go"
)

func brokers() []string {
	val := os.Getenv("KAFKA_BROKERS")
	if val == "" {
		val = "127.0.0.1:9092"
	}
	parts := strings.Split(val, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func NewWriter(topic string) *kf.Writer {
	return &kf.Writer{
		Addr:                   kf.TCP(brokers()...),
		Topic:                  topic,
		Balancer:               &kf.LeastBytes{},
		AllowAutoTopicCreation: true,
		BatchTimeout:           50 * time.Millisecond,
	}
}

func NewReader(topic, group string) *kf.Reader {
	return kf.NewReader(kf.ReaderConfig{
		Brokers:                brokers(),
		GroupID:                group,
		Topic:                  topic,
		MinBytes:               1,
		MaxBytes:               10e6,
		QueueCapacity:          1024,
		WatchPartitionChanges:  true,
		HeartbeatInterval:      3 * time.Second,
		SessionTimeout:         30 * time.Second,
		RebalanceTimeout:       30 * time.Second,
		JoinGroupBackoff:       5 * time.Second,
		ReadBackoffMin:         200 * time.Millisecond,
		ReadBackoffMax:         2 * time.Second,
		CommitInterval:         0, // synchronous commits
	})
}

func PublishJobID(ctx context.Context, topic, jobID string) error {
	w := NewWriter(topic)
	defer w.Close()
	msg := kf.Message{
		Key:   []byte(jobID),
		Value: []byte(jobID),
		Time:  time.Now(),
	}
	return w.WriteMessages(ctx, msg)
}
