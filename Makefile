GEDCOM ?= $(HOME)/Documents/Github/genealogy-export/Brennan-Hale Tree.ged
PROMPTS ?= $(HOME)/Documents/Obsidian/General Notebook/General Notebook/Areas/Ancestry Book/Prompts 3.md

.PHONY: help build test test-unit test-db test-real fmt vet check testdb-start testdb-stop import import-dry run

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

import-dry: ## Parse and match without writing anything
	go run ./cmd/import -dry-run \
	  -gedcom "$(GEDCOM)" -prompts "$(PROMPTS)" \
	  -dad-email "$(DAD_EMAIL)" -mom-email "$(MOM_EMAIL)" -admin-email "$(ADMIN_EMAIL)"

import: ## Seed the development database
	eval "$$(./scripts/testdb.sh start)" && DATABASE_URL="$$DEV_DATABASE_URL" go run ./cmd/import \
	  -gedcom "$(GEDCOM)" -prompts "$(PROMPTS)" \
	  -dad-email "$(DAD_EMAIL)" -mom-email "$(MOM_EMAIL)" -admin-email "$(ADMIN_EMAIL)"

run: ## Run the server against the development database
	eval "$$(./scripts/testdb.sh start)" && DATABASE_URL="$$DEV_DATABASE_URL" go run ./cmd/server
