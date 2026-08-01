package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

// handleQuestions is the browsable list: unanswered first, then answered.
func (s *Server) handleQuestions(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	askedOf := r.URL.Query().Get("asked_of")
	// Default to your own questions. A contributor opening this page almost
	// always wants their own list, and "everyone" is only meaningful to an admin,
	// who is asked nothing themselves.
	if askedOf == "" && u.Role == store.RoleContributor {
		askedOf = u.DisplayName
	}
	if askedOf == "everyone" {
		askedOf = ""
	}

	filter := store.QuestionFilter{
		SubjectSlug: r.URL.Query().Get("subject"),
		AskedOfName: askedOf,
		FamilySlug:  r.URL.Query().Get("family"),
	}

	// Who answers in this line. Fetched before anything is counted, because it
	// decides whether the person filter still means anything.
	contributors, err := s.Store.Contributors(r.Context(), filter.FamilySlug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Choosing a line can strand the person filter: Frank answers only in the
	// Lucero line, so keeping him selected while looking at the Grime line shows a
	// blank page and no clue why. Drop the filter instead of the questions.
	//
	// Only for an admin, because for a contributor the filter is not a choice --
	// it is set to them above and pins the page to their own questions.
	if filter.AskedOfName != "" && u.Role == store.RoleAdmin {
		found := false
		for _, c := range contributors {
			if c.DisplayName == filter.AskedOfName {
				found = true
				break
			}
		}
		if !found {
			filter.AskedOfName = ""
		}
	}

	// One section at a time. With 151 waiting, stacking both meant scrolling past
	// all of them to reach the single answered one.
	show := r.URL.Query().Get("show")
	if show != "answered" {
		show = "waiting"
	}

	counts, err := s.Store.ListCounts(r.Context(), filter)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Only the section being shown is fetched.
	wanted := filter
	wanted.OnlyUnanswered = show == "waiting"
	wanted.OnlyAnswered = show == "answered"
	items, err := s.Store.ListQuestions(r.Context(), u.ID, wanted)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Scoped to the active person filter, so empty people drop out.
	subjects, err := s.Store.SubjectsWithProgress(r.Context(), filter.AskedOfName, filter.FamilySlug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Only people something has actually been recorded about. Importing the
	// brothers, sisters and cousins put ninety names in this list, most of them
	// with nothing to read, and finding the people you could actually answer for
	// meant reading past all of them.
	//
	// They are not lost: everybody is on the chart, and opening one there gives a
	// page that takes a question or a story. Doing either puts them here.
	//
	// Counted across everybody rather than narrowed to whoever is selected. This is
	// a map of the family and should say the same thing whoever is reading it -- the
	// narrowed count showed Inez three of the eleven people her line has questions
	// about and hid four great-grandparent couples, because those questions had been
	// put to her brother.
	var withSomething []store.SubjectProgress
	for _, sub := range subjects {
		if sub.AnyTotal > 0 || sub.Stories > 0 || sub.Slug == filter.SubjectSlug {
			sub.Total, sub.Answered = sub.AnyTotal, sub.AnyAnswered
			withSomething = append(withSomething, sub)
		}
	}
	subjects = withSomething

	// Their own name on every row of their own list says nothing.
	if filter.AskedOfName != "" {
		for i := range items {
			if len(items[i].SharedWith) == 0 {
				items[i].HideAskedOf = true
			}
		}
	}

	data := s.newPageData(r, "All questions")
	data.Nav = "questions"
	data.Show = show
	// This page is a viewport-height column that scrolls inside itself, so a footer
	// after it would never move. It goes at the end of the column instead.
	data.OwnFooter = true
	data.Groups = store.GroupQuestions(items)
	data.Counts = &counts
	data.SubjectProgress = subjects
	data.Contributors = contributors
	data.FilterSubject = filter.SubjectSlug
	for _, sub := range subjects {
		if sub.Slug == filter.SubjectSlug {
			data.FilterSubjectName = sub.DisplayName
			break
		}
	}
	data.FilterAskedOf = filter.AskedOfName
	// Only worth naming the line when more than one is in view: inside a chosen
	// line there is nothing to tell apart.
	data.SubjectGroups = groupSubjects(data.SubjectProgress,
		filter.FamilySlug == "" && len(FamiliesOf(r.Context())) > 1)
	data.FilterFamily = filter.FamilySlug
	data.ViewerIsAdmin = u.Role == store.RoleAdmin
	data.NothingMatches = counts.Unanswered == 0 && counts.Answered == 0

	// Choosing somebody in the rail asks for the questions alone, so the rail keeps
	// its scroll position and whichever groups were open. Only when the request
	// really is for that fragment: an htmx request from anywhere else still gets a
	// whole page.
	if r.Header.Get("HX-Request") == "true" && r.Header.Get("HX-Target") == "question-list" {
		s.renderNamed(w, r, "questions", "question-list", data)
		return
	}
	s.render(w, r, "questions", data)
}

// handleQuestion is one question with its primary answer, the Others section,
// and the reply threads.
func (s *Server) handleQuestion(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	id, err := questionID(r)
	if err != nil {
		http.Error(w, "bad question id", http.StatusBadRequest)
		return
	}

	data, err := s.questionData(r, id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	_ = u
	s.render(w, r, "question", data)
}

// answerView pairs an answer with its replies for rendering.
type answerView struct {
	store.Entry
	Replies []store.Reply
	Photos  []store.Attachment
	IsMine  bool
	// IsPrimary marks the answer from the person the question was asked of.
	IsPrimary bool
	// PhotosEnabled and ReturnTo are carried on the view so the shared partials
	// need no template helpers to reach page-level state: a partial receives only
	// the view, so $ inside it is the view and not the page.
	PhotosEnabled bool
	ReturnTo      string
}

func (s *Server) questionData(r *http.Request, id int64) (pageData, error) {
	u := auth.User(r.Context())
	data := s.newPageData(r, "Question")
	data.Nav = "questions"

	q, err := s.Store.Question(r.Context(), id)
	if err != nil {
		return data, err
	}
	data.Question = q

	// Everybody the question was put to, not just the one name on the row. A
	// question shared by four brothers has four people who might still answer it,
	// and saying so is the whole reason the page mentions it at all.
	askees, err := s.Store.QuestionAskees(r.Context(), id)
	if err != nil {
		return data, err
	}
	data.Askees = askees
	asked := make(map[int64]bool, len(askees))
	for _, a := range askees {
		asked[a.UserID] = true
		// Not said to the person themselves: to them it refers to them in the third
		// person, and the form below already says the question is theirs.
		if !a.Answered && a.UserID != u.ID {
			data.Awaiting = append(data.Awaiting, a.Name)
		}
	}

	entries, err := s.Store.AnswersTo(r.Context(), id)
	if err != nil {
		return data, err
	}

	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	replies, err := s.Store.RepliesForEntries(r.Context(), ids)
	if err != nil {
		return data, err
	}
	photos, err := s.Store.AttachmentsForEntries(r.Context(), ids)
	if err != nil {
		return data, err
	}
	s.signAttachments(r, photos)

	// AnswersTo already sorts the people asked first, which is what makes the
	// primary / Others split fall out naturally. Every one of them is primary --
	// four brothers answering in their own words are four answers in their own
	// words, not one and three bystanders.
	for _, e := range entries {
		view := answerView{
			Entry:         e,
			Replies:       replies[e.ID],
			Photos:        photos[e.ID],
			IsMine:        e.AuthorUserID == u.ID,
			IsPrimary:     asked[e.AuthorUserID],
			PhotosEnabled: s.Storage.Configured(),
			ReturnTo:      "/questions/" + strconv.FormatInt(id, 10),
		}
		if view.IsPrimary {
			data.PrimaryAnswers = append(data.PrimaryAnswers, view)
		} else {
			data.OtherAnswers = append(data.OtherAnswers, view)
		}
	}

	// Anyone may add their own answer to any question, which is the whole point
	// of the Others section.
	mine, err := s.Store.AnswerFor(r.Context(), id, u.ID)
	if err == nil {
		data.MyAnswerBody = mine.Body
		data.MyAnswerIsDraft = mine.IsDraft
	} else if !errors.Is(err, store.ErrNotFound) {
		return data, err
	}

	data.ViewerIsAskedOf = asked[u.ID]
	// Whether they run the line this question belongs to, which is what changing or
	// removing it requires. The handlers check it again -- hiding a control is a
	// courtesy, not a permission.
	runs, err := s.runsThisLine(r, id)
	if err != nil {
		return data, err
	}
	data.ViewerRunsLine = runs
	return data, nil
}

// handleQuestionAnswer records the viewer's own answer from the detail page.
// Unlike the card stack, this is open to everybody: the Others section exists so
// Chris can answer the same question in his own words.
func (s *Server) handleQuestionAnswer(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	id, err := questionID(r)
	if err != nil {
		http.Error(w, "bad question id", http.StatusBadRequest)
		return
	}
	if _, err := s.Store.Question(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "nothing to save", http.StatusBadRequest)
		return
	}
	if _, err := s.Store.SaveAnswer(r.Context(), id, u.ID, body, false); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.redirectOrFragment(w, r, "/questions/"+strconv.FormatInt(id, 10), id)
}

func (s *Server) handleReply(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	entryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad entry id", http.StatusBadRequest)
		return
	}
	exists, err := s.Store.EntryExists(r.Context(), entryID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "nothing to save", http.StatusBadRequest)
		return
	}
	if _, err := s.Store.CreateReply(r.Context(), entryID, u.ID, body); err != nil {
		s.serverError(w, r, err)
		return
	}

	// On a question page, swap just the answers back in. Redirecting reloaded the
	// page and threw the reader to the top, which for a long thread meant losing
	// their place every time they replied.
	if r.Header.Get("HX-Request") == "true" {
		questionID, err := s.Store.EntryQuestion(r.Context(), entryID)
		if err == nil && questionID != nil {
			data, err := s.questionData(r, *questionID)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			s.renderNamed(w, r, "question", "answers", data)
			return
		}
	}

	// Replies can hang off a story as well as an answer, so return to whichever
	// page the reply came from, landing on the entry rather than the top.
	back := returnTo(r, "/stories")
	http.Redirect(w, r, back+"#entry-"+strconv.FormatInt(entryID, 10), http.StatusSeeOther)
}

// redirectOrFragment re-renders the question for htmx, or redirects for a plain
// form post, so the page works with scripting switched off.
func (s *Server) redirectOrFragment(w http.ResponseWriter, r *http.Request, path string, id int64) {
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, path, http.StatusSeeOther)
		return
	}
	data, err := s.questionData(r, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderNamed(w, r, "question", "answers", data)
}

// returnTo reads where a form wants to go back to, refusing anything that is not
// a path on this site so the field cannot be used to bounce somebody elsewhere.
func returnTo(r *http.Request, fallback string) string {
	back := r.FormValue("return_to")
	if back == "" || !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") {
		return fallback
	}
	return back
}

// handleAskQuestion adds a question to a person's page.
//
// Anyone may ask anyone: Dad can add one for himself when he remembers something
// he wants to record, and Chris can ask his father something the prompts file
// never thought of. The question joins that person's card stack like any other.
func (s *Server) handleAskQuestion(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	slug := r.PathValue("slug")

	// Resolved with its line, because who may be asked depends on which family
	// this person belongs to.
	subject, err := s.Store.SubjectProgressBySlug(r.Context(), slug, r.FormValue("family"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "A question needs something in it.", http.StatusBadRequest)
		return
	}

	// Who it is for -- possibly several people, because one question is often for
	// all of them. Each must be a contributor: asking somebody who is never shown a
	// card stack would bury it.
	//
	// Scoped to the subject's line, so the list cannot offer somebody from another
	// family and a hand-made request cannot name one.
	if err := r.ParseForm(); err != nil {
		http.Error(w, "could not read the form", http.StatusBadRequest)
		return
	}
	contributors, err := s.Store.Contributors(r.Context(), subject.FamilySlug)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	byName := make(map[string]*store.User, len(contributors))
	for _, c := range contributors {
		byName[c.DisplayName] = c
	}

	var targets []*store.User
	seen := map[int64]bool{}
	for _, name := range r.Form["asked_of"] {
		c, ok := byName[name]
		if !ok {
			http.Error(w, "Choose who the question is for.", http.StatusBadRequest)
			return
		}
		if !seen[c.ID] {
			seen[c.ID] = true
			targets = append(targets, c)
		}
	}
	if len(targets) == 0 {
		http.Error(w, "Choose who the question is for.", http.StatusBadRequest)
		return
	}

	// One question, however many people are asked it. The first is recorded on the
	// row itself, the rest alongside; all of them get it in their card stack and
	// their answers gather in one place.
	id, err := s.Store.CreateUserQuestion(r.Context(), subject.ID, targets[0].ID, u.ID, nil, body)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, t := range targets[1:] {
		if err := s.Store.AskAlso(r.Context(), id, t.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	http.Redirect(w, r, "/questions/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// runsThisLine reports whether the person making the request is an admin of the line
// a question belongs to.
//
// Read from their membership of that line rather than the role on the user, which is
// true if they are an admin of any family they belong to. Being an admin of your own
// parents' line is not authority over your in-laws'.
func (s *Server) runsThisLine(r *http.Request, questionID int64) (bool, error) {
	familyID, err := s.Store.FamilyOfQuestion(r.Context(), questionID)
	if err != nil {
		return false, err
	}
	return s.adminOf(r, familyID), nil
}

// handleEditQuestion changes what a question asks.
//
// Admins only. Anybody may add a question and anybody may answer one, but rewording
// somebody else's question changes what they were asked -- and if they have already
// answered, it changes what their answer is an answer to.
func (s *Server) handleEditQuestion(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	id, err := questionID(r)
	if err != nil {
		http.Error(w, "bad question id", http.StatusBadRequest)
		return
	}

	if _, err := s.Store.Question(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}

	mayEdit, err := s.runsThisLine(r, id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !mayEdit {
		http.Error(w, "Only an admin can change a question.", http.StatusForbidden)
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "A question needs something in it.", http.StatusBadRequest)
		return
	}
	var topic *string
	if t := strings.TrimSpace(r.FormValue("topic")); t != "" {
		topic = &t
	}

	if err := s.Store.EditQuestion(r.Context(), id, u.ID, body, topic); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Log.Info("question reworded", "question", id, "by", u.DisplayName)
	http.Redirect(w, r, "/questions/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}

// handleDeleteQuestion takes a question off the site.
func (s *Server) handleDeleteQuestion(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	id, err := questionID(r)
	if err != nil {
		http.Error(w, "bad question id", http.StatusBadRequest)
		return
	}

	mayDelete, err := s.runsThisLine(r, id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	if !mayDelete {
		http.Error(w, "Only an admin can remove a question.", http.StatusForbidden)
		return
	}

	answers, err := s.Store.AnswerCountFor(r.Context(), id)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.Store.DeleteQuestion(r.Context(), id, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.Log.Info("question removed", "question", id, "answers_kept", answers, "by", u.DisplayName)

	note := "That question is off the site."
	if answers > 0 {
		note += " The " + strconv.Itoa(answers) + " answer"
		if answers != 1 {
			note += "s"
		}
		note += " to it are still recorded."
	}
	http.Redirect(w, r, "/questions?note="+url.QueryEscape(note), http.StatusSeeOther)
}
