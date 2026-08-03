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

// A hand-flipped askable flag is a deliberate choice, not a role default -- it
// must survive re-adding somebody or changing their role. askable is left out of
// AddMemberTx's ON CONFLICT ... SET on purpose; this pins that down so an
// innocent-looking cleanup of the SQL can't silently undo it.
func TestAddMemberTxPreservesAHandFlippedAskableFlag(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	famID, err := s.CreateFamily(ctx, "askable-conflict", "The Askable Conflict line")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	fctx := WithFamily(WithFamilies(ctx, []int64{famID}), famID)

	var userID int64
	err = s.InTx(fctx, func(db DBTX) error {
		var err error
		userID, err = UpsertUser(fctx, db, "cousin@example.com", "Cousin")
		if err != nil {
			return err
		}
		return AddMemberTx(fctx, db, famID, userID, RoleContributor)
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	before, err := s.Member(fctx, famID, userID)
	if err != nil {
		t.Fatalf("Member before flip: %v", err)
	}
	if !before.Askable {
		t.Fatal("a fresh contributor should default to askable")
	}

	// Flip it by hand, as SetMemberAskable will do once it exists (Task 3).
	if _, err := s.q(fctx).Exec(fctx,
		`UPDATE core.family_members SET askable = false WHERE family_id = $1 AND user_id = $2`,
		famID, userID); err != nil {
		t.Fatalf("flip askable: %v", err)
	}

	// Re-add them under the same contributor role. Deliberately not a role change:
	// a contributor's role-based default is askable=true, which is the opposite of
	// the false we just set by hand -- so if AddMemberTx's ON CONFLICT ever starts
	// recomputing askable from role, this would flip back to true and the test
	// below would catch it. (Re-adding under RoleAdmin would not catch it: an
	// admin's role-based default is also false, so a regression there would
	// coincidentally produce the right-looking answer for the wrong reason.)
	err = s.InTx(fctx, func(db DBTX) error {
		return AddMemberTx(fctx, db, famID, userID, RoleContributor)
	})
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}

	after, err := s.Member(fctx, famID, userID)
	if err != nil {
		t.Fatalf("Member after re-add: %v", err)
	}
	if after.Askable {
		t.Error("re-adding a member should not reset a hand-flipped askable flag")
	}
}

// Flipping the flag is how an admin who also has memories worth asking for gets
// offered, without giving up running the line.
func TestSetMemberAskableFlipsEitherWay(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()

	famID, err := s.CreateFamily(ctx, "flip", "The Flip line")
	if err != nil {
		t.Fatalf("CreateFamily: %v", err)
	}
	fctx := WithFamily(WithFamilies(ctx, []int64{famID}), famID)

	var adminID int64
	err = s.InTx(fctx, func(db DBTX) error {
		var err error
		adminID, err = UpsertUser(fctx, db, "aunt@example.com", "Aunt")
		if err != nil {
			return err
		}
		return AddMemberTx(fctx, db, famID, adminID, RoleAdmin)
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := s.SetMemberAskable(fctx, famID, adminID, true); err != nil {
		t.Fatalf("SetMemberAskable(true): %v", err)
	}
	m, err := s.Member(fctx, famID, adminID)
	if err != nil {
		t.Fatalf("Member: %v", err)
	}
	if !m.Askable {
		t.Error("the admin should now be askable")
	}

	if err := s.SetMemberAskable(fctx, famID, adminID, false); err != nil {
		t.Fatalf("SetMemberAskable(false): %v", err)
	}
	m, err = s.Member(fctx, famID, adminID)
	if err != nil {
		t.Fatalf("Member: %v", err)
	}
	if m.Askable {
		t.Error("the admin should be not-askable again")
	}

	if err := s.SetMemberAskable(fctx, famID, 999999, true); err == nil {
		t.Error("expected an error setting askable for somebody not in the family")
	}
}
