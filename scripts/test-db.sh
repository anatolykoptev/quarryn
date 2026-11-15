#!/usr/bin/env bash
# Run the live-Postgres tests (PG_LIVE_DSN) against a THROWAWAY pgvector
# Postgres, never the live cluster: `docker run --rm` on a random loopback
# port, a single quarryn_test database, torn down on exit.
#
# Usage: make test-db                         # go test ./internal/...
#        bash scripts/test-db.sh ./internal/group -run TestLivePGGroupStore
set -euo pipefail

cd "$(dirname "$0")/.."
NAME="quarryn-test-db-$$"
IMAGE="pgvector/pgvector:0.8.7-pg18-trixie"

cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT INT TERM

echo ">> starting throwaway $IMAGE as $NAME (random loopback port)"
docker run -d --rm --name "$NAME" \
  -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD=postgres -e POSTGRES_DB=quarryn_test \
  -p 127.0.0.1::5432 "$IMAGE" >/dev/null
PORT=$(docker inspect -f '{{ (index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort }}' "$NAME")

ready=0
for _ in $(seq 1 30); do
  if docker exec "$NAME" pg_isready -h 127.0.0.1 -U postgres -d quarryn_test >/dev/null 2>&1; then ready=1; break; fi
  sleep 2
done
[ "$ready" = "1" ] || { echo "postgres never became ready" >&2; docker logs "$NAME" | tail -20 >&2; exit 1; }

export PG_LIVE_DSN="postgres://postgres:postgres@127.0.0.1:${PORT}/quarryn_test?sslmode=disable"
echo ">> PG_LIVE_DSN points at the throwaway container on 127.0.0.1:${PORT}"
if [ "$#" -eq 0 ]; then set -- ./internal/...; fi
GOWORK=off go test -count=1 -p 1 "$@"
