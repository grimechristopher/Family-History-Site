package subjects

import (
	"os"
	"strings"
	"testing"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
)

// fixtureOptions is the shape of the real configuration -- two roots, three
// generations, one person who no ancestor walk reaches -- over the invented family
// in testdata. The real names live in the operator's environment, never here.
func fixtureOptions() Options {
	return Options{
		RootNames:   []string{"Peter John /Hale/", "Ruth Ann /Brennan/"},
		Generations: 3,
		ExtraNames:  []string{"Vera /Lindqvist/"},
	}
}

// A woman who married more than once is shown under her last marriage, which is
// the name she was known by — even when an earlier husband is the one who puts
// her in this family.
func TestMarriedSurnameUsesTheLastMarriage(t *testing.T) {
	path := os.Getenv("REAL_GEDCOM")
	if path == "" {
		t.Skip("REAL_GEDCOM not set")
	}
	fh, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer fh.Close()
	f, err := gedcom.Parse(fh)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	tree, err := Derive(f, fixtureOptions())
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	byRecordedName := map[string]*Person{}
	for _, p := range tree.People {
		byRecordedName[p.FullName()] = p
	}

	cases := map[string]string{
		// Divorced Pierce Radley, married George Marsh in 1918. Radley is the
		// husband who connects her to this tree; Marsh is who she became.
		"Alice Marguerite Crowe": "Alice Marguerite (Crowe) Marsh",
		// Divorced Arlen Pruitt in the seventies, married James Hale in 1990.
		"Ruth Ann Brennan": "Ruth Ann (Brennan) Hale",
		// Remarried a Whitby in 1969, so that is her name, even though the family
		// grew up calling her Grandma Fletcher.
		"Alma Jean Nash": "Alma Jean (Nash) Whitby",
		// Also married a Peavler, but neither marriage is dated, so the order is
		// unknowable from the file. Falls back to the marriage into this family.
		"Vera Lindqvist": "Vera (Lindqvist) Hale",
		// Single marriages, which must not regress.
		"Margaret Irene Ward":       "Margaret Irene (Ward) Hale",
		"Nora Angeline Osgood":      "Nora Angeline (Osgood) Brennan",
		"Alice May Fletcher":        "Alice May (Fletcher) Brennan",
		"Margaret Lucille Alderman": "Margaret Lucille (Alderman) Ward",
		"Anna Lund":              "Anna (Lund) Alderman",
		"Margaret Mary Fletcher":    "Margaret Mary (Fletcher) Hale",
	}
	for recorded, want := range cases {
		p := byRecordedName[recorded]
		if p == nil {
			t.Errorf("%s is not in the imported tree", recorded)
			continue
		}
		if got := p.DisplayName(); got != want {
			t.Errorf("%s -> %q, want %q", recorded, got, want)
		}
	}

	// Men keep their own surname; no parentheses anywhere.
	for _, p := range tree.People {
		if p.Sex == "M" && strings.Contains(p.DisplayName(), "(") {
			t.Errorf("%s should not carry a married name", p.DisplayName())
		}
	}
}
