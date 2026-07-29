package subjects

import (
	"os"
	"testing"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
	"github.com/grimechristopher/family-history-site/internal/prompts"
)

// This is the review table. It resolves every heading in the real prompts file
// against the real tree and prints what matched and why, so the mapping can be
// checked by eye instead of authored by hand.
//
//	REAL_GEDCOM="/home/chris/Documents/Github/genealogy-export/Brennan-Hale Tree.ged" \
//	REAL_PROMPTS="/path/to/notes/General Notebook/General Notebook/Areas/Ancestry Book/Prompts 3.md" \
//	go test ./internal/subjects/ -run RealMatch -v
func TestRealMatchHeadings(t *testing.T) {
	gedPath := os.Getenv("REAL_GEDCOM")
	promptsPath := os.Getenv("REAL_PROMPTS")
	if gedPath == "" || promptsPath == "" {
		t.Skip("REAL_GEDCOM and REAL_PROMPTS not set")
	}

	gf, err := os.Open(gedPath)
	if err != nil {
		t.Fatalf("open gedcom: %v", err)
	}
	defer gf.Close()
	parsed, err := gedcom.Parse(gf)
	if err != nil {
		t.Fatalf("parse gedcom: %v", err)
	}

	tree, err := Derive(parsed, DefaultOptions())
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	t.Logf("people=%d subjects=%d", len(tree.People), len(tree.Subjects))
	for _, s := range tree.Subjects {
		t.Logf("  gen%d %-10s %-46s %s", s.Generation, s.Kind, s.DisplayName, s.Slug)
	}

	pf, err := os.Open(promptsPath)
	if err != nil {
		t.Fatalf("open prompts: %v", err)
	}
	defer pf.Close()
	qs, err := prompts.Parse(pf)
	if err != nil {
		t.Fatalf("parse prompts: %v", err)
	}
	headings, counts := prompts.Headings(qs)

	personSubjects, err := tree.PersonSubjects(map[string]string{
		"Dad": "Peter John /Hale/",
		"Mom": "Ruth Ann /Brennan/",
	})
	if err != nil {
		t.Fatalf("PersonSubjects: %v", err)
	}

	matches, ambiguities := tree.MatchHeadings(headings, personSubjects, Overrides{})

	t.Logf("")
	t.Logf("=== MATCHED %d of %d headings ===", len(matches), len(headings))
	for _, m := range matches {
		topic := ""
		if m.Topic != "" {
			topic = " topic=" + m.Topic
		}
		t.Logf("  %-46s -> %-34s [%s]%s", m.Heading.String(), m.Subject, m.Why, topic)
	}

	if len(ambiguities) > 0 {
		t.Logf("")
		t.Logf("=== NEEDS A DECISION: %d ===", len(ambiguities))
		for _, a := range ambiguities {
			t.Logf("  %d question(s)  %s", counts[a.Heading], a.Error())
		}
	}

	// Every question must end up somewhere, so unresolved headings are a
	// failure rather than a warning.
	if len(ambiguities) > 0 {
		t.Errorf("%d heading(s) unresolved; each needs an Overrides entry or an ExtraNames addition", len(ambiguities))
	}
}
