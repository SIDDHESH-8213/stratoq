-- 001_init.sql
CREATE TYPE job_status AS ENUM ('pending','running','succeeded','failed','dead');

CREATE TABLE jobs (
  id UUID PRIMARY KEY,
  idempotency_key TEXT UNIQUE,
  queue TEXT NOT NULL,
  priority SMALLINT NOT NULL,
  payload JSONB NOT NULL,
  callback_url TEXT,
  status job_status NOT NULL DEFAULT 'pending',
  attempts INT NOT NULL DEFAULT 0,
  max_attempts INT NOT NULL DEFAULT 5,
  next_run_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE job_attempts (
  id BIGSERIAL PRIMARY KEY,
  job_id UUID REFERENCES jobs(id) ON DELETE CASCADE,
  attempt_no INT NOT NULL,
  worker_id TEXT NOT NULL,
  status job_status NOT NULL,
  error TEXT,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  finished_at TIMESTAMPTZ
);

CREATE TABLE schedules (
  id BIGSERIAL PRIMARY KEY,
  job_id UUID REFERENCES jobs(id) ON DELETE CASCADE,
  due_at TIMESTAMPTZ NOT NULL,
  released BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE idempotency_keys (
  key TEXT PRIMARY KEY,
  job_id UUID UNIQUE REFERENCES jobs(id),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX jobs_status_idx ON jobs(status);
CREATE INDEX jobs_next_idx ON jobs(next_run_at) WHERE next_run_at IS NOT NULL;
CREATE INDEX ja_job_idx ON job_attempts(job_id);
CREATE INDEX sched_due_idx ON schedules(due_at) WHERE released = false;
