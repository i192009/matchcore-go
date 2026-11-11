#!/bin/sh
set -eu
acceptance_user="$(id -u):$(id -g)"
docker compose run --rm --user "$acceptance_user" acceptance --phase before
docker compose restart matchcore
attempt=0
until docker compose exec -T gateway /app/matchcore health http://localhost:8080/readyz >/dev/null 2>&1; do
  attempt=$((attempt+1))
  [ "$attempt" -lt 60 ] || exit 1
  sleep 1
done
docker compose run --rm --user "$acceptance_user" acceptance --phase after
