package gedcom

import "testing"

func TestAncestorsWalksUpGenerations(t *testing.T) {
	f := loadSample(t)

	got := f.Ancestors([]string{"@I1@"}, 2)

	want := map[string]int{
		"@I1@": 0, // Chris
		"@I2@": 1, // Peter John Hale
		"@I3@": 1, // Ruth Ann Brennan
		"@I4@": 2, // Peter Samuel Hale
		"@I5@": 2, // Margaret Irene Ward
	}
	if len(got) != len(want) {
		t.Fatalf("found %d individuals, want %d: %v", len(got), len(want), got)
	}
	for id, gen := range want {
		if got[id] != gen {
			t.Errorf("%s generation = %d, want %d", id, got[id], gen)
		}
	}
}

func TestAncestorsStopsAtTheGenerationLimit(t *testing.T) {
	f := loadSample(t)

	got := f.Ancestors([]string{"@I1@"}, 1)

	if _, present := got["@I4@"]; present {
		t.Error("@I4@ is two generations up and must not appear with gens=1")
	}
	if len(got) != 3 {
		t.Errorf("found %d, want 3 (self plus two parents): %v", len(got), got)
	}
}

func TestAncestorsZeroGenerationsReturnsOnlyRoots(t *testing.T) {
	f := loadSample(t)

	got := f.Ancestors([]string{"@I2@", "@I3@"}, 0)

	if len(got) != 2 {
		t.Fatalf("got %v, want only the two roots", got)
	}
	if got["@I2@"] != 0 || got["@I3@"] != 0 {
		t.Errorf("roots should be generation 0: %v", got)
	}
}

func TestAncestorsIgnoresUnknownRoots(t *testing.T) {
	f := loadSample(t)

	got := f.Ancestors([]string{"@NOPE@"}, 3)

	if len(got) != 0 {
		t.Errorf("got %v, want empty", got)
	}
}

func TestParentFamily(t *testing.T) {
	f := loadSample(t)

	fam := f.ParentFamily("@I2@")
	if fam == nil {
		t.Fatal("expected @I2@ to have a parent family")
	}
	if fam.ID != "@F2@" {
		t.Errorf("family = %q, want @F2@", fam.ID)
	}

	if f.ParentFamily("@I4@") != nil {
		t.Error("expected no parent family for @I4@")
	}
	if f.ParentFamily("@NOPE@") != nil {
		t.Error("expected nil for an unknown individual")
	}
}
