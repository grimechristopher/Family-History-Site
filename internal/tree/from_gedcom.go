package tree

import (
	"sort"
	"strconv"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
)

// FromGedcom converts a parsed GEDCOM into the JSON shape, once, so the family
// never has to touch a GEDCOM again.
//
// Names are normalised on the way through: the capitals are the file's convention,
// not anybody's name. Ids are made from the names rather than kept as xrefs, because
// "@I372728977243@" tells a reader nothing and "louis-j-lucero" tells them who they
// are looking at.
// keep names the xrefs to include, or is nil for everybody. Filtering happens here
// rather than afterwards because the ids in the JSON are readable names, not the
// xrefs a window is expressed in -- comparing the two silently kept nobody.
func FromGedcom(f *gedcom.File, keep map[string]bool) *File {
	wanted := func(xref string) bool { return keep == nil || keep[xref] }
	ids := make(map[string]string, len(f.Individuals))
	taken := map[string]bool{}

	// Sorted, so the same GEDCOM always yields the same ids and a re-export produces
	// a file you can diff against the last one.
	xrefs := make([]string, 0, len(f.Individuals))
	for xref := range f.Individuals {
		xrefs = append(xrefs, xref)
	}
	sort.Strings(xrefs)

	for _, xref := range xrefs {
		if !wanted(xref) {
			continue
		}
		ind := f.Individuals[xref]
		base := slug(NormaliseName(ind.Given) + " " + NormaliseName(ind.Surname))
		if base == "" {
			base = "unnamed"
		}
		id := base
		// Two people can share a name -- a son named after his father. The year
		// tells them apart, and a counter covers the rest.
		if taken[id] && ind.BirthYear != 0 {
			id = base + "-" + strconv.Itoa(ind.BirthYear)
		}
		for n := 2; taken[id]; n++ {
			id = base + "-" + strconv.Itoa(n)
		}
		taken[id] = true
		ids[xref] = id
	}

	out := &File{People: make([]Person, 0, len(ids))}
	for _, xref := range xrefs {
		if !wanted(xref) {
			continue
		}
		ind := f.Individuals[xref]
		p := Person{
			ID:      ids[xref],
			Given:   NormaliseName(ind.Given),
			Surname: NormaliseName(ind.Surname),
			Sex:     ind.Sex,
			Born:    ind.BirthYear,
			Died:    ind.DeathYear,
		}

		// Parents, from whichever family this person is a child in.
		for _, famID := range ind.FamC {
			fam := f.Families[famID]
			if fam == nil {
				continue
			}
			// A link to somebody outside the window is dropped rather than left
			// dangling: the file must never name a person it does not contain.
			if wanted(fam.HusbandID) {
				p.Father = ids[fam.HusbandID]
			}
			if wanted(fam.WifeID) {
				p.Mother = ids[fam.WifeID]
			}
		}

		// Marriages, recorded once each -- from the husband's side where there is
		// one, so the file does not say the same wedding twice.
		for _, famID := range ind.FamS {
			fam := f.Families[famID]
			if fam == nil {
				continue
			}
			other := fam.WifeID
			if fam.HusbandID != xref {
				if fam.WifeID != xref {
					continue
				}
				// This person is the wife; only record it here when there is no
				// husband to record it from.
				if fam.HusbandID != "" {
					continue
				}
				other = ""
			}
			if other == "" || !wanted(other) {
				continue
			}
			p.Marriages = append(p.Marriages, Marriage{
				To: ids[other], Year: fam.MarriageYear, Divorced: fam.Divorced,
			})
		}
		sort.SliceStable(p.Marriages, func(i, j int) bool {
			return p.Marriages[i].Year < p.Marriages[j].Year
		})

		out.People = append(out.People, p)
	}
	return out
}

// slug makes a readable id. The same rules the rest of the site uses for addresses.
func slug(s string) string {
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
