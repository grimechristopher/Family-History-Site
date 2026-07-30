# Paths to your own data. Override on the command line or in the environment.
GEDCOM ?= $(GEDCOM_PATH)
PROMPTS ?= $(PROMPTS_PATH)

.PHONY: help build test test-unit test-db test-real fmt vet check testdb-start testdb-stop import import-dry run set-email family

help:
	@grep -hE '^[a-z-]+:.*?##' $(MAKEFILE_LIST) | sed 's/:.*##/\t/' | expand -t22

build: ## Compile everything
	go build ./...

fmt: ## Format
	gofmt -w ./cmd ./internal ./assets

vet: ## Static checks
	go vet ./...

test-unit: ## Tests needing no database
	go test ./internal/config/ ./internal/gedcom/ ./internal/prompts/ ./internal/subjects/ ./internal/auth/

# -p 1 is required: these packages share one schema and drop it between tests,
# so running the packages concurrently makes them fight over it. They run against
# a separate database from the dev data, which they would otherwise wipe.
test-db: testdb-start ## Tests needing Postgres
	eval "$$(./scripts/testdb.sh start)" && go test -p 1 ./internal/migrate/ ./internal/store/ ./internal/web/

test: test-unit test-db ## Everything

test-real: ## Verify parsers against the actual GEDCOM and prompts file
	REAL_GEDCOM="$(GEDCOM)" REAL_PROMPTS="$(PROMPTS)" \
	  go test ./internal/gedcom/ ./internal/subjects/ -run Real -v

check: fmt vet test ## Format, vet, and test

testdb-start: ## Start the throwaway Postgres
	@./scripts/testdb.sh start >/dev/null

testdb-stop: ## Remove the throwaway Postgres
	@./scripts/testdb.sh stop

import-dry: ## Parse and match one line without writing anything
	eval "$$(./scripts/testdb.sh start)" && set -a && . ./.env && set +a && \
	  DATABASE_URL="$$DEV_ADMIN_DATABASE_URL" DRY_RUN=1 ./scripts/import.sh $(FAMILY)

# The arguments live in lines.conf, not here: they are real names and addresses,
# and this file is public. See lines.example.conf.
# make import          -- every line
# make import FAMILY=grime
import: ## Seed the development database from lines.conf (FAMILY=slug for one)
	eval "$$(./scripts/testdb.sh start)" && set -a && . ./.env && set +a && \
	  DATABASE_URL="$$DEV_ADMIN_DATABASE_URL" ./scripts/import.sh $(FAMILY)

# Both places have to agree: this site's allowlist and Supabase's own auth.users.
# make set-email NAME=Dad EMAIL=dad@theirdomain.com
set-email: ## Set the address a person signs in with, and create their Supabase account
	eval "$$(./scripts/testdb.sh start)" && DATABASE_URL="$${DATABASE_URL:-$$DEV_ADMIN_DATABASE_URL}" \
	  go run ./cmd/user -name "$(NAME)" -email "$(EMAIL)" -create-supabase

# The server connects as fhs_app, not postgres. A superuser is exempt from every
# row-level security policy, so running it as postgres silently disables family
# isolation -- which is how one family's tree came to render inside another's.
family: ## Create a family and its first admin (SLUG=, NAME=, ADMIN=)
	eval "$$(./scripts/testdb.sh start)" && DATABASE_URL="$$DEV_ADMIN_DATABASE_URL" \
	  go run ./cmd/family -slug "$(SLUG)" -name "$(NAME)" -admin "$(ADMIN)"

run: ## Run the server against the development database
	eval "$$(./scripts/testdb.sh start)" && DATABASE_URL="$$DEV_DATABASE_URL" go run ./cmd/server
