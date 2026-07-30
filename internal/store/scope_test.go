package store

import (
	"context"
	"testing"
)

// Without a transaction in the context a store runs against the pool, so the
// importer and the command-line tools are unaffected by any of this.
func TestQueryerFallsBackToThePool(t *testing.T) {
	s := &Store{}
	if got := s.q(context.Background()); got != DBTX(s.Pool) {
		t.Fatalf("expected the pool, got %T", got)
	}
}

// With one, every query in that request runs inside it. That is what makes
// SET LOCAL app.family_id apply to the queries and not just to the setting.
func TestQueryerPrefersTheRequestTransaction(t *testing.T) {
	tx := &fakeDBTX{}
	ctx := WithTx(context.Background(), tx)
	s := &Store{}
	if got := s.q(ctx); got != DBTX(tx) {
		t.Fatalf("expected the request transaction, got %T", got)
	}
}

func TestFamilyTravelsInTheContext(t *testing.T) {
	if got := FamilyFrom(context.Background()); got != 0 {
		t.Errorf("outside a family-scoped request the family should be 0, got %d", got)
	}
	if got := FamilyFrom(WithFamily(context.Background(), 7)); got != 7 {
		t.Errorf("family = %d, want 7", got)
	}
}

type fakeDBTX struct{ DBTX }
