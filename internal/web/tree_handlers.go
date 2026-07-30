package web

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

// treeNode is one person plus their two parents, so a template can recurse.
type treeNode struct {
	Person     *store.TreePerson
	Generation int
	Parents    []*treeNode
}

// buildTree assembles pedigrees rooted at each contributor. Depth is bounded so
// unexpected data — a person recorded as their own ancestor — cannot recurse
// forever and take the page with it.
func buildTree(people []*store.TreePerson, rootIDs []int64, maxDepth int) []*treeNode {
	byID := make(map[int64]*store.TreePerson, len(people))
	for _, p := range people {
		byID[p.ID] = p
	}

	var build func(id int64, depth int, seen map[int64]bool) *treeNode
	build = func(id int64, depth int, seen map[int64]bool) *treeNode {
		p, ok := byID[id]
		if !ok || seen[id] || depth > maxDepth {
			return nil
		}
		// A fresh set per branch, so the same ancestor appearing on both sides of
		// the family still renders in both places.
		branch := make(map[int64]bool, len(seen)+1)
		for k := range seen {
			branch[k] = true
		}
		branch[id] = true

		node := &treeNode{Person: p, Generation: depth}
		for _, parentID := range []*int64{p.FatherID, p.MotherID} {
			if parentID == nil {
				continue
			}
			if child := build(*parentID, depth+1, branch); child != nil {
				node.Parents = append(node.Parents, child)
			}
		}
		return node
	}

	var roots []*treeNode
	for _, id := range rootIDs {
		if node := build(id, 0, map[int64]bool{}); node != nil {
			roots = append(roots, node)
		}
	}
	return roots
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	people, err := s.Store.TreePeople(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rootIDs, err := s.Store.RootPeople(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := s.newPageData(r, "Family tree")
	data.Nav = "tree"
	// Four levels covers Mom and Dad through their great-grandparents, which is
	// everything imported.
	data.Tree = buildTree(people, rootIDs, 4)

	// A pedigree only reaches blood ancestors, so a step-parent and the
	// "Further Back" bucket would otherwise have no route in from here — between
	// them that is thirty questions with no way to find them. Anything with
	// questions that the pedigree does not reach is listed alongside it.
	inTree := map[string]bool{}
	var collect func(nodes []*treeNode)
	collect = func(nodes []*treeNode) {
		for _, n := range nodes {
			if n.Person.SubjectSlug != nil {
				inTree[*n.Person.SubjectSlug] = true
			}
			collect(n.Parents)
		}
	}
	collect(data.Tree)

	subjects, err := s.Store.SubjectsWithProgress(r.Context(), "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	for _, sub := range subjects {
		if sub.Total > 0 && !inTree[sub.Slug] {
			data.SubjectProgress = append(data.SubjectProgress, sub)
		}
	}

	s.render(w, r, "tree", data)
}

func (s *Server) handleSubjects(w http.ResponseWriter, r *http.Request) {
	subjects, err := s.Store.SubjectsWithProgress(r.Context(), "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data := s.newPageData(r, "Everyone")
	data.SubjectProgress = subjects
	s.render(w, r, "subjects", data)
}

// handleSubject is a person's page, shaped like a chapter: who they were, the
// questions about them, and the stories.
func (s *Server) handleSubject(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())
	slug := r.PathValue("slug")

	subject, err := s.Store.SubjectProgressBySlug(r.Context(), slug)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	members, err := s.Store.SubjectMembers(r.Context(), subject.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	unanswered, err := s.Store.ListQuestions(r.Context(), u.ID, store.QuestionFilter{
		SubjectSlug: slug, OnlyUnanswered: true,
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	answered, err := s.Store.ListQuestions(r.Context(), u.ID, store.QuestionFilter{
		SubjectSlug: slug, OnlyAnswered: true,
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	stories, err := s.Store.StoriesAboutSubject(r.Context(), subject.ID, u.ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	contributors, err := s.Store.Contributors(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	data := s.newPageData(r, subject.DisplayName)
	data.Nav = "tree"
	data.Subject = subject
	data.Contributors = contributors
	data.Members = members
	data.Unanswered = unanswered
	data.Answered = answered
	for _, st := range stories {
		data.Stories = append(data.Stories, storyView{
			Story:         st,
			IsMine:        st.AuthorUserID == u.ID,
			PhotosEnabled: s.Storage.Configured(),
			ReturnTo:      "/subjects/" + slug,
		})
	}
	s.render(w, r, "subject", data)
}

// handleFocusSubject switches the card stack to one person and sends them
// straight to it — the path from "tell me about Grandpa Louis" to answering.
func (s *Server) handleFocusSubject(w http.ResponseWriter, r *http.Request) {
	u := auth.User(r.Context())

	sub, err := s.Store.SubjectBySlug(r.Context(), r.PathValue("slug"))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.Store.SetQueueMode(r.Context(), u.ID, store.QueueOneSubject, &sub.ID); err != nil {
		s.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/cards", http.StatusSeeOther)
}

// pedigreeNode is the shape the browser gets. Deliberately small: names, years,
// where to click through to, and how much has been said.
type pedigreeNode struct {
	Name string `json:"name"`
	// Label names the line for the switcher — "Dad", "Mom" — set on roots only.
	Label string `json:"label,omitempty"`
	Years string `json:"years,omitempty"`
	Slug  string `json:"slug,omitempty"`
	// Members is set for a couple, which the great-grandparent generation is
	// drawn as: one box for the pair, since that is how their questions are
	// asked and how the book's chapters are organised.
	Members []pedigreeMember `json:"members,omitempty"`
	// Not omitempty on the counts: a zero is meaningful, and dropping it had the
	// chart rendering "undefined/62 answered".
	Total    int             `json:"total"`
	Answered int             `json:"answered"`
	Gen      int             `json:"gen"`
	Parents  []*pedigreeNode `json:"parents,omitempty"`
}

// pedigreeMember is one person inside a couple's box.
type pedigreeMember struct {
	Name  string `json:"name"`
	Years string `json:"years,omitempty"`
}

// handleTreeJSON serves the pedigree for the drawn view.
//
// The same walk backs both views, so the diagram and the list can never disagree
// about who is in the family.
func (s *Server) handleTreeJSON(w http.ResponseWriter, r *http.Request) {
	people, err := s.Store.TreePeople(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	rootIDs, err := s.Store.RootPeople(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// A couple is worth opening whether or not questions have been written for
	// them yet, so this ignores the count that link() checks.
	coupleSlug := func(p *store.TreePerson) string {
		if p.SubjectSlug != nil {
			return *p.SubjectSlug
		}
		return ""
	}

	link := func(p *store.TreePerson) string {
		// Linked only where there is something to read, matching the list view.
		if p.SubjectSlug != nil && p.QuestionCount > 0 {
			return *p.SubjectSlug
		}
		return ""
	}

	// A married pair sharing one subject is drawn as one box. Both members carry
	// the same subject slug, which is what identifies them as a couple.
	sameSubject := func(a, b *treeNode) bool {
		x, y := a.Person.SubjectSlug, b.Person.SubjectSlug
		return x != nil && y != nil && *x == *y
	}

	var convert func(n *treeNode) *pedigreeNode
	convert = func(n *treeNode) *pedigreeNode {
		p := n.Person
		out := &pedigreeNode{
			Name:     p.FullName(),
			Years:    p.Lifespan(),
			Total:    p.QuestionCount,
			Answered: p.AnsweredCount,
			Gen:      n.Generation,
			Slug:     link(p),
		}

		if len(n.Parents) == 2 && sameSubject(n.Parents[0], n.Parents[1]) {
			a, b := n.Parents[0].Person, n.Parents[1].Person
			couple := &pedigreeNode{
				Gen:      n.Parents[0].Generation,
				Total:    a.QuestionCount,
				Answered: a.AnsweredCount,
				// Always clickable, unlike an individual. A couple's page names
				// both of them and offers a story about them, so it is worth
				// opening even before any question has been asked -- and these
				// pairs currently have none, since the great-grandparent
				// questions all sit under "Further Back".
				Slug: coupleSlug(a),
				Members: []pedigreeMember{
					{Name: a.FullName(), Years: a.Lifespan()},
					{Name: b.FullName(), Years: b.Lifespan()},
				},
			}
			if a.SubjectName != nil {
				couple.Name = *a.SubjectName
			}
			out.Parents = append(out.Parents, couple)
			return out
		}

		for _, parent := range n.Parents {
			out.Parents = append(out.Parents, convert(parent))
		}
		return out
	}

	// Whose line each root is, so the switcher can say "Dad" rather than repeating
	// the full name.
	contributors, err := s.Store.Contributors(r.Context())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	labelFor := map[int64]string{}
	for _, c := range contributors {
		if c.PersonID != nil {
			labelFor[*c.PersonID] = c.DisplayName
		}
	}

	roots := buildTree(people, rootIDs, 4)
	out := make([]*pedigreeNode, 0, len(roots))
	for _, root := range roots {
		node := convert(root)
		node.Label = labelFor[root.Person.ID]
		out = append(out, node)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		s.Log.Error("encode pedigree", "err", err)
	}
}
