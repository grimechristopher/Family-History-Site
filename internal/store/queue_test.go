package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grimechristopher/family-history-site/internal/migrate"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip(`TEST_DATABASE_URL not set; run: eval "$(scripts/testdb.sh start)"`)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS family CASCADE"); err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := migrate.Run(ctx, pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(pool)
}

// seedQueue creates one contributor and n questions in markdown order.
func seedQueue(t *testing.T, s *Store, n int) (*User, []int64) {
	t.Helper()
	ctx := context.Background()

	var userID int64
	var subjectID int64
	err := s.InTx(ctx, func(db DBTX) error {
		var err error
		userID, err = UpsertUser(ctx, db, "dad@example.com", "Dad", RoleContributor)
		if err != nil {
			return err
		}
		subjectID, err = UpsertSubject(ctx, db, Subject{
			Slug: "dad", Kind: "individual", DisplayName: "Peter John Hale", SortOrder: 1,
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed user/subject: %v", err)
	}

	var ids []int64
	err = s.InTx(ctx, func(db DBTX) error {
		for i := 0; i < n; i++ {
			id, err := UpsertImportedQuestion(ctx, db, ImportedQuestion{
				SubjectID:     subjectID,
				AskedOfUserID: userID,
				Body:          "Question " + string(rune('A'+i)),
				SortOrder:     i,
				ImportKey:     "key-" + string(rune('A'+i)),
			})
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed questions: %v", err)
	}

	u, err := s.UserByID(ctx, userID)
	if err != nil {
		t.Fatalf("UserByID: %v", err)
	}
	return u, ids
}

func cardIDs(cards []Card) []int64 {
	out := make([]int64, len(cards))
	for i, c := range cards {
		out[i] = c.QuestionID
	}
	return out
}

func TestNextCardsFollowsMarkdownOrder(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, ids := seedQueue(t, s, 5)

	cards, err := s.NextCards(ctx, u, 10)
	if err != nil {
		t.Fatalf("NextCards: %v", err)
	}
	if len(cards) != 5 {
		t.Fatalf("got %d cards, want 5", len(cards))
	}
	for i, got := range cardIDs(cards) {
		if got != ids[i] {
			t.Errorf("card %d = %d, want %d (markdown order)", i, got, ids[i])
		}
	}
	if cards[0].SubjectName != "Peter John Hale" {
		t.Errorf("SubjectName = %q", cards[0].SubjectName)
	}
}

// Swiping a card away must send it behind everything, and swiping again must
// send it further back still.
func TestDeferSendsCardToTheBack(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, ids := seedQueue(t, s, 4)

	if err := s.DeferQuestion(ctx, ids[0], u.ID); err != nil {
		t.Fatalf("DeferQuestion: %v", err)
	}

	cards, err := s.NextCards(ctx, u, 10)
	if err != nil {
		t.Fatalf("NextCards: %v", err)
	}
	got := cardIDs(cards)
	if len(got) != 4 {
		t.Fatalf("deferring must not remove a card: got %d", len(got))
	}
	if got[len(got)-1] != ids[0] {
		t.Errorf("deferred card is at position %v, want last: %v", got, ids)
	}
	if got[0] != ids[1] {
		t.Errorf("first card = %d, want %d", got[0], ids[1])
	}

	// A second deferral of a different card must land behind the first, since
	// deferrals are ordered oldest-first.
	time.Sleep(5 * time.Millisecond)
	if err := s.DeferQuestion(ctx, ids[1], u.ID); err != nil {
		t.Fatalf("DeferQuestion: %v", err)
	}
	cards, _ = s.NextCards(ctx, u, 10)
	got = cardIDs(cards)
	if got[len(got)-1] != ids[1] || got[len(got)-2] != ids[0] {
		t.Errorf("order = %v, want ...%d,%d at the back", got, ids[0], ids[1])
	}
}

func TestDeferCountAccumulates(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, ids := seedQueue(t, s, 2)

	for i := 0; i < 3; i++ {
		if err := s.DeferQuestion(ctx, ids[0], u.ID); err != nil {
			t.Fatalf("DeferQuestion: %v", err)
		}
	}
	cards, _ := s.NextCards(ctx, u, 10)
	for _, c := range cards {
		if c.QuestionID == ids[0] && c.DeferCount != 3 {
			t.Errorf("DeferCount = %d, want 3", c.DeferCount)
		}
	}
}

// A published answer removes a card. A draft must not, or an unfinished thought
// would silently vanish from the stack.
func TestPublishedAnswerRemovesCardButDraftDoesNot(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, ids := seedQueue(t, s, 3)

	if _, err := s.SaveAnswer(ctx, ids[0], u.ID, "half a thought", true); err != nil {
		t.Fatalf("SaveAnswer draft: %v", err)
	}
	cards, _ := s.NextCards(ctx, u, 10)
	if len(cards) != 3 {
		t.Fatalf("a draft must not remove a card: got %d", len(cards))
	}
	if cards[0].QuestionID != ids[0] || cards[0].DraftBody != "half a thought" {
		t.Errorf("draft should ride along on the card: %+v", cards[0])
	}

	if _, err := s.SaveAnswer(ctx, ids[0], u.ID, "the whole story", false); err != nil {
		t.Fatalf("SaveAnswer published: %v", err)
	}
	cards, _ = s.NextCards(ctx, u, 10)
	if len(cards) != 2 {
		t.Fatalf("a published answer must remove the card: got %d", len(cards))
	}
	for _, c := range cards {
		if c.QuestionID == ids[0] {
			t.Error("answered question is still in the queue")
		}
	}
}

// Refreshing mid-stack must not reshuffle the deck under someone.
func TestShuffleIsStableForTheSameSeed(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, _ := seedQueue(t, s, 8)

	u.QueueMode = QueueShuffle
	u.QueueSeed = 12345

	first, err := s.NextCards(ctx, u, 8)
	if err != nil {
		t.Fatalf("NextCards: %v", err)
	}
	second, _ := s.NextCards(ctx, u, 8)

	a, b := cardIDs(first), cardIDs(second)
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("shuffle is not stable: %v then %v", a, b)
		}
	}

	// A different seed should generally give a different order.
	u.QueueSeed = 99999
	third, _ := s.NextCards(ctx, u, 8)
	if same(a, cardIDs(third)) {
		t.Error("a different seed produced an identical order")
	}
}

func TestShuffleStillPutsDeferredCardsLast(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, ids := seedQueue(t, s, 6)
	u.QueueMode = QueueShuffle
	u.QueueSeed = 4242

	if err := s.DeferQuestion(ctx, ids[2], u.ID); err != nil {
		t.Fatalf("DeferQuestion: %v", err)
	}

	cards, _ := s.NextCards(ctx, u, 10)
	got := cardIDs(cards)
	if got[len(got)-1] != ids[2] {
		t.Errorf("deferred card should still sort last under shuffle: %v", got)
	}
}

func TestOneSubjectModeFiltersToTheFocusSubject(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, ids := seedQueue(t, s, 3)

	// A second subject with one question of its own.
	var otherSubject, otherQuestion int64
	err := s.InTx(ctx, func(db DBTX) error {
		var err error
		otherSubject, err = UpsertSubject(ctx, db, Subject{
			Slug: "louis", Kind: "individual", DisplayName: "Louis Raymond Hale", SortOrder: 2,
		})
		if err != nil {
			return err
		}
		otherQuestion, err = UpsertImportedQuestion(ctx, db, ImportedQuestion{
			SubjectID: otherSubject, AskedOfUserID: u.ID,
			Body: "About Louis", SortOrder: 99, ImportKey: "key-louis",
		})
		return err
	})
	if err != nil {
		t.Fatalf("seed second subject: %v", err)
	}

	u.QueueMode = QueueOneSubject
	u.QueueFocusSubjectID = &otherSubject

	cards, err := s.NextCards(ctx, u, 10)
	if err != nil {
		t.Fatalf("NextCards: %v", err)
	}
	if len(cards) != 1 || cards[0].QuestionID != otherQuestion {
		t.Fatalf("one_subject mode returned %v, want just question %d", cardIDs(cards), otherQuestion)
	}

	// Without a focus set, the mode must not silently hide everything.
	u.QueueFocusSubjectID = nil
	cards, _ = s.NextCards(ctx, u, 10)
	if len(cards) != len(ids)+1 {
		t.Errorf("with no focus subject, got %d cards, want all %d", len(cards), len(ids)+1)
	}
}

func TestProgressCountsPublishedAnswersOnly(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, ids := seedQueue(t, s, 5)

	p, err := s.Progress(ctx, u.ID)
	if err != nil {
		t.Fatalf("Progress: %v", err)
	}
	if p.Answered != 0 || p.Total != 5 {
		t.Errorf("Progress = %+v, want 0 of 5", p)
	}

	if _, err := s.SaveAnswer(ctx, ids[0], u.ID, "done", false); err != nil {
		t.Fatalf("SaveAnswer: %v", err)
	}
	if _, err := s.SaveAnswer(ctx, ids[1], u.ID, "still typing", true); err != nil {
		t.Fatalf("SaveAnswer: %v", err)
	}

	p, _ = s.Progress(ctx, u.ID)
	if p.Answered != 1 {
		t.Errorf("Answered = %d, want 1 (a draft does not count)", p.Answered)
	}
	if p.Total != 5 {
		t.Errorf("Total = %d, want 5", p.Total)
	}
}

func TestSaveAnswerReplacesRatherThanDuplicating(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, ids := seedQueue(t, s, 1)

	first, err := s.SaveAnswer(ctx, ids[0], u.ID, "draft text", true)
	if err != nil {
		t.Fatalf("SaveAnswer: %v", err)
	}
	second, err := s.SaveAnswer(ctx, ids[0], u.ID, "final text", false)
	if err != nil {
		t.Fatalf("SaveAnswer: %v", err)
	}
	if first != second {
		t.Errorf("expected the same row to be updated, got %d then %d", first, second)
	}

	e, err := s.AnswerFor(ctx, ids[0], u.ID)
	if err != nil {
		t.Fatalf("AnswerFor: %v", err)
	}
	if e.Body != "final text" || e.IsDraft {
		t.Errorf("entry = %+v", e)
	}
}

func TestQuestionOwner(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	u, ids := seedQueue(t, s, 1)

	owner, err := s.QuestionOwner(ctx, ids[0])
	if err != nil {
		t.Fatalf("QuestionOwner: %v", err)
	}
	if owner != u.ID {
		t.Errorf("owner = %d, want %d", owner, u.ID)
	}
	if _, err := s.QuestionOwner(ctx, 999999); err != ErrNotFound {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func same(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
