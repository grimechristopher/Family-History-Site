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

// lockID is an arbitrary constant identifying this application's migration lock.
const lockID = 0x66_68_73_01 // "fhs" + 1

// Run applies every migration not already recorded. Safe to call on every boot,
// and safe when two instances boot simultaneously.
func Run(ctx context.Context, pool *pgxpool.Pool) error {
	// Serialise across processes. Without this, two instances starting together
	// race: CREATE SCHEMA IF NOT EXISTS is not atomic, and the loser gets a
	// unique-violation on pg_namespace rather than a quiet no-op. A compose
	// restart with more than one replica does exactly this.
	//
	// The lock is held on a dedicated connection for the duration, and advisory
	// locks need no schema to exist first.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		return fmt.Errorf("take migration lock: %w", err)
	}
	defer func() {
		// Best effort: releasing the connection drops the lock regardless.
		_, _ = conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", lockID)
	}()

	// The bookkeeping table lives in the same schema, so it has to exist before
	// anything can be recorded against it.
	_, err = conn.Exec(ctx, `
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
		err := conn.QueryRow(ctx,
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
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
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
