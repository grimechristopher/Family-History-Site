package gedcom

// Ancestors returns every individual reachable upward from rootIDs within gens
// generations, mapped to the generation at which it was first found (roots are
// generation 0).
//
// The walk is breadth-first, so when a tree loops back on itself — which real
// genealogy data does — the shallowest generation wins.
func (f *File) Ancestors(rootIDs []string, gens int) map[string]int {
	found := map[string]int{}
	frontier := map[string]bool{}

	for _, id := range rootIDs {
		if _, ok := f.Individuals[id]; !ok {
			continue
		}
		found[id] = 0
		frontier[id] = true
	}

	for gen := 1; gen <= gens; gen++ {
		next := map[string]bool{}
		for id := range frontier {
			ind := f.Individuals[id]
			if ind == nil {
				continue
			}
			for _, famID := range ind.FamC {
				fam := f.Families[famID]
				if fam == nil {
					continue
				}
				for _, parentID := range []string{fam.HusbandID, fam.WifeID} {
					if parentID == "" {
						continue
					}
					if _, ok := f.Individuals[parentID]; !ok {
						continue
					}
					if _, seen := found[parentID]; seen {
						continue
					}
					found[parentID] = gen
					next[parentID] = true
				}
			}
		}
		frontier = next
	}
	return found
}

// ParentFamily returns the family in which the individual appears as a child,
// or nil when the individual is unknown or has no recorded parents.
func (f *File) ParentFamily(id string) *Family {
	ind := f.Individuals[id]
	if ind == nil || len(ind.FamC) == 0 {
		return nil
	}
	return f.Families[ind.FamC[0]]
}

// Siblings returns everyone who shares a parent family with id, excluding id.
func (f *File) Siblings(id string) []string {
	ind := f.Individuals[id]
	if ind == nil {
		return nil
	}
	seen := map[string]bool{id: true}
	var out []string
	for _, famID := range ind.FamC {
		fam := f.Families[famID]
		if fam == nil {
			continue
		}
		for _, child := range fam.ChildIDs {
			if !seen[child] {
				seen[child] = true
				out = append(out, child)
			}
		}
	}
	return out
}

// Children returns everyone recorded as a child of a family this person is a
// spouse in.
func (f *File) Children(id string) []string {
	ind := f.Individuals[id]
	if ind == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, famID := range ind.FamS {
		fam := f.Families[famID]
		if fam == nil {
			continue
		}
		for _, child := range fam.ChildIDs {
			if !seen[child] {
				seen[child] = true
				out = append(out, child)
			}
		}
	}
	return out
}

// Collaterals finds the relatives an ancestor walk misses: the brothers and
// sisters of everybody near the roots, and those siblings' own children.
//
// An ancestor chart is a line of descent, so it leaves out precisely the people a
// family talks about most -- somebody's sister, their aunt, the cousins they grew
// up with. window is the ancestors already found, sibsUpTo how far up to take
// siblings (1 covers the roots and the roots' parents, which is somebody's own
// brothers and sisters plus their aunts and uncles), and withChildren adds the
// cousins.
//
// Returned separately from the ancestors, and keyed by the generation of the
// sibling they hang from, so each can be labelled for what it is.
func (f *File) Collaterals(window map[string]int, sibsUpTo int, withChildren bool) (siblings, children map[string]int) {
	siblings = map[string]int{}
	children = map[string]int{}

	for id, gen := range window {
		if gen > sibsUpTo {
			continue
		}
		for _, sib := range f.Siblings(id) {
			if _, isAncestor := window[sib]; isAncestor {
				continue
			}
			siblings[sib] = gen
		}
	}

	if !withChildren {
		return siblings, children
	}
	for sib := range siblings {
		for _, child := range f.Children(sib) {
			if _, isAncestor := window[child]; isAncestor {
				continue
			}
			if _, isSibling := siblings[child]; isSibling {
				continue
			}
			children[child] = 0
		}
	}
	return siblings, children
}
