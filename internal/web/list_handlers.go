package web

import (
	"errors"
	"net/http"
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
	data := s.newPageData(r, "All questions")
	data.Nav = "questions"
	data.Show = show
	data.Groups = store.GroupQuestions(items)
	data.Counts = &counts
	data.SubjectProgress = subjects
	data.Contributors = contributors
	data.FilterSubject = filter.SubjectSlug
	data.FilterAskedOf = filter.AskedOfName
	// Only worth naming the line when more than one is in view: inside a chosen
	// line there is nothing to tell apart.
	data.SubjectGroups = groupSubjects(data.SubjectProgress,
		filter.FamilySlug == "" && len(FamiliesOf(r.Context())) > 1)
	data.FilterFamily = filter.FamilySlug
	data.ViewerIsAdmin = u.Role == store.RoleAdmin
	data.NothingMatches = counts.Unanswered == 0 && counts.Answered == 0

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

	// AnswersTo already sorts the intended person first, which is what makes the
	// primary / Others split fall out naturally.
	for _, e := range entries {
		view := answerView{
			Entry:         e,
			Replies:       replies[e.ID],
			Photos:        photos[e.ID],
			IsMine:        e.AuthorUserID == u.ID,
			IsPrimary:     e.AuthorUserID == q.AskedOfUserID,
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

	data.ViewerIsAskedOf = q.AskedOfUserID == u.ID
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

	subject, err := s.Store.SubjectBySlug(r.Context(), slug)
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

	// Who it is for. Must be a contributor: asking a question of somebody who is
	// never shown a card stack would bury it.
	askedOf := r.FormValue("asked_of")
	contributors, err := s.Store.Contributors(r.Context(), "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	var target *store.User
	for _, c := range contributors {
		if c.DisplayName == askedOf {
			target = c
			break
		}
	}
	if target == nil {
		http.Error(w, "Choose who the question is for.", http.StatusBadRequest)
		return
	}

	id, err := s.Store.CreateUserQuestion(r.Context(), subject.ID, target.ID, u.ID, nil, body)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/questions/"+strconv.FormatInt(id, 10), http.StatusSeeOther)
}
