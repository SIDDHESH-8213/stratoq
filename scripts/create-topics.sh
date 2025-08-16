#!/usr/bin/env bash
set -euo pipefail

BROKER="${1:-localhost:9092}"
PARTITIONS=6
REPLICATION=1

create() {
  local topic="$1"
  kafka-topics.sh --bootstrap-server "$BROKER" --create --if-not-exists --topic "$topic" --partitions "$PARTITIONS" --replication-factor "$REPLICATION" || true
}

create jobs_high
create jobs_medium
create jobs_low

echo "Topics ensured on $BROKER"
