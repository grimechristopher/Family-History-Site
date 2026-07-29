package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

func (s *Server) handleStories(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	stories, err := s.Store.ListStories(r.Context(), u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	ids := make([]int64, 0, len(stories))
	for _, st := range stories {
		ids = append(ids, st.ID)
	}
	replies, err := s.Store.RepliesForEntries(r.Context(), ids)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	photos, err := s.Store.AttachmentsForEntries(r.Context(), ids)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.signAttachments(r, photos)

	subjects, err := s.Store.Subjects(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := s.newPageData(r, "Stories")
	data.Subjects = subjects
	for _, st := range stories {
		data.Stories = append(data.Stories, storyView{
			Story:         st,
			Replies:       replies[st.ID],
			Photos:        photos[st.ID],
			IsMine:        st.AuthorUserID == u.ID,
			PhotosEnabled: s.Storage.Configured(),
			ReturnTo:      "/stories",
		})
	}
	s.render(w, r, "stories", data)
}

type storyView struct {
	store.Story
	Replies       []store.Reply
	Photos        []store.Attachment
	IsMine        bool
	PhotosEnabled bool
	ReturnTo      string
}

func (s *Server) handleCreateStory(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	title := strings.TrimSpace(r.FormValue("title"))
	body := strings.TrimSpace(r.FormValue("body"))
	if body == "" {
		http.Error(w, "a story needs something in it", http.StatusBadRequest)
		return
	}
	if title == "" {
		title = "Untitled"
	}

	var subjectID *int64
	if slug := r.FormValue("subject"); slug != "" {
		sub, err := s.Store.SubjectBySlug(r.Context(), slug)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "unknown subject", http.StatusBadRequest)
			return
		} else if err != nil {
			s.serverError(w, r, err)
			return
		}
		subjectID = &sub.ID
	}

	if _, err := s.Store.CreateStory(r.Context(), u.ID, title, body, subjectID, false); err != nil {
		s.serverError(w, r, err)
		return
	}
	back := r.FormValue("return_to")
	if back == "" {
		back = "/stories"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func (s *Server) handleDeleteStory(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad story id", http.StatusBadRequest)
		return
	}
	st, err := s.Store.Story(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Only your own words are yours to remove.
	if st.AuthorUserID != u.ID {
		http.Error(w, "that story belongs to someone else", http.StatusForbidden)
		return
	}
	if err := s.Store.DeleteStory(r.Context(), id); err != nil {
		s.serverError(w, r, err)
		return
	}
	back := r.FormValue("return_to")
	if back == "" {
		back = "/stories"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}
