#!/usr/bin/env bash
# Starts a throwaway Postgres for integration tests and prints the URL to use.
#
#   eval "$(scripts/testdb.sh start)"   # exports TEST_DATABASE_URL
#   scripts/testdb.sh stop
set -euo pipefail

NAME=fhs-testdb
PORT=55432
URL="postgres://postgres:testpw@127.0.0.1:${PORT}/postgres?sslmode=disable"

case "${1:-start}" in
  start)
    if ! docker inspect "$NAME" >/dev/null 2>&1; then
      docker run -d --name "$NAME" \
        -e POSTGRES_PASSWORD=testpw \
        -p "${PORT}:5432" \
        postgres:16-alpine >/dev/null
    fi
    docker start "$NAME" >/dev/null 2>&1 || true
    for _ in $(seq 1 30); do
      if docker exec "$NAME" pg_isready -U postgres >/dev/null 2>&1; then
        echo "export TEST_DATABASE_URL='${URL}'"
        exit 0
      fi
      sleep 1
    done
    echo "postgres did not become ready" >&2
    exit 1
    ;;
  stop)
    docker rm -f "$NAME" >/dev/null 2>&1 || true
    ;;
  *)
    echo "usage: $0 [start|stop]" >&2
    exit 2
    ;;
esac
