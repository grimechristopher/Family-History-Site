// Package subjects derives the people questions are about, and matches markdown
// headings onto them.
//
// Nothing here is hand-authored. The tree already says who these people are:
// walking three generations up from Mom and Dad yields exactly the 2 parents,
// 4 their-parents, 8 grandparents and 16 great-grandparents the questions cover.
// Generation, sex, and name tokens are then enough to attach each markdown
// heading to the right person.
package subjects

import (
	"fmt"
	"sort"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
)

// Kind values mirror the subjects.kind column.
const (
	KindIndividual = "individual"
	KindCouple     = "couple"
	KindGroup      = "group"
)

// FurtherBackSlug is the bucket for questions about deep surname lines, which
// name no specific ancestor.
const FurtherBackSlug = "further-back"

// Subject is a thing questions can be about: one person, a married couple, or a
// catch-all group.
type Subject struct {
	Slug        string
	Kind        string
	DisplayName string
	SortOrder   int
	Generation  int      // 0 = Mom/Dad, 1 = their parents, ...
	MemberIDs   []string // GEDCOM xrefs
}

// Person is an individual inside the imported window.
type Person struct {
	GedcomID   string
	Given      string
	Surname    string
	Sex        string
	BirthYear  int
	DeathYear  int
	FatherID   string
	MotherID   string
	Generation int

	// AliasSurnames holds surnames this person is commonly referred to by but
	// is not recorded under — chiefly married names. "Grandma Mary Brennan" is
	// recorded as Nora Angeline Radley.
	AliasSurnames []string

	// MarriedSurname is set for women who took a husband's name, so they can be
	// shown the way family records show them.
	MarriedSurname string
}

// FullName is the name as recorded: "Nora Angeline Radley".
func (p Person) FullName() string {
	return strings.TrimSpace(p.Given + " " + p.Surname)
}

// DisplayName follows the genealogical convention, putting the maiden name in
// parentheses between the given names and the married surname:
// "Nora Angeline (Radley) Brennan". Everyone else is unchanged.
func (p Person) DisplayName() string {
	if p.MarriedSurname == "" || p.MarriedSurname == p.Surname {
		return p.FullName()
	}
	if p.Surname == "" {
		return strings.TrimSpace(p.Given + " " + p.MarriedSurname)
	}
	return strings.TrimSpace(p.Given + " (" + p.Surname + ") " + p.MarriedSurname)
}

// Tree is the derived, bounded slice of the GEDCOM that the site uses.
type Tree struct {
	People      map[string]*Person // keyed by GEDCOM xref
	Subjects    []Subject
	RootIDs     []string // Mom and Dad
	byGen       map[int][]string
	generations int
}

// Options names the two root individuals and how far up to walk.
type Options struct {
	// RootNames are GEDCOM-form names ("Given /Surname/") of the people whose
	// ancestors are imported — Dad and Mom.
	RootNames []string
	// Generations above the roots to include. 3 gives parents, grandparents,
	// and great-grandparents.
	Generations int
	// ExtraNames are individuals to include who are not blood ancestors, such
	// as a step-parent. Resolved against the whole file, so they must be
	// unambiguous there.
	ExtraNames []string
}

// DefaultOptions matches the approved scope: Mom, Dad, and three generations up.
//
// Vera is named explicitly because she is Dad's stepmother — Peter Samuel
// Hale's second wife — so no ancestor walk reaches her, yet the prompts file
// devotes a whole block of questions to her.
func DefaultOptions() Options {
	return Options{
		RootNames:   []string{"Peter John /Hale/", "Ruth Ann /Brennan/"},
		Generations: 3,
		ExtraNames:  []string{"Vera /Lindqvist/"},
	}
}

// Derive builds the bounded tree and its subjects from a parsed GEDCOM.
func Derive(f *gedcom.File, opts Options) (*Tree, error) {
	if len(opts.RootNames) == 0 {
		return nil, fmt.Errorf("no root names given")
	}

	var rootIDs []string
	for _, name := range opts.RootNames {
		id, err := f.FindByName(name)
		if err != nil {
			return nil, fmt.Errorf("root %q: %w", name, err)
		}
		rootIDs = append(rootIDs, id)
	}

	window := f.Ancestors(rootIDs, opts.Generations)

	// Step-parents and the like are not reachable by an ancestor walk, so they
	// are named explicitly and folded in at their spouse's generation.
	for _, name := range opts.ExtraNames {
		id, err := f.FindByName(name)
		if err != nil {
			return nil, fmt.Errorf("extra individual %q: %w", name, err)
		}
		if _, already := window[id]; already {
			continue
		}
		window[id] = generationOfSpouse(f, id, window)
	}

	t := &Tree{
		People:      make(map[string]*Person, len(window)),
		RootIDs:     rootIDs,
		byGen:       map[int][]string{},
		generations: opts.Generations,
	}

	for id, gen := range window {
		ind := f.Individuals[id]
		if ind == nil {
			continue
		}
		p := &Person{
			GedcomID:      id,
			Given:         ind.Given,
			Surname:       ind.Surname,
			Sex:           ind.Sex,
			BirthYear:     ind.BirthYear,
			DeathYear:     ind.DeathYear,
			Generation:    gen,
			AliasSurnames: spouseSurnames(f, ind),
		}
		// Only women took a spouse's surname in these generations, so the
		// convention is applied on that basis rather than guessed at.
		if p.Sex == "F" {
			p.MarriedSurname = marriedSurname(f, ind, window)
		}
		if fam := f.ParentFamily(id); fam != nil {
			if _, ok := window[fam.HusbandID]; ok {
				p.FatherID = fam.HusbandID
			}
			if _, ok := window[fam.WifeID]; ok {
				p.MotherID = fam.WifeID
			}
		}
		t.People[id] = p
		t.byGen[gen] = append(t.byGen[gen], id)
	}
	for gen := range t.byGen {
		sort.Strings(t.byGen[gen])
	}

	t.Subjects = t.buildSubjects(f)
	return t, nil
}

// generationOfSpouse places a non-ancestor at the same generation as whichever
// person already in the window they are married to.
func generationOfSpouse(f *gedcom.File, id string, window map[string]int) int {
	ind := f.Individuals[id]
	if ind == nil {
		return 1
	}
	for _, famID := range ind.FamS {
		fam := f.Families[famID]
		if fam == nil {
			continue
		}
		for _, spouseID := range []string{fam.HusbandID, fam.WifeID} {
			if spouseID == id || spouseID == "" {
				continue
			}
			if gen, ok := window[spouseID]; ok {
				return gen
			}
		}
	}
	return 1
}

// marriedSurname picks which husband's name to show her under: the last one she
// married.
//
// Alma Jean Nash remarried a Whitby in 1969, so she is Alma Jean (Nash)
// Whitby even though the family grew up calling her Grandma Osgood. Alice
// Matilda Crowe married George Marsh in 1918 after Pierce Radley, so she is
// Marsh even though Radley is the husband who connects her to this tree.
// Whether a marriage ended in divorce or a death turns out not to matter: the
// latest one is the answer either way.
//
// When no marriage carries a date the order is genuinely unknowable from the
// file, so the marriage into this family wins — that is the only real signal
// available, and it matches what the family calls her. Vera Lindqvist is the
// one person this affects.
func marriedSurname(f *gedcom.File, ind *gedcom.Individual, window map[string]int) string {
	type marriage struct {
		surname  string
		year     int
		order    int
		inFamily bool
	}

	var marriages []marriage
	for i, famID := range ind.FamS {
		fam := f.Families[famID]
		if fam == nil {
			continue
		}
		spouseID := fam.HusbandID
		if spouseID == ind.ID {
			spouseID = fam.WifeID
		}
		if spouseID == "" || spouseID == ind.ID {
			continue
		}
		spouse := f.Individuals[spouseID]
		if spouse == nil || spouse.Surname == "" || spouse.Surname == ind.Surname {
			continue
		}
		_, inFamily := window[spouseID]
		marriages = append(marriages, marriage{
			surname: spouse.Surname, year: fam.MarriageYear,
			order: i, inFamily: inFamily,
		})
	}
	if len(marriages) == 0 {
		return ""
	}

	// Any dated marriage beats every undated one, and the latest date wins.
	best := marriage{year: -1, order: -1}
	for _, m := range marriages {
		if m.year > best.year || (m.year == best.year && m.order > best.order) {
			best = m
		}
	}
	if best.year > 0 {
		return best.surname
	}

	// Nothing dated: fall back to the marriage that put her in this family.
	for _, m := range marriages {
		if m.inFamily {
			return m.surname
		}
	}
	return best.surname
}

// spouseSurnames collects the surnames of everyone this individual married, so
// a woman recorded under her maiden name can still be found by her married one.
func spouseSurnames(f *gedcom.File, ind *gedcom.Individual) []string {
	seen := map[string]bool{ind.Surname: true}
	var out []string
	for _, famID := range ind.FamS {
		fam := f.Families[famID]
		if fam == nil {
			continue
		}
		for _, spouseID := range []string{fam.HusbandID, fam.WifeID} {
			if spouseID == "" || spouseID == ind.ID {
				continue
			}
			spouse := f.Individuals[spouseID]
			if spouse == nil || spouse.Surname == "" || seen[spouse.Surname] {
				continue
			}
			seen[spouse.Surname] = true
			out = append(out, spouse.Surname)
		}
	}
	sort.Strings(out)
	return out
}

// buildSubjects turns the windowed people into question targets: individuals up
// to the grandparent generation, married couples beyond that, plus the
// further-back bucket.
func (t *Tree) buildSubjects(f *gedcom.File) []Subject {
	var out []Subject
	order := 0

	// Generations 0 through 2 are individuals: Mom and Dad, their parents, and
	// their grandparents. There are real memories of these people.
	for gen := 0; gen <= 2 && gen <= t.generations; gen++ {
		for _, id := range t.byGen[gen] {
			p := t.People[id]
			order++
			out = append(out, Subject{
				// The slug stays on the recorded name, so it is stable even if
				// a marriage is added to the tree later.
				Slug:        slugify(p.FullName()),
				Kind:        KindIndividual,
				DisplayName: p.DisplayName(),
				SortOrder:   order,
				Generation:  gen,
				MemberIDs:   []string{id},
			})
		}
	}

	// Generation 3 is grouped into couples. Nobody has personal memories of a
	// great-grandparent they never met, but they may have heard about a pair.
	if t.generations >= 3 {
		for _, c := range t.couplesAtGeneration(f, 3) {
			order++
			out = append(out, c.subject(order))
		}
	}

	order++
	out = append(out, Subject{
		Slug:        FurtherBackSlug,
		Kind:        KindGroup,
		DisplayName: "Further Back",
		SortOrder:   order,
		Generation:  t.generations + 1,
	})
	return out
}

type couple struct {
	a, b *Person
}

func (c couple) subject(order int) Subject {
	names := []string{c.a.DisplayName()}
	slugParts := []string{c.a.FullName()}
	members := []string{c.a.GedcomID}
	if c.b != nil {
		names = append(names, c.b.DisplayName())
		slugParts = append(slugParts, c.b.FullName())
		members = append(members, c.b.GedcomID)
	}
	kind := KindCouple
	if c.b == nil {
		kind = KindIndividual
	}
	return Subject{
		Slug:        slugify(strings.Join(slugParts, " and ")),
		Kind:        kind,
		DisplayName: strings.Join(names, " & "),
		SortOrder:   order,
		Generation:  c.a.Generation,
		MemberIDs:   members,
	}
}

// couplesAtGeneration pairs people at a generation by the family they share as
// spouses, leaving anyone unpaired as a subject of their own.
func (t *Tree) couplesAtGeneration(f *gedcom.File, gen int) []couple {
	paired := map[string]bool{}
	var out []couple

	for _, id := range t.byGen[gen] {
		if paired[id] {
			continue
		}
		p := t.People[id]
		ind := f.Individuals[id]
		if ind == nil {
			continue
		}

		var partner *Person
		for _, famID := range ind.FamS {
			fam := f.Families[famID]
			if fam == nil {
				continue
			}
			for _, spouseID := range []string{fam.HusbandID, fam.WifeID} {
				if spouseID == "" || spouseID == id || paired[spouseID] {
					continue
				}
				if sp, inWindow := t.People[spouseID]; inWindow && sp.Generation == gen {
					partner = sp
					break
				}
			}
			if partner != nil {
				break
			}
		}

		// Put the husband first so the display name reads conventionally.
		a, b := p, partner
		if b != nil && a.Sex == "F" && b.Sex == "M" {
			a, b = b, a
		}
		paired[a.GedcomID] = true
		if b != nil {
			paired[b.GedcomID] = true
		}
		out = append(out, couple{a: a, b: b})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].a.GedcomID < out[j].a.GedcomID })
	return out
}

// PeopleAtGeneration returns the xrefs at a generation, sorted for stability.
func (t *Tree) PeopleAtGeneration(gen int) []string {
	return t.byGen[gen]
}

func slugify(s string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
