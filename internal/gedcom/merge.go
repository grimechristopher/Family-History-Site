package gedcom

import (
	"sort"
	"strconv"
)

// MergeDuplicates folds records that describe the same person into one.
//
// A family tree assembled over years, by several people, across imports from
// other sites, ends up with the same person entered twice. Two records for
// Rogelio Holguin Barraza sit in this one: same name, same parents, both born
// 1919, nothing to tell them apart. On the chart that is two boxes side by side
// with identical names, and whichever one you click is a coin toss.
//
// The test is deliberately narrow, because the obvious rule is wrong. Same name
// and same parents is not enough: families reuse a name after a child dies, and
// they name a son after his father. Both are in this tree -- Burton LeRoy Stevens
// born 1890 is the father of Burton LeRoy Stevens born 1919, and merging those two
// would collapse a generation and make a man his own father.
//
// So a birth year is required, on both, and it must match. Two people with the
// same name and the same parents who were born in the same year are one person.
// Anything less certain is left alone: a duplicate on the chart is a small
// annoyance, and a wrongly merged pair is a lie about the family.
func (f *File) MergeDuplicates() []Merge {
	// Group by everything that has to match. Parents come from the child-family
	// links rather than the family record, so a person recorded twice under the
	// same parents groups together whichever way round the links were written.
	groups := map[string][]*Individual{}
	for _, ind := range f.Individuals {
		if ind.BirthYear == 0 {
			continue
		}
		key := ind.Given + "\x00" + ind.Surname + "\x00" +
			strconv.Itoa(ind.BirthYear) + "\x00" + f.parentKey(ind)
		groups[key] = append(groups[key], ind)
	}

	var merges []Merge
	replace := map[string]string{}
	for _, group := range groups {
		if len(group) < 2 {
			continue
		}
		// Stable regardless of map order, so an import is repeatable.
		sort.Slice(group, func(i, j int) bool { return group[i].ID < group[j].ID })
		keep := group[0]
		for _, dup := range group[1:] {
			// The surviving record takes anything the other one knew and it did
			// not. Neither is more authoritative; between them they may be more
			// complete than either.
			if keep.Sex == "" {
				keep.Sex = dup.Sex
			}
			if keep.DeathYear == 0 {
				keep.DeathYear = dup.DeathYear
			}
			keep.FamC = union(keep.FamC, dup.FamC)
			keep.FamS = union(keep.FamS, dup.FamS)

			replace[dup.ID] = keep.ID
			merges = append(merges, Merge{Kept: keep.ID, Removed: dup.ID, Name: dup.Name()})
			delete(f.Individuals, dup.ID)
		}
	}
	if len(replace) == 0 {
		return nil
	}

	// Every reference to a record that is gone now points at the one that stayed.
	for _, fam := range f.Families {
		fam.HusbandID = resolve(replace, fam.HusbandID)
		fam.WifeID = resolve(replace, fam.WifeID)
		var kids []string
		seen := map[string]bool{}
		for _, id := range fam.ChildIDs {
			id = resolve(replace, id)
			if !seen[id] {
				seen[id] = true
				kids = append(kids, id)
			}
		}
		fam.ChildIDs = kids
	}

	sort.Slice(merges, func(i, j int) bool { return merges[i].Removed < merges[j].Removed })
	return merges
}

// Merge records one fold, so an import can report what it did rather than
// quietly changing the shape of somebody's family.
type Merge struct {
	Kept    string
	Removed string
	Name    string
}

// parentKey identifies who somebody's parents are, so two records can be compared
// on it. Sorted, because the same pair may be reached through different family
// records.
func (f *File) parentKey(ind *Individual) string {
	var parents []string
	for _, famID := range ind.FamC {
		fam := f.Families[famID]
		if fam == nil {
			continue
		}
		if fam.HusbandID != "" {
			parents = append(parents, fam.HusbandID)
		}
		if fam.WifeID != "" {
			parents = append(parents, fam.WifeID)
		}
	}
	if len(parents) == 0 {
		// Nobody's parents recorded is not evidence of being the same person.
		return "\x00unknown\x00" + ind.ID
	}
	sort.Strings(parents)
	key := ""
	for _, p := range parents {
		key += p + ","
	}
	return key
}

func resolve(replace map[string]string, id string) string {
	if to, ok := replace[id]; ok {
		return to
	}
	return id
}

func union(a, b []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, list := range [][]string{a, b} {
		for _, v := range list {
			if !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}
