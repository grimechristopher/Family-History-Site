package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

// treeNode is one person plus their two parents, so a template can recurse.
type treeNode struct {
	Person     *store.TreePerson
	Generation int
	Parents    []*treeNode
	// FamilyName names the line, for the switcher above the chart, and FamilySlug
	// puts it on every link out of the chart: a subject slug is unique inside a
	// line and not across them.
	FamilyName string
	FamilySlug string
}

// buildTree assembles pedigrees rooted at each contributor. Depth is bounded so
// unexpected data — a person recorded as their own ancestor — cannot recurse
// forever and take the page with it.
func buildTree(people []*store.TreePerson, roots []store.TreeRoot, maxDepth int) []*treeNode {
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

	var out []*treeNode
	for _, r := range roots {
		if node := build(r.PersonID, 0, map[int64]bool{}); node != nil {
			node.FamilyName = r.FamilyName
			node.FamilySlug = r.FamilySlug
			out = append(out, node)
		}
	}
	return out
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

	// Which line to open on. It used to compare the viewer's name against the
	// names of the lines, which worked when a line was called "Dad" and cannot
	// possibly match now that they are called "The Grime line" -- so everybody got
	// whichever line was drawn first, and Ashley opened on her husband's family.
	home, err := s.Store.HomeLine(r.Context(), auth.User(r.Context()).ID)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	data.HomeLine = home
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

	subjects, err := s.Store.SubjectsWithProgress(r.Context(), "", "")
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
	subjects, err := s.Store.SubjectsWithProgress(r.Context(), "", "")
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

	subject, err := s.Store.SubjectProgressBySlug(r.Context(), slug, r.URL.Query().Get("family"))
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

	// Scoped to the line the subject was resolved in, taken from the row itself
	// rather than from the query string. Without it the four subjects called
	// "further-back" all matched and every one of these pages listed the same
	// twenty-one questions drawn from all four lines.
	unanswered, err := s.Store.ListQuestions(r.Context(), u.ID, store.QuestionFilter{
		SubjectSlug: slug, FamilySlug: subject.FamilySlug, OnlyUnanswered: true,
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	answered, err := s.Store.ListQuestions(r.Context(), u.ID, store.QuestionFilter{
		SubjectSlug: slug, FamilySlug: subject.FamilySlug, OnlyAnswered: true,
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

	// The people who answer in this person's line, not everybody the viewer can
	// see. Unscoped, Rosemary's page -- an Ayres great-aunt -- offered Frank, Tony,
	// Inez, Robert and Violeta from Ashley's side, and the handler behind the form
	// then refused them with "Choose who the question is for."
	contributors, err := s.Store.Contributors(r.Context(), subject.FamilySlug)
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

	sub, err := s.Store.SubjectBySlug(r.Context(), r.PathValue("slug"), r.FormValue("family"))
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
	// Family is the line this box belongs to, so a link out of the chart lands on
	// the right person: every line has a subject called "further-back".
	Family string `json:"family,omitempty"`
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

	// Kin is this person's children, each carrying their own children in turn.
	// A pedigree has no room for them -- every box has exactly two parents above
	// it and nothing beside it -- so they are sent along and drawn only when
	// somebody opens a person up.
	Kin []*pedigreeNode `json:"kin,omitempty"`
	// OnLine marks the child who is already on the pedigree, so opening a
	// grandparent reads as "your dad, and his brother and sister" rather than as
	// three strangers.
	OnLine bool `json:"onLine,omitempty"`
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

	link := func(p *store.TreePerson) string {
		// Everybody with a page is clickable, whether or not anything has been asked
		// about them yet. Somebody with no questions is exactly who you most want to
		// reach: their page is where a story goes, and where a question about them
		// gets added. Requiring a question first meant a great-grandmother nobody had
		// written a prompt for was a dead box on the chart, reachable only by typing
		// her address.
		if p.SubjectSlug != nil {
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

	childrenOf := childIndex(people)

	// kinOf is a person's children with their children under them: two generations,
	// which from a grandparent is the aunts and uncles and then the cousins. Two
	// because that is the whole of what the collateral walk imports, and because a
	// third would be a chart nobody could read.
	//
	// onLine is whoever is already drawn on the pedigree above this person, so the
	// opened chart can say which of the children you came in through.
	var kinOf func(ids []int64, onLine int64, depth int) []*pedigreeNode
	kinOf = func(ids []int64, onLine int64, depth int) []*pedigreeNode {
		if depth == 0 {
			return nil
		}
		var out []*pedigreeNode
		seen := map[int64]bool{}
		for _, id := range ids {
			for _, c := range childrenOf[id] {
				if seen[c.ID] {
					// Both parents are on the chart, so every child of the marriage
					// is reached twice.
					continue
				}
				seen[c.ID] = true
				out = append(out, &pedigreeNode{
					Name:     c.FullName(),
					Years:    c.Lifespan(),
					Total:    c.QuestionCount,
					Answered: c.AnsweredCount,
					Slug:     link(c),
					OnLine:   c.ID == onLine,
					Kin:      kinOf([]int64{c.ID}, 0, depth-1),
				})
			}
		}
		return out
	}

	// onLine is the person directly below this one on the pedigree -- the child you
	// came in through -- so opening a box can mark them among their brothers and
	// sisters.
	var convert func(n *treeNode, onLine int64) *pedigreeNode
	convert = func(n *treeNode, onLine int64) *pedigreeNode {
		p := n.Person
		out := &pedigreeNode{
			Name:     p.FullName(),
			Years:    p.Lifespan(),
			Total:    p.QuestionCount,
			Answered: p.AnsweredCount,
			Gen:      n.Generation,
			Slug:     link(p),
			Kin:      kinOf([]int64{p.ID}, onLine, 2),
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
				Slug: link(a),
				Members: []pedigreeMember{
					{Name: a.FullName(), Years: a.Lifespan()},
					{Name: b.FullName(), Years: b.Lifespan()},
				},
			}
			if a.SubjectName != nil {
				couple.Name = *a.SubjectName
			}
			// The children of the marriage, gathered from both of them so a child
			// recorded under only one parent is not lost.
			couple.Kin = kinOf([]int64{a.ID, b.ID}, p.ID, 2)
			out.Parents = append(out.Parents, couple)
			return out
		}

		for _, parent := range n.Parents {
			out.Parents = append(out.Parents, convert(parent, p.ID))
		}
		return out
	}

	roots := buildTree(people, rootIDs, 4)
	out := make([]*pedigreeNode, 0, len(roots))
	for _, root := range roots {
		node := convert(root, 0)
		node.Label = root.FamilyName
		stampFamily(node, root.FamilySlug)
		out = append(out, node)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		s.Log.Error("encode pedigree", "err", err)
	}
}

// childIndex maps a person to their children, so the chart can be opened downward
// as well as read upward.
//
// Built from the same rows the pedigree is built from rather than queried again:
// the walk that imports a line brings in the brothers and sisters and their
// children, so everything needed is already here.
func childIndex(people []*store.TreePerson) map[int64][]*store.TreePerson {
	byParent := map[int64][]*store.TreePerson{}
	for _, p := range people {
		for _, parent := range []*int64{p.FatherID, p.MotherID} {
			if parent != nil {
				byParent[*parent] = append(byParent[*parent], p)
			}
		}
	}
	// Eldest first, which is how a family lists itself.
	for _, kids := range byParent {
		sort.SliceStable(kids, func(i, j int) bool {
			a, b := kids[i].BirthYear, kids[j].BirthYear
			switch {
			case a != nil && b != nil:
				return *a < *b
			case a != nil:
				return true
			case b != nil:
				return false
			}
			return kids[i].ID < kids[j].ID
		})
	}
	return byParent
}

// stampFamily marks every box in one line's chart with that line, including the
// children hanging off it. Done in one pass afterwards rather than threaded
// through the conversion, because every node in a chart is in the same line by
// construction -- the walk only ever follows links within one.
func stampFamily(n *pedigreeNode, slug string) {
	n.Family = slug
	for _, p := range n.Parents {
		stampFamily(p, slug)
	}
	for _, k := range n.Kin {
		stampFamily(k, slug)
	}
}
