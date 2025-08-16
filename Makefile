SHELL := /bin/bash

.PHONY: up down migrate api worker scheduler topics fmt

up:
	docker compose -f deploy/docker-compose.yaml up -d

down:
	docker compose -f deploy/docker-compose.yaml down -v

migrate:
	psql $$DATABASE_URL -f migrations/001_init.sql
	psql $$DATABASE_URL -f migrations/002_add_callback_url.sql

api:
	go run ./cmd/api

worker:
	go run ./cmd/worker

scheduler:
	go run ./cmd/scheduler

topics:
	docker exec -it stratoq-kafka bash -lc "scripts/create-topics.sh || bash -lc 'cat /scripts/create-topics.sh not found'" || true
	@echo "If the above failed, run: docker exec -it stratoq-kafka bash -lc '/opt/bitnami/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic jobs_high --partitions 6 --replication-factor 1'; \
	/opt/bitnami/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic jobs_medium --partitions 6 --replication-factor 1; \
	/opt/bitnami/kafka/bin/kafka-topics.sh --bootstrap-server localhost:9092 --create --if-not-exists --topic jobs_low --partitions 6 --replication-factor 1"

fmt:
	go fmt ./...
