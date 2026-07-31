package web

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/grimechristopher/family-history-site/internal/store"
)

// A photograph belongs to everybody in it, not to one person's answer. That is the
// whole point: a picture of two brothers has to appear on both their pages, and a
// picture somebody found in a box has to exist before anybody has written about it.
func TestAPhotographBelongsToEverybodyInIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	chris := h.signIn("chris@example.com")

	fam, err := h.store.FamilyBySlug(ctx, "home")
	if err != nil {
		t.Fatalf("FamilyBySlug: %v", err)
	}
	famCtx := store.WithFamily(ctx, fam.ID)
	me, err := h.store.UserByEmail(ctx, "chris@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}

	// Two brothers, seeded here rather than borrowed from the fixture so the test
	// says what it needs.
	var one, two *store.Subject
	err = h.store.InTx(famCtx, func(db store.DBTX) error {
		for i, who := range []struct{ slug, name string }{
			{"frank-in-photo", "Frank Lucero"},
			{"robert-in-photo", "Robert Arturo Lucero"},
		} {
			id, err := store.UpsertSubject(famCtx, db, store.Subject{
				Slug: who.slug, Kind: "individual", DisplayName: who.name, SortOrder: 80 + i,
			})
			if err != nil {
				return err
			}
			s := &store.Subject{ID: id, Slug: who.slug, DisplayName: who.name}
			if i == 0 {
				one = s
			} else {
				two = s
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed subjects: %v", err)
	}

	photoID, err := h.store.CreatePhoto(famCtx, fam.ID, store.Photo{
		StoragePath: "test/photo.jpg", Caption: "Bassett High Gymnastics!",
		MimeType: "image/jpeg", SizeBytes: 1234, UploadedBy: me.ID,
	})
	if err != nil {
		t.Fatalf("CreatePhoto: %v", err)
	}
	for _, s := range []*store.Subject{one, two} {
		if err := h.store.TagPhotoPerson(famCtx, photoID, s.ID, me.ID, nil, nil); err != nil {
			t.Fatalf("TagPhotoPerson: %v", err)
		}
	}

	// It is in both galleries.
	for _, s := range []*store.Subject{one, two} {
		photos, err := h.store.PhotosOfSubject(famCtx, s.ID)
		if err != nil {
			t.Fatalf("PhotosOfSubject: %v", err)
		}
		var found bool
		for _, p := range photos {
			if p.ID == photoID {
				found = true
			}
		}
		if !found {
			t.Errorf("the photograph is missing from %s's gallery", s.DisplayName)
		}
	}

	// A point is a percentage, so it stays on the face at any size, and repeating a
	// tag moves it rather than failing -- which is what dragging a pin is.
	x, y := 23.35, 23.98
	if err := h.store.TagPhotoPerson(famCtx, photoID, one.ID, me.ID, &x, &y); err != nil {
		t.Fatalf("placing a pin: %v", err)
	}
	moved := 60.0
	if err := h.store.TagPhotoPerson(famCtx, photoID, one.ID, me.ID, &moved, &y); err != nil {
		t.Fatalf("moving a pin: %v", err)
	}
	people, err := h.store.PhotoPeople(famCtx, photoID)
	if err != nil {
		t.Fatalf("PhotoPeople: %v", err)
	}
	var placed, unplaced int
	for _, p := range people {
		if p.Placed() {
			placed++
			if *p.PointX != moved {
				t.Errorf("the pin is at %v, want %v after being moved", *p.PointX, moved)
			}
		} else {
			unplaced++
		}
	}
	if placed != 1 || unplaced != 1 {
		t.Errorf("placed %d and unplaced %d, want one of each", placed, unplaced)
	}

	// Writing about it, and replying, are ordinary entries and replies.
	rec := h.post("/photos/"+itoa(photoID)+"/stories", url.Values{
		"body": {"Placeholder."},
	}, chris)
	if rec.Code != 303 {
		t.Fatalf("writing about a photograph: status %d, body %s", rec.Code, rec.Body.String())
	}
	written, err := h.store.StoriesAboutPhoto(famCtx, photoID, me.ID)
	if err != nil || len(written) != 1 {
		t.Fatalf("StoriesAboutPhoto: %d stories, %v", len(written), err)
	}

	// The page itself renders, with breadcrumbs and the pins carried as data rather
	// than as a style attribute -- the Content-Security-Policy forbids inline
	// styles, and a style attribute here is ignored in silence.
	page := h.get("/photos/"+itoa(photoID), chris).Body.String()
	if !strings.Contains(page, `class="crumbs"`) {
		t.Error("the photograph page has no breadcrumbs")
	}
	// Never with a style attribute: style-src is 'self' with no unsafe-inline, so a
	// style attribute is ignored in silence and the pins stack in the corner.
	if strings.Contains(page, `style="left:`) {
		t.Error("a pin is positioned with a style attribute, which the CSP ignores")
	}
	// The pins only exist when there is a picture to put them on, and storage is not
	// configured in a test, so this is checked only when one rendered.
	if strings.Contains(page, "<img") && !strings.Contains(page, `data-x=`) {
		t.Error("the pins carry no position for the script to apply")
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
