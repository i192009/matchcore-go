#!/bin/sh
set -eu
: "${MATCHCORE_TEST_DATABASE_URL:?Set MATCHCORE_TEST_DATABASE_URL to an isolated PostgreSQL database}"
: "${MATCHCORE_TEST_NATS_URL:?Set MATCHCORE_TEST_NATS_URL}"
: "${MATCHCORE_TEST_REDIS_ADDR:?Set MATCHCORE_TEST_REDIS_ADDR}"
go test -count=1 -race ./tests/integration ./internal/transport
