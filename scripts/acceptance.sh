#!/bin/sh
set -eu
docker compose run --rm acceptance --phase before
docker compose restart matchcore
attempt=0
until docker compose exec -T gateway /app/matchcore health http://localhost:8080/readyz >/dev/null 2>&1; do
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || exit 1
  sleep 1
done
docker compose run --rm acceptance --phase after
