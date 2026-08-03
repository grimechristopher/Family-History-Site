package store

import (
	"context"
	"testing"
)

// A contributor is askable the moment they're added, whether or not anything has
// ever been asked of them -- the checkbox that offers who a new question can go
// to must not require a first question to already exist before offering them at
// all. An admin is the opposite by default: not askable, until somebody flips it.
func TestNewMembersDefaultAskableByRole(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	famID, err := s.CreateFamily(ctx, "askable", "The Askable line")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	fctx := WithFamily(WithFamilies(ctx, []int64{famID}), famID)

	var contributorID, adminID int64
	err = s.InTx(fctx, func(db DBTX) error {
		var err error
		contributorID, err = UpsertUser(fctx, db, "niece@example.com", "Niece")
		if err != nil {
			return err
		}
		if err := AddMemberTx(fctx, db, famID, contributorID, RoleContributor); err != nil {
			return err
		}
		adminID, err = UpsertUser(fctx, db, "uncle@example.com", "Uncle")
		if err != nil {
			return err
		}
		return AddMemberTx(fctx, db, famID, adminID, RoleAdmin)
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	contributor, err := s.Member(fctx, famID, contributorID)
	if err != nil {
		t.Fatalf("Member(contributor): %v", err)
	}
	if !contributor.Askable {
		t.Error("a fresh contributor should default to askable")
	}

	admin, err := s.Member(fctx, famID, adminID)
	if err != nil {
		t.Fatalf("Member(admin): %v", err)
	}
	if admin.Askable {
		t.Error("a fresh admin should default to not askable")
	}
}
