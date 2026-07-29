package prompts

import (
	"os"
	"strings"
	"testing"
)

func load(t *testing.T) []Question {
	t.Helper()
	f, err := os.Open("testdata/sample.md")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	qs, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return qs
}

func TestParseAssignsHierarchy(t *testing.T) {
	qs := load(t)

	if len(qs) != 6 {
		t.Fatalf("got %d questions, want 6: %+v", len(qs), qs)
	}

	first := qs[0]
	if first.Person != "Dad" || first.Section != "Parents" || first.Subsection != "James R Hale" {
		t.Errorf("hierarchy = %q/%q/%q", first.Person, first.Section, first.Subsection)
	}
	if first.Body != "What do you remember about your Aunts and Uncles?" {
		t.Errorf("Body = %q", first.Body)
	}
	if first.IsProposed {
		t.Error("first question should not be proposed")
	}
}

func TestParseOrdinalResetsPerHeading(t *testing.T) {
	qs := load(t)

	if qs[0].Ordinal != 1 || qs[1].Ordinal != 2 {
		t.Errorf("ordinals under James R Hale = %d,%d; want 1,2", qs[0].Ordinal, qs[1].Ordinal)
	}
	if qs[2].Ordinal != 1 {
		t.Errorf("first ordinal under Grandma Vera = %d, want 1", qs[2].Ordinal)
	}
}

// The key must not contain the heading text: renaming a heading has to leave a
// question's identity, and therefore its answers, alone.
func TestImportKeyIgnoresHeadingText(t *testing.T) {
	before := ImportKey("Dad", "alice-may-fletcher", "", 1)
	after := ImportKey("Dad", "alice-may-fletcher", "", 1)
	if before != after {
		t.Fatalf("same inputs gave %q then %q", before, after)
	}
	if strings.Contains(before, "Parents") || strings.Contains(before, "Alice May") {
		t.Errorf("key %q still carries heading text", before)
	}

	// Different subjects, topics, people and positions stay distinct.
	keys := map[string]bool{}
	for _, k := range []string{
		ImportKey("Dad", "a", "", 1),
		ImportKey("Dad", "a", "", 2),
		ImportKey("Dad", "a", "Childhood", 1),
		ImportKey("Dad", "b", "", 1),
		ImportKey("Mom", "a", "", 1),
	} {
		if keys[k] {
			t.Errorf("duplicate key %q", k)
		}
		keys[k] = true
	}
}

func TestParseMarksProposed(t *testing.T) {
	qs := load(t)

	vera := qs[2]
	if vera.Subsection != "Grandma Vera Hale" {
		t.Fatalf("expected Grandma Vera question, got %q", vera.Subsection)
	}
	if !vera.IsProposed {
		t.Error("question under #### Proposed should be marked proposed")
	}
}

func TestParseAllowsQuestionsWithNoSubsection(t *testing.T) {
	qs := load(t)

	var found *Question
	for i := range qs {
		if qs[i].Section == "Great Grandparents" {
			found = &qs[i]
			break
		}
	}
	if found == nil {
		t.Fatal("no question found directly under ## Great Grandparents")
	}
	if found.Subsection != "" {
		t.Errorf("Subsection = %q, want empty", found.Subsection)
	}
}

func TestParseResetsProposedOnNewHeading(t *testing.T) {
	qs := load(t)

	for _, q := range qs {
		if q.Section == "About You" && q.IsProposed {
			t.Errorf("proposed flag leaked into %q/%q", q.Section, q.Subsection)
		}
	}
}

func TestParseSkipsEmptySectionsAndSeparators(t *testing.T) {
	qs := load(t)

	for _, q := range qs {
		if q.Person == "Stephanie" {
			t.Errorf("Stephanie section is empty and must yield no questions: %+v", q)
		}
		if q.Body == "---" {
			t.Error("separator parsed as a question")
		}
	}
}

func TestParseSwitchesPerson(t *testing.T) {
	qs := load(t)

	last := qs[len(qs)-1]
	if last.Person != "Mom" || last.Subsection != "Edward R Brennan" {
		t.Errorf("last question = %q/%q", last.Person, last.Subsection)
	}
	if last.Ordinal != 1 {
		t.Errorf("Ordinal = %d, want 1 after person switch", last.Ordinal)
	}
}

func TestHeadingsCountsAndOrder(t *testing.T) {
	qs := load(t)

	order, counts := Headings(qs)

	if len(order) != 5 {
		t.Fatalf("distinct headings = %d, want 5: %v", len(order), order)
	}
	if order[0].Subsection != "James R Hale" {
		t.Errorf("first heading = %v", order[0])
	}
	if counts[order[0]] != 2 {
		t.Errorf("James R Hale count = %d, want 2", counts[order[0]])
	}
	if got := order[2].String(); got != "Dad / Great Grandparents" {
		t.Errorf("String() with no subsection = %q", got)
	}
}
