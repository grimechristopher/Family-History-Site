#!/usr/bin/env bash
# Local Postgres for development and for tests, in one container but two
# databases.
#
# They must be separate: the test suites drop and recreate the `family` schema
# between cases, so sharing one database silently wipes whatever was imported
# for hand-testing. That happened repeatedly before this split.
#
# Three URLs come out of this, and the difference matters:
#
#   DEV_DATABASE_URL       connects as fhs_app, the unprivileged role the
#                          deployment uses. Row-level security applies, so
#                          development behaves like production.
#   DEV_ADMIN_DATABASE_URL connects as postgres. Migrations and the importer use
#                          it, because creating schemas needs the privilege.
#   TEST_DATABASE_URL      connects as postgres, for the suites that seed and
#                          inspect across families on purpose.
#
# Running the server as postgres is what let one family's tree render inside
# another's: superusers are exempt from every policy, so nothing was scoped.
#
#   eval "$(scripts/testdb.sh start)"
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
        # The role the server connects as. Created here rather than waiting for
        # migration 0005, so it exists before anything wants to use it.
        docker exec "$NAME" psql -U postgres -d "${DEV_DB}" -q -c "DO \$\$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='fhs_app') THEN CREATE ROLE fhs_app LOGIN PASSWORD 'testpw'; END IF; END \$\$;" >/dev/null 2>&1 || true
        docker exec "$NAME" psql -U postgres -d "${DEV_DB}" -q -c "GRANT CREATE ON DATABASE ${DEV_DB} TO fhs_app;" >/dev/null 2>&1 || true
        # Hand the schemas to fhs_app once they exist, which is what production does
        # (REASSIGN OWNED BY postgres TO fhs_app). The app has to own them to run its
        # own migrations, and FORCE ROW LEVEL SECURITY is what keeps the owner subject
        # to the policies anyway. Idempotent, so this converges on every start.
        docker exec -i "$NAME" psql -U postgres -d "${DEV_DB}" -q >/dev/null 2>&1 <<'OWN' || true
DO $$
DECLARE r record;
BEGIN
    -- Targeted rather than REASSIGN OWNED BY postgres, which refuses because that
    -- role also owns objects the database system requires.
    FOR r IN SELECT nspname FROM pg_namespace WHERE nspname IN ('family','core') LOOP
        EXECUTE format('ALTER SCHEMA %I OWNER TO fhs_app', r.nspname);
    END LOOP;
    FOR r IN SELECT schemaname, tablename FROM pg_tables
              WHERE schemaname IN ('family','core') AND tableowner <> 'fhs_app' LOOP
        EXECUTE format('ALTER TABLE %I.%I OWNER TO fhs_app', r.schemaname, r.tablename);
    END LOOP;
END $$;
OWN' >/dev/null 2>&1 || true
DO $$
DECLARE r record;
BEGIN
    -- Targeted rather than REASSIGN OWNED BY postgres, which refuses because that
    -- role also owns objects the database system requires.
    FOREACH r IN ARRAY ARRAY[]::record[] LOOP END LOOP;
    FOR r IN SELECT nspname FROM pg_namespace WHERE nspname IN ('family','core') LOOP
        EXECUTE format('ALTER SCHEMA %I OWNER TO fhs_app', r.nspname);
    END LOOP;
    FOR r IN SELECT schemaname, tablename FROM pg_tables
              WHERE schemaname IN ('family','core') AND tableowner <> 'fhs_app' LOOP
        EXECUTE format('ALTER TABLE %I.%I OWNER TO fhs_app', r.schemaname, r.tablename);
    END LOOP;
    FOR r IN SELECT sequence_schema, sequence_name FROM information_schema.sequences
              WHERE sequence_schema IN ('family','core') LOOP
        EXECUTE format('ALTER SEQUENCE %I.%I OWNER TO fhs_app', r.sequence_schema, r.sequence_name);
    END LOOP;
END $$;
OWN
        echo "export DEV_DATABASE_URL='postgres://fhs_app:testpw@${HOST}/${DEV_DB}?sslmode=disable'"
        echo "export DEV_ADMIN_DATABASE_URL='postgres://postgres:testpw@${HOST}/${DEV_DB}?sslmode=disable'"
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
