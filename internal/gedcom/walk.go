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
