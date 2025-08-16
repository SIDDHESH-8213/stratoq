#!/usr/bin/env bash
set -euo pipefail
export DATABASE_URL=${DATABASE_URL:-postgres://dev:dev@localhost:5432/stratoq?sslmode=disable}
export KAFKA_BROKERS=${KAFKA_BROKERS:-127.0.0.1:9092}
go run ./cmd/scheduler
