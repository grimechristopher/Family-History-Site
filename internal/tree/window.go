package tree

import "github.com/grimechristopher/family-history-site/internal/gedcom"

// Window is the people the site actually reads: the ancestors of each person a line
// is drawn from, their brothers and sisters, those siblings' children, and the
// spouses of all of them.
//
// Everything else in a genealogy export is somebody else's family. A tree built on
// Ancestry has thousands of people in it reached through fifth cousins and their
// in-laws, and the whole point of keeping this as JSON is that somebody can open it
// and correct their aunt's name -- which nobody will do in four thousand records
// they have never heard of.
//
// The parameters are the importer's own, so the file holds exactly what the import
// will look for and nothing else.
func Window(f *gedcom.File, rootIDs []string, generations, siblingsUpTo int, cousins bool, extra []string) map[string]bool {
	keep := map[string]bool{}

	for id := range f.Ancestors(rootIDs, generations) {
		keep[id] = true
	}
	if siblingsUpTo >= 0 {
		sibs, kids := f.Collaterals(f.Ancestors(rootIDs, generations), siblingsUpTo, cousins)
		for id := range sibs {
			keep[id] = true
		}
		for id := range kids {
			keep[id] = true
		}
	}
	for _, name := range extra {
		if id, err := f.FindByName(name); err == nil {
			keep[id] = true
		}
	}

	// Spouses of everybody kept. A married surname is the name a family actually
	// uses -- "Lori Ann (Ayres) Grime" -- and it comes from the marriage, so the
	// husband has to be in the file for the wife's name to be right. It also gives
	// the great-grandparent couples both halves.
	for id := range map[string]bool(keep) {
		ind := f.Individuals[id]
		if ind == nil {
			continue
		}
		for _, famID := range ind.FamS {
			fam := f.Families[famID]
			if fam == nil {
				continue
			}
			for _, spouse := range []string{fam.HusbandID, fam.WifeID} {
				if spouse != "" && spouse != id {
					keep[spouse] = true
				}
			}
		}
	}

	return keep
}

// Only keeps the people in the window, and drops every link that points outside it
// so the file never refers to somebody it does not contain.
func (doc *File) Only(keep map[string]bool) *File {
	out := &File{People: make([]Person, 0, len(keep))}
	for _, p := range doc.People {
		if !keep[p.ID] {
			continue
		}
		if p.Father != "" && !keep[p.Father] {
			p.Father = ""
		}
		if p.Mother != "" && !keep[p.Mother] {
			p.Mother = ""
		}
		var marriages []Marriage
		for _, m := range p.Marriages {
			if keep[m.To] {
				marriages = append(marriages, m)
			}
		}
		p.Marriages = marriages
		out.People = append(out.People, p)
	}
	return out
}
