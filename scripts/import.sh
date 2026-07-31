#!/usr/bin/env bash
# Import one line, or every line listed in a config file.
#
# The four commands that build this site used to live only in a shell history.
# Rebuilding from a clone meant reconstructing them by hand, and each one carries
# details -- real names, real addresses, which GEDCOM name is whose -- that do not
# belong in a public repository. So the arguments live in a file you keep, and
# this script reads it.
#
#   cp lines.example.conf lines.conf   # then edit; lines.conf is gitignored
#   scripts/import.sh                  # every line in the file
#   scripts/import.sh grime            # just one
#
# Each line of the config is:
#   slug | tree file | prompts file | -person argument | extra names
# and a line may repeat the -person field, separated by ';;', for a family where
# several people are asked the same set of questions.
#
# The tree file is per line and not global. Two families here come from one export
# and two from another, and running them all against one file silently imported
# fifteen fewer questions than it should have -- the names matched, so nothing
# failed, there was simply less of the family in the file.
#
# JSON, produced once from a GEDCOM by cmd/treejson. A .ged path still works.
set -euo pipefail

cd "$(dirname "$0")/.."
CONF="${LINES_CONF:-lines.conf}"
WANT="${1:-}"

if [ ! -f "$CONF" ]; then
  echo "No $CONF. Copy lines.example.conf to $CONF and fill it in." >&2
  exit 1
fi
: "${ADMIN_EMAIL:?set ADMIN_EMAIL}"
: "${DATABASE_URL:?set DATABASE_URL -- the owner role, not fhs_app: the importer creates and deletes rows across the schema}"

while IFS='|' read -r slug ged prompts people extra; do
  slug="$(echo "$slug" | xargs)"
  [ -z "$slug" ] && continue
  case "$slug" in \#*) continue ;; esac
  [ -n "$WANT" ] && [ "$WANT" != "$slug" ] && continue

  ged="$(echo "$ged" | xargs)"
  [ -z "$ged" ] && ged="${GEDCOM_PATH:-}"
  [ -f "$ged" ] || { echo "$slug: no tree file at '$ged'" >&2; exit 1; }
  prompts="$(echo "$prompts" | xargs)"
  [ -f "$prompts" ] || { echo "$slug: no prompts at '$prompts'" >&2; exit 1; }
  extra="$(echo "${extra:-}" | xargs)"

  args=()
  IFS=';;' read -ra parts <<< "$people"
  for p in "${parts[@]}"; do
    p="$(echo "$p" | xargs)"
    [ -n "$p" ] && args+=(-person "$p")
  done

  echo "== $slug"
  go run ./cmd/import -family "$slug" \
    -gedcom "$ged" -prompts "$prompts" \
    -database-url "$DATABASE_URL" -extra-names "$extra" \
    "${args[@]}" \
    -admin-email "$ADMIN_EMAIL" -admin-label "${ADMIN_LABEL:-Admin}" \
    | sed 's/^/   /'
done < "$CONF"
