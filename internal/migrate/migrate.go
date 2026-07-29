// Package migrate applies embedded SQL migrations at server startup.
//
// A migration tool is deliberately avoided: embedding the files means there is
// no binary to install and no migration step in the deployment, and the whole
// mechanism is short enough to read in one sitting.
package migrate

import (
	"context"
	"embed"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed sql/*.sql
var files embed.FS

// Run applies every migration not already recorded. Safe to call on every boot.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	// The bookkeeping table lives in the same schema, so it has to exist before
	// anything can be recorded against it.
	_, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS family;
		CREATE TABLE IF NOT EXISTS family.schema_migrations (
			name       text PRIMARY KEY,
			applied_at timestamptz NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	entries, err := files.ReadDir("sql")
	if err != nil {
		return fmt.Errorf("read embedded migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		var applied bool
		err := pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM family.schema_migrations WHERE name = $1)",
			name).Scan(&applied)
		if err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
		if applied {
			continue
		}

		body, err := files.ReadFile("sql/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		// Each migration runs in its own transaction, so a failure leaves no
		// partial schema behind.
		err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, string(body)); err != nil {
				return fmt.Errorf("exec %s: %w", name, err)
			}
			_, err := tx.Exec(ctx,
				"INSERT INTO family.schema_migrations (name) VALUES ($1)", name)
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}
