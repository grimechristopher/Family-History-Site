package tree

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
)

// The JSON has to build the same tree the GEDCOM parser does, because everything
// downstream -- the ancestor walk, the collaterals, the duplicate merge -- reads
// that structure and knows nothing about where it came from.
func TestJSONBuildsTheSameTreeAsAGedcom(t *testing.T) {
	const doc = `{"people":[
	  {"id":"louis","given":"Louis J","surname":"Lucero","sex":"M","born":1918,"died":2008,
	   "marriages":[{"to":"raquel","year":1950}]},
	  {"id":"raquel","given":"Raquel","surname":"Holguin","sex":"F","born":1922},
	  {"id":"robert","given":"Robert Arturo","surname":"Lucero","sex":"M","born":1958,
	   "father":"louis","mother":"raquel"},
	  {"id":"frank","given":"Frank","surname":"Lucero","sex":"M","born":1960,
	   "father":"louis","mother":"raquel"},
	  {"id":"ines","given":"Ines","surname":"Lucero","sex":"F","father":"louis","mother":"raquel"}
	]}`

	f, err := Load(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Individuals) != 5 {
		t.Fatalf("%d people, want 5", len(f.Individuals))
	}

	// Siblings fall out of having the same parents, rather than being declared.
	sibs := f.Siblings("robert")
	if len(sibs) != 2 {
		t.Errorf("Robert has %d siblings (%v), want Frank and Ines", len(sibs), sibs)
	}

	// The children of a marriage, eldest first, with an unknown year last where it
	// cannot reorder anybody.
	kids := f.Children("louis")
	want := []string{"robert", "frank", "ines"}
	if len(kids) != len(want) {
		t.Fatalf("Louis has %v, want %v", kids, want)
	}
	for i := range want {
		if kids[i] != want[i] {
			t.Errorf("child %d is %s, want %s", i, kids[i], want[i])
		}
	}

	// The ancestor walk, which is what the whole import is built on.
	window := f.Ancestors([]string{"robert"}, 2)
	if window["louis"] != 1 || window["raquel"] != 1 || window["robert"] != 0 {
		t.Errorf("the walk placed people at %v", window)
	}

	// The marriage attached to the family the children are in, so a married surname
	// can be worked out.
	fam := f.ParentFamily("robert")
	if fam == nil || fam.MarriageYear != 1950 {
		t.Errorf("the marriage year did not reach the family the children are in: %+v", fam)
	}

	// And a name is findable the way the config asks for it.
	if id, err := f.FindByName("Robert Arturo /Lucero/"); err != nil || id != "robert" {
		t.Errorf("FindByName gave %q, %v", id, err)
	}
}

// A GEDCOM converted to JSON and read back has to describe the same family. This is
// the one-way trip the family makes once, so it is worth knowing it arrives.
func TestConvertingAGedcomKeepsTheFamily(t *testing.T) {
	const ged = `0 HEAD
0 @I1@ INDI
1 NAME Louis J /LUCERO/
1 SEX M
1 BIRT
2 DATE 1918
1 FAMS @F1@
0 @I2@ INDI
1 NAME Raquel /HOLGUIN/
1 SEX F
1 FAMS @F1@
0 @I3@ INDI
1 NAME Robert Arturo /LUCERO/
1 BIRT
2 DATE 1958
1 FAMC @F1@
0 @F1@ FAM
1 HUSB @I1@
1 WIFE @I2@
1 CHIL @I3@
1 MARR
2 DATE 1950
0 TRLR
`
	parsed, err := gedcom.Parse(strings.NewReader(ged))
	if err != nil {
		t.Fatalf("gedcom.Parse: %v", err)
	}

	doc := FromGedcom(parsed, nil)
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(doc); err != nil {
		t.Fatalf("encode: %v", err)
	}
	rebuilt, err := Load(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("the file we just wrote does not load: %v", err)
	}

	if len(rebuilt.Individuals) != len(parsed.Individuals) {
		t.Errorf("%d people after the round trip, %d before",
			len(rebuilt.Individuals), len(parsed.Individuals))
	}

	// Readable ids, not xrefs: the point of the file is that somebody can find their
	// grandmother in it.
	if _, ok := rebuilt.Individuals["louis-j-lucero"]; !ok {
		ids := make([]string, 0, len(rebuilt.Individuals))
		for id := range rebuilt.Individuals {
			ids = append(ids, id)
		}
		t.Errorf("no readable id for Louis; got %v", ids)
	}

	// And the shouting is gone.
	for id, ind := range rebuilt.Individuals {
		if strings.ToUpper(ind.Surname) == ind.Surname && len(ind.Surname) > 1 {
			t.Errorf("%s still has a surname in capitals: %q", id, ind.Surname)
		}
	}

	// The relationships came through, not just the names.
	if got := rebuilt.Children("louis-j-lucero"); len(got) != 1 {
		t.Errorf("Louis has %v children after the round trip, want one", got)
	}
	if fam := rebuilt.ParentFamily("robert-arturo-lucero"); fam == nil || fam.MarriageYear != 1950 {
		t.Errorf("the marriage year did not survive: %+v", fam)
	}
}

func TestNormaliseNameLeavesWhatItShould(t *testing.T) {
	for in, want := range map[string]string{
		"LUCERO":                   "Lucero",
		"Nash Ignacio LUCERO":      "Nash Ignacio Lucero",
		"Emma Ermelinda M. LUCERO": "Emma Ermelinda M. Lucero",
		"Christopher Frank OERGEL": "Christopher Frank Oergel",
		"O'BRIEN":                  "O'Brien",
		"MARY-JANE":                "Mary-Jane",
		// Already written by a person, so left exactly alone.
		"de la Cruz": "de la Cruz",
		"McDonald":   "McDonald",
		"Lori Ann":   "Lori Ann",
		"J":          "J",
		"M.":         "M.",
	} {
		if got := NormaliseName(in); got != want {
			t.Errorf("NormaliseName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The file should hold what the site reads and nothing else. An Ancestry export has
// thousands of people in it reached through fifth cousins and their in-laws, and a
// file nobody can read is a file nobody will correct -- which was the whole reason
// for moving off the GEDCOM.
func TestOnlyTheFamilyTheSiteActuallyReads(t *testing.T) {
	// A root, three generations of ancestors, a sibling, and a stranger reached only
	// through somebody's in-laws.
	const ged = `0 HEAD
0 @ROOT@ INDI
1 NAME Robert /Lucero/
1 BIRT
2 DATE 1958
1 FAMC @F1@
0 @SIB@ INDI
1 NAME Frank /Lucero/
1 FAMC @F1@
0 @DAD@ INDI
1 NAME Louis /Lucero/
1 FAMS @F1@
1 FAMC @F2@
0 @MUM@ INDI
1 NAME Raquel /Holguin/
1 FAMS @F1@
0 @GRAN@ INDI
1 NAME Ignacio /Lucero/
1 FAMS @F2@
0 @STRANGER@ INDI
1 NAME Someone /Unrelated/
1 FAMS @F9@
0 @ALSOSTRANGER@ INDI
1 NAME Another /Stranger/
1 FAMS @F9@
0 @F1@ FAM
1 HUSB @DAD@
1 WIFE @MUM@
1 CHIL @ROOT@
1 CHIL @SIB@
0 @F2@ FAM
1 HUSB @GRAN@
1 CHIL @DAD@
0 @F9@ FAM
1 HUSB @STRANGER@
1 WIFE @ALSOSTRANGER@
0 TRLR
`
	parsed, err := gedcom.Parse(strings.NewReader(ged))
	if err != nil {
		t.Fatalf("gedcom.Parse: %v", err)
	}

	root, err := parsed.FindByName("Robert /Lucero/")
	if err != nil {
		t.Fatalf("FindByName: %v", err)
	}
	keep := Window(parsed, []string{root}, 2, 1, true, nil)
	doc := FromGedcom(parsed, keep)

	got := map[string]bool{}
	for _, p := range doc.People {
		got[p.ID] = true
	}
	for _, want := range []string{"robert-lucero", "frank-lucero", "louis-lucero",
		"raquel-holguin", "ignacio-lucero"} {
		if !got[want] {
			t.Errorf("%s should be in the file and is not", want)
		}
	}
	for _, unwanted := range []string{"someone-unrelated", "another-stranger"} {
		if got[unwanted] {
			t.Errorf("%s is somebody else's family and should not be in the file", unwanted)
		}
	}

	// And it must still build a working tree: a link pointing at somebody who was
	// left out would be worse than including them.
	f, err := doc.Build()
	if err != nil {
		t.Fatalf("the window does not build: %v", err)
	}
	if sibs := f.Siblings("robert-lucero"); len(sibs) != 1 || sibs[0] != "frank-lucero" {
		t.Errorf("Robert's siblings came out as %v", sibs)
	}
	if window := f.Ancestors([]string{"robert-lucero"}, 2); len(window) != 4 {
		t.Errorf("the walk found %v", window)
	}
}
