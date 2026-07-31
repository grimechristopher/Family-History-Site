// Package tree reads and writes the family as JSON.
//
// A GEDCOM export was never a good thing to depend on. It is a 1980s line format,
// it arrives from whatever site the family happens to be using, it carries a
// hundred thousand lines of sources and citations that nothing here reads, and it
// puts surnames in capitals -- so the tree said LUCERO and OERGEL because a
// genealogy program from another decade said so.
//
// This is the same information in a file somebody can open, read, and correct. It
// builds the same in-memory tree the GEDCOM parser does, so everything downstream --
// the ancestor walk, the collaterals, the duplicate merge -- is unchanged and
// untouched by the swap.
package tree

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
)

// Person is somebody in the family.
//
// Identified by a readable id rather than a GEDCOM xref, because the whole point of
// this file is that a person can find their grandmother in it and fix her name.
type Person struct {
	ID      string `json:"id"`
	Given   string `json:"given"`
	Surname string `json:"surname,omitempty"`
	// M, F, or empty when unrecorded. It decides pronouns in a generated question
	// and nothing else.
	Sex  string `json:"sex,omitempty"`
	Born int    `json:"born,omitempty"`
	Died int    `json:"died,omitempty"`
	// Parents, by id. Either may be missing: plenty of records have one.
	Father string `json:"father,omitempty"`
	Mother string `json:"mother,omitempty"`
	// Marriages, in the order they happened. The order matters: it decides the
	// surname somebody was last known by, which is the name a family uses.
	Marriages []Marriage `json:"marriages,omitempty"`
}

// Marriage is one marriage, from the point of view of the person carrying it.
type Marriage struct {
	To       string `json:"to"`
	Year     int    `json:"year,omitempty"`
	Divorced bool   `json:"divorced,omitempty"`
}

// File is the whole tree.
type File struct {
	People []Person `json:"people"`
}

// Load reads the JSON and builds the tree the rest of the code works on.
func Load(r io.Reader) (*gedcom.File, error) {
	var doc File
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("read tree: %w", err)
	}
	return doc.Build()
}

// Build turns the people into the same structure a GEDCOM parse produces.
//
// The families are worked out rather than written down. A GEDCOM makes you declare
// a family record and then point at it from both directions, which is a filing
// system rather than a fact; here a person says who their parents are and who they
// married, and the groupings fall out of that. Two people with the same pair of
// parents are siblings because that is what being siblings means.
func (doc *File) Build() (*gedcom.File, error) {
	f := &gedcom.File{
		Individuals: make(map[string]*gedcom.Individual, len(doc.People)),
		Families:    map[string]*gedcom.Family{},
	}

	for i := range doc.People {
		p := &doc.People[i]
		if p.ID == "" {
			return nil, fmt.Errorf("tree: somebody has no id (%q %q)", p.Given, p.Surname)
		}
		if _, dup := f.Individuals[p.ID]; dup {
			return nil, fmt.Errorf("tree: two people share the id %q", p.ID)
		}
		f.Individuals[p.ID] = &gedcom.Individual{
			ID:        p.ID,
			Given:     p.Given,
			Surname:   p.Surname,
			Sex:       p.Sex,
			BirthYear: p.Born,
			DeathYear: p.Died,
		}
	}

	// A family is a pair of parents, or a couple. Keyed on the pair so the parent
	// family and the marriage are the same record, which is what lets a marriage
	// year describe the family its children are in.
	family := func(a, b string) *gedcom.Family {
		first, second := a, b
		if first > second {
			first, second = second, first
		}
		key := "f:" + first + "+" + second
		fam, ok := f.Families[key]
		if !ok {
			fam = &gedcom.Family{ID: key, HusbandID: a, WifeID: b}
			f.Families[key] = fam
		}
		return fam
	}

	// Parents first, so a marriage found later attaches to the family the children
	// are already in.
	for i := range doc.People {
		p := &doc.People[i]
		if p.Father == "" && p.Mother == "" {
			continue
		}
		for _, parent := range []string{p.Father, p.Mother} {
			if parent != "" && f.Individuals[parent] == nil {
				return nil, fmt.Errorf("tree: %s names %s as a parent, who is not in the file", p.ID, parent)
			}
		}
		fam := family(p.Father, p.Mother)
		fam.ChildIDs = append(fam.ChildIDs, p.ID)
		f.Individuals[p.ID].FamC = append(f.Individuals[p.ID].FamC, fam.ID)
	}

	for i := range doc.People {
		p := &doc.People[i]
		for _, m := range p.Marriages {
			if f.Individuals[m.To] == nil {
				return nil, fmt.Errorf("tree: %s is married to %s, who is not in the file", p.ID, m.To)
			}
			// Recorded from both sides in the JSON or only one -- either way it is
			// one family, because the key is the pair.
			fam := family(p.ID, m.To)
			if m.Year != 0 {
				fam.MarriageYear = m.Year
			}
			if m.Divorced {
				fam.Divorced = true
			}
		}
	}

	// Spouse links, and stable child order. Sorted rather than left in file order so
	// two runs of the importer produce the same subjects in the same places.
	for _, fam := range f.Families {
		for _, id := range []string{fam.HusbandID, fam.WifeID} {
			if id == "" {
				continue
			}
			ind := f.Individuals[id]
			if !contains(ind.FamS, fam.ID) {
				ind.FamS = append(ind.FamS, fam.ID)
			}
		}
		sort.SliceStable(fam.ChildIDs, func(i, j int) bool {
			a, b := f.Individuals[fam.ChildIDs[i]], f.Individuals[fam.ChildIDs[j]]
			if a.BirthYear != b.BirthYear {
				// Eldest first; unknown years last, where they cannot reorder anybody.
				if a.BirthYear == 0 {
					return false
				}
				if b.BirthYear == 0 {
					return true
				}
				return a.BirthYear < b.BirthYear
			}
			return a.ID < b.ID
		})
	}

	return f, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// NormaliseName fixes the capitals a genealogy program left behind.
//
// "Nash Ignacio LUCERO" and "Christopher Frank OERGEL" are not how anybody writes
// their own name; the shouting is a convention of the file, not a fact about the
// family. Only words that are entirely capitals are touched, so an initial like "M."
// is left exactly as it is, and a name somebody has deliberately written as "de la
// Cruz" or "McDonald" is never rewritten.
func NormaliseName(name string) string {
	words := strings.Fields(name)
	for i, w := range words {
		if !isShouted(w) {
			continue
		}
		words[i] = titleCase(w)
	}
	return strings.Join(words, " ")
}

// isShouted reports whether a word is all capitals and long enough for that to be a
// decision rather than an initial. "M." and "J" stay; "LUCERO" does not.
func isShouted(w string) bool {
	letters := 0
	for _, r := range w {
		if unicode.IsLetter(r) {
			if unicode.IsLower(r) {
				return false
			}
			letters++
		}
	}
	return letters > 1
}

// titleCase capitalises the first letter of each part, so a hyphenated or
// apostrophised name comes out as somebody would write it: MARY-JANE to Mary-Jane,
// O'BRIEN to O'Brien.
func titleCase(w string) string {
	var b strings.Builder
	startOfPart := true
	for _, r := range strings.ToLower(w) {
		switch {
		case startOfPart && unicode.IsLetter(r):
			b.WriteRune(unicode.ToUpper(r))
			startOfPart = false
		case r == '-' || r == '\'' || r == '’' || r == ' ' || r == '.':
			b.WriteRune(r)
			startOfPart = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
