package web

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/grimechristopher/family-history-site/internal/store"
)

// Rewording and removing a question belong to whoever runs that line.
//
// Anybody may add a question and anybody may answer one. Changing a question is
// different: it changes what somebody was asked, and if they have already answered,
// what their answer is an answer to.
func TestOnlyAnAdminCanChangeAQuestion(t *testing.T) {
	h := newHarness(t)
	path := fmt.Sprintf("/questions/%d", h.dadQuestion)
	admin := h.signIn("chris@example.com")
	dad := h.signIn("dad@example.com") // a contributor, and the one being asked

	// A contributor is refused, and is not shown a control that would be.
	if body := h.get(path, dad).Body.String(); strings.Contains(body, "/edit") {
		t.Error("a contributor is offered a control the handler would refuse")
	}
	rec := h.post(path+"/edit", url.Values{"body": {"Rewritten by a contributor"}}, dad)
	if rec.Code != 403 {
		t.Errorf("contributor rewording a question = %d, want 403", rec.Code)
	}
	if rec := h.post(path+"/delete", nil, dad); rec.Code != 403 {
		t.Errorf("contributor removing a question = %d, want 403", rec.Code)
	}

	// The admin is offered it and it works.
	if body := h.get(path, admin).Body.String(); !strings.Contains(body, path+"/edit") {
		t.Error("an admin is not offered a way to change the question")
	}
	const better = "What kind of cars did your father have?"
	rec = h.post(path+"/edit", url.Values{"body": {better}, "topic": {"Cars"}}, admin)
	if rec.Code != 303 {
		t.Fatalf("admin rewording a question = %d, want 303: %s", rec.Code, rec.Body.String())
	}

	// The wording it now asks is the wording everybody reads, including the person
	// whose card stack it is in.
	if body := h.get(path, dad).Body.String(); !strings.Contains(body, better) {
		t.Error("the reworded question is not what the page shows")
	}
	if body := h.get("/questions", dad).Body.String(); strings.Contains(body, "What kind of cars did he have?") {
		t.Error("the old wording is still in the list")
	}

	// Nothing empty. A question with nothing in it is worse than a badly worded one:
	// it is in somebody's stack and cannot be answered.
	if rec := h.post(path+"/edit", url.Values{"body": {"   "}}, admin); rec.Code != 400 {
		t.Errorf("saving an empty question = %d, want 400", rec.Code)
	}
}

// Being an admin of your own parents' line is not authority over your in-laws'.
//
// This is what reading the per-family membership buys, rather than the role on the
// user, which is true if they are an admin of any line at all.
func TestAdminOfOneLineCannotChangeAnothersQuestions(t *testing.T) {
	h := newHarness(t)
	otherFamily, otherQuestion := seedOtherFamily(t, h)

	// Dad runs the other line, and is only a contributor at home.
	ctx := context.Background()
	dadUser, err := h.store.UserByEmail(ctx, "dad@example.com")
	if err != nil {
		t.Fatalf("UserByEmail: %v", err)
	}
	octx := store.WithFamily(ctx, otherFamily)
	if err := h.store.AddMember(octx, otherFamily, dadUser.ID, store.RoleAdmin); err != nil {
		t.Fatalf("AddMember: %v", err)
	}

	dad := h.signIn("dad@example.com")
	home := fmt.Sprintf("/questions/%d", h.dadQuestion)
	if rec := h.post(home+"/edit", url.Values{"body": {"Not his to reword"}}, dad); rec.Code != 403 {
		t.Errorf("admin of another line rewording a home question = %d, want 403", rec.Code)
	}
	if rec := h.post(home+"/delete", nil, dad); rec.Code != 403 {
		t.Errorf("admin of another line removing a home question = %d, want 403", rec.Code)
	}
	if body := h.get(home, dad).Body.String(); strings.Contains(body, home+"/edit") {
		t.Error("the control is offered on a line he does not run")
	}

	// And in the line he does run, it works.
	theirs := fmt.Sprintf("/questions/%d", otherQuestion)
	if rec := h.post(theirs+"/edit", url.Values{"body": {"His to reword"}}, dad); rec.Code != 303 {
		t.Errorf("admin rewording his own line's question = %d, want 303", rec.Code)
	}
}

// Removing a question takes it off every page it appeared on, and takes nothing with
// it. The count is shown before anybody presses the button, and said again after.
func TestRemovingAQuestionTakesItOffTheSite(t *testing.T) {
	h := newHarness(t)
	admin := h.signIn("chris@example.com")
	dad := h.signIn("dad@example.com")
	path := fmt.Sprintf("/questions/%d", h.dadQuestion)

	// Dad answers it first, which is the case that matters: the answer has to survive.
	if rec := h.post(path+"/answer", url.Values{
		"body": {"A brown Nova with one door a different colour."},
	}, dad); rec.Code >= 400 {
		t.Fatalf("answering = %d: %s", rec.Code, rec.Body.String())
	}

	// The page warns that it has been answered before offering to remove it.
	page := h.get(path, admin).Body.String()
	if !strings.Contains(page, "leaves what was written") {
		t.Error("the remove control does not say the answers are kept")
	}

	if rec := h.post(path+"/delete", nil, admin); rec.Code != 303 {
		t.Fatalf("removing = %d: %s", rec.Code, rec.Body.String())
	}

	// Gone from the list, gone from the page, gone from the stack.
	if body := h.get("/questions", dad).Body.String(); strings.Contains(body, "What kind of cars") {
		t.Error("a removed question is still in the list")
	}
	if rec := h.get(path, admin); rec.Code != 404 {
		t.Errorf("a removed question still has a page: %d", rec.Code)
	}

	// But the answer is still recorded.
	n, err := h.store.AnswerCountFor(context.Background(), h.dadQuestion)
	if err != nil {
		t.Fatalf("AnswerCountFor: %v", err)
	}
	if n != 1 {
		t.Errorf("answers kept = %d, want 1", n)
	}
}

// Nothing destructive may ask for confirmation with an inline handler.
//
// script-src is 'self' with no unsafe-inline, so onsubmit="return confirm(...)" never
// runs: the browser refuses it and says nothing. The people page was written that way,
// which meant the button that removes somebody from a line asked nothing at all --
// and it looked, in the source, exactly like a button that did.
func TestConfirmationIsNotWrittenAsAnInlineHandler(t *testing.T) {
	h := newHarness(t)
	admin := h.signIn("chris@example.com")

	for _, page := range []string{"/people", fmt.Sprintf("/questions/%d", h.dadQuestion)} {
		body := h.get(page, admin).Body.String()
		if strings.Contains(body, "onsubmit=") || strings.Contains(body, "onclick=") {
			t.Errorf("%s uses an inline handler, which the CSP silently discards", page)
		}
		if !strings.Contains(body, "data-confirm=") {
			t.Errorf("%s has no confirmation on anything destructive", page)
		}
	}
}
