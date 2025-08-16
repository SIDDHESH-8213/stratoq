package domain

import "time"

type EnqueueRequest struct {
	Queue        string                 `json:"queue"`
	Priority     int16                  `json:"priority"`
	MaxAttempts  int                    `json:"max_attempts"`
	DelaySeconds int                    `json:"delay_seconds"`
	CallbackURL  string                 `json:"callback_url"`
	Payload      map[string]any         `json:"payload"`
}

type Job struct {
	ID            string                 `json:"id"`
	Queue         string                 `json:"queue"`
	Priority      int16                  `json:"priority"`
	Status        string                 `json:"status"`
	Attempts      int                    `json:"attempts"`
	MaxAttempts   int                    `json:"max_attempts"`
	NextRunAt     *time.Time             `json:"next_run_at,omitempty"`
	CallbackURL   *string                `json:"callback_url,omitempty"`
	Payload       map[string]any         `json:"payload"`
	CreatedAt     time.Time              `json:"created_at"`
	UpdatedAt     time.Time              `json:"updated_at"`
	IdempotentHit bool                   `json:"idempotent,omitempty"`
}

func TopicForPriority(p int16) string {
	if p >= 9 {
		return "jobs_high"
	}
	if p >= 5 {
		return "jobs_medium"
	}
	return "jobs_low"
}
