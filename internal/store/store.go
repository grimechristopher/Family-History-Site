// Package store holds every database query, grouped by table.
//
// Queries are hand-written against pgx. There are few enough of them that a code
// generator would add a build step without removing meaningful work.
package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrNotFound = errors.New("not found")

// DBTX is satisfied by both *pgxpool.Pool and pgx.Tx, so every query can run
// either standalone or inside the import's transaction.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Store struct {
	Pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{Pool: pool}
}

type txKey struct{}
type familyKey struct{}

// WithTx puts the request's transaction in the context. Every store method called
// with that context runs inside it, which is what makes SET LOCAL app.family_id
// apply to the queries as well as to the setting -- SET LOCAL lasts only for the
// transaction, and a pooled connection is shared with everybody else.
//
// It travels in the context rather than in a parameter so that the sixty-odd call
// sites in the handlers keep their signatures: ctx is already the first argument
// of every store method.
func WithTx(ctx context.Context, db DBTX) context.Context {
	return context.WithValue(ctx, txKey{}, db)
}

// WithFamily records which family the request is for, so inserts can set
// family_id. Reads do not need it: row-level security filters them whether or not
// the query remembered to.
func WithFamily(ctx context.Context, familyID int64) context.Context {
	return context.WithValue(ctx, familyKey{}, familyID)
}

// FamilyFrom returns the request's family, or 0 outside a family-scoped request.
func FamilyFrom(ctx context.Context) int64 {
	id, _ := ctx.Value(familyKey{}).(int64)
	return id
}

// q is the handle every query runs against: the request's transaction when there
// is one, the pool otherwise.
func (s *Store) q(ctx context.Context) DBTX {
	if db, ok := ctx.Value(txKey{}).(DBTX); ok && db != nil {
		return db
	}
	return s.Pool
}

// InTx runs fn inside a transaction, rolling back on error.
func (s *Store) InTx(ctx context.Context, fn func(DBTX) error) error {
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		return fn(tx)
	})
}

// prefixed qualifies a comma-separated column list with a table alias, so the
// same list can be reused in joined queries.
func prefixed(columns, prefix string) string {
	parts := strings.Split(columns, ",")
	for i, p := range parts {
		parts[i] = prefix + strings.TrimSpace(p)
	}
	return strings.Join(parts, ", ")
}
