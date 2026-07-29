package gedcom

import (
	"os"
	"strings"
	"testing"
)

func loadSample(t *testing.T) *File {
	t.Helper()
	f, err := os.Open("testdata/sample.ged")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	parsed, err := Parse(f)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return parsed
}

func TestParseIndividuals(t *testing.T) {
	f := loadSample(t)

	if got := len(f.Individuals); got != 6 {
		t.Errorf("individuals = %d, want 6", got)
	}

	chris := f.Individuals["@I1@"]
	if chris == nil {
		t.Fatal("@I1@ missing")
	}
	if chris.Given != "Thomas James" {
		t.Errorf("Given = %q", chris.Given)
	}
	if chris.Surname != "Hale" {
		t.Errorf("Surname = %q", chris.Surname)
	}
	if chris.Sex != "M" {
		t.Errorf("Sex = %q", chris.Sex)
	}
	if chris.BirthYear != 1985 {
		t.Errorf("BirthYear = %d, want 1985", chris.BirthYear)
	}
	if chris.Name() != "Thomas James /Hale/" {
		t.Errorf("Name() = %q", chris.Name())
	}
}

func TestParseHandlesApproximateAndBareDates(t *testing.T) {
	f := loadSample(t)

	if got := f.Individuals["@I2@"].BirthYear; got != 1950 {
		t.Errorf("ABT 1950 -> %d, want 1950", got)
	}
	if got := f.Individuals["@I4@"].BirthYear; got != 1922 {
		t.Errorf("bare 1922 -> %d, want 1922", got)
	}
	if got := f.Individuals["@I4@"].DeathYear; got != 2011 {
		t.Errorf("death year = %d, want 2011", got)
	}
	// A birth event must never leak into the death year or vice versa.
	if got := f.Individuals["@I2@"].DeathYear; got != 0 {
		t.Errorf("DeathYear = %d, want 0 for a living person", got)
	}
}

func TestParseFamilies(t *testing.T) {
	f := loadSample(t)

	fam := f.Families["@F2@"]
	if fam == nil {
		t.Fatal("@F2@ missing")
	}
	if fam.HusbandID != "@I4@" || fam.WifeID != "@I5@" {
		t.Errorf("spouses = %q/%q", fam.HusbandID, fam.WifeID)
	}
	if len(fam.ChildIDs) != 1 || fam.ChildIDs[0] != "@I2@" {
		t.Errorf("children = %v", fam.ChildIDs)
	}
}

func TestParseLineForms(t *testing.T) {
	cases := []struct {
		in               string
		level            int
		xref, tag, value string
		ok               bool
	}{
		{"0 @I1@ INDI", 0, "@I1@", "INDI", "", true},
		{"1 NAME John /Doe/", 1, "", "NAME", "John /Doe/", true},
		{"2 DATE 12 MAR 1985", 2, "", "DATE", "12 MAR 1985", true},
		{"1 BIRT", 1, "", "BIRT", "", true},
		{"", 0, "", "", "", false},
		{"garbage", 0, "", "", "", false},
	}
	for _, c := range cases {
		level, xref, tag, value, ok := parseLine(c.in)
		if ok != c.ok || level != c.level || xref != c.xref || tag != c.tag || value != c.value {
			t.Errorf("parseLine(%q) = (%d,%q,%q,%q,%v), want (%d,%q,%q,%q,%v)",
				c.in, level, xref, tag, value, ok, c.level, c.xref, c.tag, c.value, c.ok)
		}
	}
}

func TestFindByNameRequiresExactlyOneMatch(t *testing.T) {
	f := loadSample(t)

	id, err := f.FindByName("Peter Samuel /Hale/")
	if err != nil {
		t.Fatalf("FindByName: %v", err)
	}
	if id != "@I4@" {
		t.Errorf("id = %q, want @I4@", id)
	}

	if _, err := f.FindByName("Nobody /Here/"); err == nil {
		t.Error("expected an error for a name with no match")
	}
}

func TestExtractYear(t *testing.T) {
	cases := map[string]int{
		"12 MAR 1985":       1985,
		"ABT 1900":          1900,
		"BET 1910 AND 1912": 1912,
		"":                  0,
		"no digits here":    0,
		"12":                0,
		"3 JUN 2011":        2011,
	}
	for in, want := range cases {
		if got := extractYear(in); got != want {
			t.Errorf("extractYear(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestSplitName(t *testing.T) {
	cases := []struct{ in, given, surname string }{
		{"Thomas James /Hale/", "Thomas James", "Hale"},
		{"Margaret Irene /Ward/", "Margaret Irene", "Ward"},
		{"Cher", "Cher", ""},
		{"/OnlySurname/", "", "OnlySurname"},
	}
	for _, c := range cases {
		given, surname := splitName(c.in)
		if given != c.given || surname != c.surname {
			t.Errorf("splitName(%q) = (%q,%q), want (%q,%q)", c.in, given, surname, c.given, c.surname)
		}
	}
}

// Marriage dates and divorces decide which surname a woman was last known by,
// so the parser has to keep them.
func TestParseMarriageAndDivorce(t *testing.T) {
	f, err := Parse(strings.NewReader(`0 @I1@ INDI
1 NAME Alice Marguerite /Crowe/
1 SEX F
1 FAMS @F1@
1 FAMS @F2@
0 @I2@ INDI
1 NAME Pierce Tobias /Radley/
0 @I3@ INDI
1 NAME George /Marsh/
0 @F1@ FAM
1 HUSB @I2@
1 WIFE @I1@
1 MARR
2 DATE 12 JUN 1888
1 DIV
2 DATE 1910
0 @F2@ FAM
1 HUSB @I3@
1 WIFE @I1@
1 MARR
2 DATE 1918
0 TRLR
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	first, second := f.Families["@F1@"], f.Families["@F2@"]
	if first.MarriageYear != 1888 {
		t.Errorf("first marriage year = %d, want 1888", first.MarriageYear)
	}
	if !first.Divorced {
		t.Error("first marriage should be recorded as divorced")
	}
	if second.MarriageYear != 1918 {
		t.Errorf("second marriage year = %d, want 1918", second.MarriageYear)
	}
	if second.Divorced {
		t.Error("second marriage was not dissolved")
	}
	// A divorce date must not overwrite the marriage date.
	if first.MarriageYear == 1910 {
		t.Error("divorce date leaked into the marriage year")
	}
}
