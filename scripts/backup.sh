#!/usr/bin/env bash
#
# Nightly backup of the family schema.
#
# This is not routine infrastructure hygiene. The whole point of this site is to
# hold things that cannot be recovered from anywhere else: if the disk dies and
# there is no copy, Dad's stories about his father are simply gone, and no amount
# of code brings them back.
#
# Install as a cron entry, e.g.:
#   15 3 * * *  /path/to/scripts/backup.sh >> /var/log/family-backup.log 2>&1
#
# Required:
#   DATABASE_URL     Postgres connection string
#   BACKUP_DIR       where dumps are written -- MUST be on different hardware
#                    from the database (a NAS mount, an external disk, a bucket)
# Optional:
#   KEEP_DAYS        how many days of dumps to retain (default 30)
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL must be set}"
: "${BACKUP_DIR:?BACKUP_DIR must be set (and should not be on the same disk as the database)}"
KEEP_DAYS="${KEEP_DAYS:-30}"

stamp="$(date +%Y%m%d-%H%M%S)"
mkdir -p "$BACKUP_DIR"
target="${BACKUP_DIR}/family-${stamp}.sql.gz"
partial="${target}.partial"

echo "$(date -Is) backing up family schema to ${target}"

# Write to a .partial name and rename only on success, so an interrupted run
# never leaves a truncated file that looks like a good backup.
pg_dump "$DATABASE_URL" \
    --schema=family \
    --no-owner \
    --no-privileges \
    | gzip -9 > "$partial"

mv "$partial" "$target"

size="$(du -h "$target" | cut -f1)"
echo "$(date -Is) wrote ${target} (${size})"

# A dump that cannot be read is not a backup. Verify before trusting it.
if ! gzip -t "$target"; then
    echo "$(date -Is) FAILED: ${target} is not a readable gzip file" >&2
    exit 1
fi

# Guard against the silent failure mode where pg_dump succeeds but emits nothing
# useful, so the retention sweep then deletes the last good copy.
if [ "$(gzip -dc "$target" | grep -c 'CREATE TABLE' || true)" -lt 5 ]; then
    echo "$(date -Is) FAILED: ${target} contains almost no tables; refusing to rotate" >&2
    exit 1
fi

echo "$(date -Is) verified ${target}"

deleted="$(find "$BACKUP_DIR" -name 'family-*.sql.gz' -type f -mtime "+${KEEP_DAYS}" -print -delete | wc -l)"
if [ "$deleted" -gt 0 ]; then
    echo "$(date -Is) removed ${deleted} dump(s) older than ${KEEP_DAYS} days"
fi

echo "$(date -Is) done. $(find "$BACKUP_DIR" -name 'family-*.sql.gz' -type f | wc -l) dump(s) retained"
