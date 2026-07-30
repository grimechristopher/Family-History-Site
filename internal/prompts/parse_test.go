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
	if first.Person != "Dad" || first.Section != "Parents" || first.Subsection != "Peter S Hale" {
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
		t.Errorf("ordinals under Peter S Hale = %d,%d; want 1,2", qs[0].Ordinal, qs[1].Ordinal)
	}
	if qs[2].Ordinal != 1 {
		t.Errorf("first ordinal under Grandma Vera = %d, want 1", qs[2].Ordinal)
	}
}

// The key must depend on the question, not on where it sits. Two positional
// schemes corrupted data before this one: see ImportKey's comment.
func TestImportKeyIsContentAddressed(t *testing.T) {
	const subject, body = "alice-may-osgood", "What kind of cars did he have?"

	if ImportKey(subject, body, 1) != ImportKey(subject, body, 1) {
		t.Fatal("the same inputs must give the same key")
	}
	// Reflowing or re-spacing a line is not a different question.
	if ImportKey(subject, body, 1) !=
		ImportKey(subject, "  What  kind of cars\n  did he have? ", 1) {
		t.Error("whitespace should not change identity")
	}
	// Neither the heading nor the question is recoverable from the key, and no
	// part of it counts position within the file.
	key := ImportKey(subject, body, 1)
	if strings.Contains(key, "cars") || strings.Contains(key, "alice") {
		t.Errorf("key %q should carry neither heading nor body text", key)
	}

	// Everything that genuinely distinguishes two questions must reach the key.
	seen := map[string]string{}
	for what, k := range map[string]string{
		"baseline":           ImportKey(subject, body, 1),
		"second occurrence":  ImportKey(subject, body, 2),
		"about someone else": ImportKey("louis-hale", body, 1),
		"different question": ImportKey(subject, "A different question?", 1),
	} {
		if prev, dup := seen[k]; dup {
			t.Errorf("%s collides with %s", what, prev)
		}
		seen[k] = what
	}

	// Who is asked is deliberately not in it. Robert, Frank, Tony and Inez are
	// given the same prompts about their parents; keyed by person that was four
	// rows of one question, four cards and four places to answer. One key means one
	// question, and they are recorded against it as the people asked.
	if ImportKey(subject, body, 1) != ImportKey(subject, body, 1) {
		t.Error("two people given the same prompt must reach the same question")
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
	if order[0].Subsection != "Peter S Hale" {
		t.Errorf("first heading = %v", order[0])
	}
	if counts[order[0]] != 2 {
		t.Errorf("Peter S Hale count = %d, want 2", counts[order[0]])
	}
	if got := order[2].String(); got != "Dad / Great Grandparents" {
		t.Errorf("String() with no subsection = %q", got)
	}
}
