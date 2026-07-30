package web

import (
	"errors"
	"net/http"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

// cardsInStack is how many cards are fetched so the two behind the top one can
// peek out, making the pile look finite.
const cardsInStack = 3

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	data := s.newPageData(r, "Home")

	if u.Role == store.RoleContributor {
		p, err := s.Store.Progress(r.Context(), u.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		data.Progress = &p
		switch {
		case p.Answered == 0:
			data.Greeting = "There are some questions waiting for you."
		case p.Answered >= p.Total:
			data.Greeting = "You've answered every single one."
		default:
			data.Greeting = "Pick up where you left off."
		}
	} else {
		data.Greeting = "Everything is set up and waiting."
	}

	s.render(w, r, "home", data)
}

// stackData assembles everything the card region needs.
func (s *Server) stackData(r *http.Request, flash string) (pageData, error) {
	u := auth.User(r.Context())
	data := s.newPageData(r, "Questions")
	data.Nav = "cards"
	data.Flash = flash
	data.Mode = u.QueueMode

	cards, err := s.Store.NextCards(r.Context(), u, cardsInStack)
	if err != nil {
		return data, err
	}
	data.Cards = cards

	p, err := s.Store.Progress(r.Context(), u.ID)
	if err != nil {
		return data, err
	}
	data.Progress = &p

	// The subject picker is only meaningful in one-person-at-a-time mode, and it
	// must only offer people who actually have questions: picking an empty one
	// landed you on a dead end.
	if u.QueueMode == store.QueueOneSubject {
		withProgress, err := s.Store.SubjectsWithProgress(r.Context(), u.DisplayName, "")
		if err != nil {
			return data, err
		}
		var subjects []store.Subject
		for _, sp := range withProgress {
			if sp.Total > 0 {
				subjects = append(subjects, sp.Subject)
			}
		}
		data.Subjects = subjects

		if u.QueueFocusSubjectID != nil {
			data.Focused = true
			for _, sub := range subjects {
				if sub.ID == *u.QueueFocusSubjectID {
					data.FocusSlug = sub.Slug
					data.FocusName = sub.DisplayName
					break
				}
			}
		}
	}
	return data, nil
}

func (s *Server) handleCards(w http.ResponseWriter, r *http.Request) {
	data, err := s.stackData(r, "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.render(w, r, "cards", data)
}

// renderStack returns just the swappable region, which is what every htmx action
// on this page targets.
func (s *Server) renderStack(w http.ResponseWriter, r *http.Request, flash string) {
	data, err := s.stackData(r, flash)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderNamed(w, r, "cards", "stack-region", data)
}

func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	mode := r.FormValue("mode")
	switch mode {
	case store.QueueInOrder, store.QueueShuffle, store.QueueOneSubject:
	default:
		http.Error(w, "unknown mode", http.StatusBadRequest)
		return
	}

	// Keep whatever subject was already in focus unless a new one is named, so
	// switching to shuffle and back does not lose the choice.
	focus := u.QueueFocusSubjectID
	if slug := r.FormValue("subject"); slug != "" {
		sub, err := s.Store.SubjectBySlug(r.Context(), slug, r.FormValue("family"))
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "unknown subject", http.StatusBadRequest)
			return
		} else if err != nil {
			s.serverError(w, r, err)
			return
		}
		focus = &sub.ID
	}

	if err := s.Store.SetQueueMode(r.Context(), u.ID, mode, focus); err != nil {
		s.serverError(w, r, err)
		return
	}

	// The middleware loaded this user before the change, so update the copy in
	// context rather than re-querying.
	u.QueueMode = mode
	u.QueueFocusSubjectID = focus

	s.renderStack(w, r, "")
}

func (s *Server) handleDefer(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	id, err := questionID(r)
	if err != nil {
		http.Error(w, "bad question id", http.StatusBadRequest)
		return
	}
	if !s.ownsQuestion(w, r, id, u.ID) {
		return
	}

	if err := s.Store.DeferQuestion(r.Context(), id, u.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderStack(w, r, "Put back in the pile. It'll come round again.")
}

func (s *Server) handleAnswer(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	id, err := questionID(r)
	if err != nil {
		http.Error(w, "bad question id", http.StatusBadRequest)
		return
	}
	if !s.ownsQuestion(w, r, id, u.ID) {
		return
	}

	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		// An empty save is almost certainly a mis-tap on "Save & next". Treating
		// it as a deferral is kinder than recording an empty answer that would
		// remove the question from the pile for good.
		if err := s.Store.DeferQuestion(r.Context(), id, u.ID); err != nil {
			s.serverError(w, r, err)
			return
		}
		s.renderStack(w, r, "Nothing written yet, so we've kept that one in the pile.")
		return
	}

	if _, err := s.Store.SaveAnswer(r.Context(), id, u.ID, body, false); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderStack(w, r, "Saved. Thank you.")
}

// handleDraft is the autosave endpoint. It returns no body: the page already
// shows what was typed, and swapping anything in mid-sentence would be hostile.
func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	id, err := questionID(r)
	if err != nil {
		http.Error(w, "bad question id", http.StatusBadRequest)
		return
	}
	if !s.ownsQuestion(w, r, id, u.ID) {
		return
	}

	body := r.FormValue("body")
	if strings.TrimSpace(body) == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// An existing published answer must not be silently demoted to a draft by a
	// stray autosave.
	existing, err := s.Store.AnswerFor(r.Context(), id, u.ID)
	isDraft := true
	if err == nil && !existing.IsDraft {
		isDraft = false
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.serverError(w, r, err)
		return
	}

	if _, err := s.Store.SaveAnswer(r.Context(), id, u.ID, body, isDraft); err != nil {
		s.Log.Error("draft save failed", "question", id, "err", err)
		http.Error(w, "could not save draft", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ownsQuestion keeps one person out of another's queue. Everyone may read
// everything, but only the person a question was asked of may answer or defer it.
func (s *Server) ownsQuestion(w http.ResponseWriter, r *http.Request, questionID, userID int64) bool {
	owner, err := s.Store.QuestionOwner(r.Context(), questionID)
	if errors.Is(err, store.ErrNotFound) {
		http.Error(w, "that question no longer exists", http.StatusNotFound)
		return false
	}
	if err != nil {
		s.serverError(w, r, err)
		return false
	}
	if owner != userID {
		http.Error(w, "that question was asked of someone else", http.StatusForbidden)
		return false
	}
	return true
}
