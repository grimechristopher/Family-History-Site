#!/usr/bin/env bash
# Local Postgres for development and for tests, in one container but two
# databases.
#
# They must be separate: the test suites drop and recreate the `family` schema
# between cases, so sharing one database silently wipes whatever was imported
# for hand-testing. That happened repeatedly before this split.
#
#   eval "$(scripts/testdb.sh start)"   # exports DEV_DATABASE_URL and TEST_DATABASE_URL
#   scripts/testdb.sh stop
set -euo pipefail

NAME=fhs-testdb
PORT=55432
HOST="127.0.0.1:${PORT}"
DEV_DB=postgres      # what the server and the importer use
TEST_DB=fhs_test     # what `go test` uses, and is free to destroy

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
        # Idempotent: CREATE DATABASE has no IF NOT EXISTS, so check first.
        if ! docker exec "$NAME" psql -U postgres -tAc \
             "SELECT 1 FROM pg_database WHERE datname='${TEST_DB}'" | grep -q 1; then
          docker exec "$NAME" createdb -U postgres "${TEST_DB}"
        fi
        echo "export DEV_DATABASE_URL='postgres://postgres:testpw@${HOST}/${DEV_DB}?sslmode=disable'"
        echo "export TEST_DATABASE_URL='postgres://postgres:testpw@${HOST}/${TEST_DB}?sslmode=disable'"
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
