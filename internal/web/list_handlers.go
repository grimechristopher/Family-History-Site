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
	}

	counts, err := s.Store.ListCounts(r.Context(), filter)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	unanswered := filter
	unanswered.OnlyUnanswered = true
	unansweredItems, err := s.Store.ListQuestions(r.Context(), u.ID, unanswered)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	answered := filter
	answered.OnlyAnswered = true
	answeredItems, err := s.Store.ListQuestions(r.Context(), u.ID, answered)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// Scoped to the active person filter, so empty people drop out.
	subjects, err := s.Store.SubjectsWithProgress(r.Context(), filter.AskedOfName)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	contributors, err := s.Store.Contributors(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := s.newPageData(r, "All questions")
	data.Nav = "questions"
	data.Unanswered = unansweredItems
	data.Answered = answeredItems
	data.UnansweredGroups = store.GroupQuestions(unansweredItems)
	data.AnsweredGroups = store.GroupQuestions(answeredItems)
	data.Counts = &counts
	data.SubjectProgress = subjects
	data.Contributors = contributors
	data.FilterSubject = filter.SubjectSlug
	data.FilterAskedOf = filter.AskedOfName
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
	// PhotosEnabled and ReturnTo are carried on the view so the shared photo
	// partial needs no template helpers to reach page-level state.
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
	back := r.FormValue("return_to")
	if back == "" {
		back = "/stories"
	}
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
