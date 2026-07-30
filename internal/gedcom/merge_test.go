package gedcom

import (
	"strings"
	"testing"
)

// The rule has to fold a genuine duplicate and leave a father and son alone.
// Both are in the real tree: two records for one Rogelio, and a Burton LeRoy
// Stevens who named his son after himself.
func TestMergeDuplicatesKnowsTheDifference(t *testing.T) {
	const doc = `0 HEAD
0 @I1@ INDI
1 NAME Francisco /Holguin/
1 SEX M
1 FAMS @F1@
0 @I2@ INDI
1 NAME Maria /Barraza/
1 SEX F
1 FAMS @F1@
0 @I3@ INDI
1 NAME Rogelio /Holguin Barraza/
1 BIRT
2 DATE 1919
1 FAMC @F1@
0 @I4@ INDI
1 NAME Rogelio /Holguin Barraza/
1 SEX M
1 BIRT
2 DATE 1919
1 FAMC @F1@
0 @I5@ INDI
1 NAME Burton LeRoy /Stevens/
1 BIRT
2 DATE 1890
1 FAMC @F1@
1 FAMS @F2@
0 @I6@ INDI
1 NAME Burton LeRoy /Stevens/
1 BIRT
2 DATE 1919
1 FAMC @F2@
0 @F1@ FAM
1 HUSB @I1@
1 WIFE @I2@
1 CHIL @I3@
1 CHIL @I4@
1 CHIL @I5@
0 @F2@ FAM
1 HUSB @I5@
1 CHIL @I6@
0 TRLR
`
	f, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	merges := f.MergeDuplicates()

	if len(merges) != 1 {
		t.Fatalf("merged %d pairs, want just the two Rogelios: %+v", len(merges), merges)
	}
	if merges[0].Removed != "@I4@" || merges[0].Kept != "@I3@" {
		t.Errorf("merged %s into %s, want @I4@ into @I3@", merges[0].Removed, merges[0].Kept)
	}
	if _, still := f.Individuals["@I4@"]; still {
		t.Error("the duplicate record is still there")
	}

	// The record that stayed takes what the other one knew.
	if f.Individuals["@I3@"].Sex != "M" {
		t.Error("the surviving record should have taken the sex the other one recorded")
	}

	// The father and son are untouched. Merging them would make a man his own
	// father, which is worse than a duplicate box on a chart.
	for _, id := range []string{"@I5@", "@I6@"} {
		if _, ok := f.Individuals[id]; !ok {
			t.Errorf("%s was merged away; a son named after his father is not a duplicate", id)
		}
	}

	// And nothing still points at the record that went.
	kids := f.Families["@F1@"].ChildIDs
	for _, id := range kids {
		if id == "@I4@" {
			t.Error("a family still lists the merged-away record as a child")
		}
	}
	// Three children were listed and two of them were the same person, so two
	// remain -- and the duplicate is gone from the list rather than left as an
	// entry pointing at nothing.
	if len(kids) != 2 {
		t.Errorf("family lists %d children, want 2 after the fold: %v", len(kids), kids)
	}
	if got := len(f.Children("@I1@")); got != 2 {
		t.Errorf("Francisco has %d children after merging, want 2", got)
	}
}
