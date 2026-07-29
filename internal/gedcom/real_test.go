package gedcom

import (
	"os"
	"testing"
)

// The subjects in mapping/subjects.yaml are keyed on exact GEDCOM names. If any
// of these stops resolving to exactly one individual, the import will file
// questions under the wrong ancestor, so this guards the real file.
//
// Run with:
//
//	REAL_GEDCOM="/home/chris/Documents/Github/genealogy-export/Brennan-Hale Tree.ged" go test ./internal/gedcom/
func TestRealGedcomResolvesMappedNames(t *testing.T) {
	path := os.Getenv("REAL_GEDCOM")
	if path == "" {
		t.Skip("REAL_GEDCOM not set")
	}

	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer fh.Close()

	f, err := Parse(fh)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	t.Logf("individuals=%d families=%d", len(f.Individuals), len(f.Families))

	if len(f.Individuals) < 2000 {
		t.Errorf("individuals = %d, expected around 2073", len(f.Individuals))
	}
	if len(f.Families) < 600 {
		t.Errorf("families = %d, expected around 697", len(f.Families))
	}

	// Mom and Dad are unambiguous, so they can be resolved against the whole
	// file. Everyone else is resolved within the ancestor window, which is how
	// the importer works.
	dad, err := f.FindByName("Peter John /Hale/")
	if err != nil {
		t.Fatalf("Dad: %v", err)
	}
	mom, err := f.FindByName("Ruth Ann /Brennan/")
	if err != nil {
		t.Fatalf("Mom: %v", err)
	}

	anc := f.Ancestors([]string{dad, mom}, 3)
	byGen := map[int]int{}
	for _, gen := range anc {
		byGen[gen]++
	}
	t.Logf("ancestors within 3 generations: %d (gen0=%d gen1=%d gen2=%d gen3=%d)",
		len(anc), byGen[0], byGen[1], byGen[2], byGen[3])

	// Mom, Dad, their 4 parents, 8 grandparents, 16 great-grandparents. Real
	// data has gaps, so these are floors rather than equalities.
	if byGen[0] != 2 {
		t.Errorf("generation 0 = %d, want 2 (Mom and Dad)", byGen[0])
	}
	if byGen[1] < 4 {
		t.Errorf("generation 1 = %d, want at least 4", byGen[1])
	}
	if byGen[2] < 8 {
		t.Errorf("generation 2 = %d, want at least 8", byGen[2])
	}

	// Every name mapping/subjects.yaml will key on must resolve to exactly one
	// individual inside that window.
	for _, name := range []string{
		"Peter John /Hale/",
		"Ruth Ann /Brennan/",
		"Peter Samuel /Hale/",
		"Margaret Irene /Ward/",
		"Louis Raymond /Hale/",
		"Margaret Mary /Fletcher/",
		"Clarence Vernon /Ward/",
		"Margaret Lucille /Alderman/",
		"Edward Robert /Brennan/",
		"Alice May /Fletcher/",
		"Bertram Lyle /Fletcher/",
		"Alma Jean /Nash/",
	} {
		id, err := f.FindByNameIn(name, anc)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		ind := f.Individuals[id]
		t.Logf("%-28s %-18s gen%d b.%d d.%d", name, id, anc[id], ind.BirthYear, ind.DeathYear)
	}
}

// The window restriction must not paper over an ambiguity that is genuinely
// inside the window.
func TestRealGedcomDuplicateOutsideWindowIsExcluded(t *testing.T) {
	path := os.Getenv("REAL_GEDCOM")
	if path == "" {
		t.Skip("REAL_GEDCOM not set")
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer fh.Close()
	f, err := Parse(fh)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	// Unrestricted, this name is ambiguous — an Ancestry merge duplicate.
	if _, err := f.FindByName("Bertram Lyle /Fletcher/"); err == nil {
		t.Error("expected the unrestricted lookup to report an ambiguity")
	}

	dad, _ := f.FindByName("Peter John /Hale/")
	mom, _ := f.FindByName("Ruth Ann /Brennan/")
	anc := f.Ancestors([]string{dad, mom}, 3)

	id, err := f.FindByNameIn("Bertram Lyle /Fletcher/", anc)
	if err != nil {
		t.Fatalf("restricted lookup should be unambiguous: %v", err)
	}
	t.Logf("Bertram Lyle Osgood resolves to %s at generation %d", id, anc[id])
}

