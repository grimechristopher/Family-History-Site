package subjects

import (
	"strings"
	"testing"

	"github.com/grimechristopher/family-history-site/internal/prompts"
)

// testTree builds a small tree by hand so matching can be tested without a
// GEDCOM. It mirrors the shape of the real data: a couple at generation 1 plus a
// stepmother, and four grandparents at generation 2 including two Wards.
func testTree() *Tree {
	people := map[string]*Person{
		"@DAD@": {GedcomID: "@DAD@", Given: "Peter John", Surname: "Hale", Sex: "M", Generation: 0},
		"@MOM@": {GedcomID: "@MOM@", Given: "Ruth Ann", Surname: "Brennan", Sex: "F", Generation: 0},

		"@JRG@":  {GedcomID: "@JRG@", Given: "Peter Samuel", Surname: "Hale", Sex: "M", Generation: 1},
		"@FIS@":  {GedcomID: "@FIS@", Given: "Margaret Irene", Surname: "Ward", Sex: "F", Generation: 1, AliasSurnames: []string{"Hale"}},
		"@LELA@": {GedcomID: "@LELA@", Given: "Vera", Surname: "Lindqvist", Sex: "F", Generation: 1, AliasSurnames: []string{"Hale"}},

		"@CVS@": {GedcomID: "@CVS@", Given: "Clarence Vernon", Surname: "Ward", Sex: "M", Generation: 2},
		"@FLD@": {GedcomID: "@FLD@", Given: "Margaret Lucille", Surname: "Alderman", Sex: "F", Generation: 2, AliasSurnames: []string{"Ward"}},
		"@MAT@": {GedcomID: "@MAT@", Given: "Nora Angeline", Surname: "Radley", Sex: "F", Generation: 2, AliasSurnames: []string{"Brennan"}},
		"@SCA@": {GedcomID: "@SCA@", Given: "Sheldon Grant", Surname: "Brennan", Sex: "M", Generation: 2},
	}

	t := &Tree{People: people, generations: 3, byGen: map[int][]string{}}
	for id, p := range people {
		t.byGen[p.Generation] = append(t.byGen[p.Generation], id)
	}

	order := 0
	for gen := 0; gen <= 2; gen++ {
		for _, id := range t.byGen[gen] {
			order++
			p := people[id]
			t.Subjects = append(t.Subjects, Subject{
				Slug: slugify(p.FullName()), Kind: KindIndividual, DisplayName: p.FullName(),
				SortOrder: order, Generation: gen, MemberIDs: []string{id},
			})
		}
	}
	t.Subjects = append(t.Subjects, Subject{
		Slug: FurtherBackSlug, Kind: KindGroup, DisplayName: "Further Back", Generation: 4,
	})
	return t
}

func match(t *testing.T, tree *Tree, section, subsection string) (Match, []Ambiguity) {
	t.Helper()
	h := prompts.Heading{Person: "Dad", Section: section, Subsection: subsection}
	people := map[string]string{"Dad": "peter-john-hale", "Mom": "ruth-ann-brennan"}
	ms, ambs := tree.MatchHeadings([]prompts.Heading{h}, people, Overrides{})
	if len(ms) == 1 {
		return ms[0], ambs
	}
	return Match{}, ambs
}

// The regression that matters most: a stepmother sharing only a married surname
// must never be matched to the birth mother.
func TestPartialMatchIsRefused(t *testing.T) {
	tree := testTree()
	// Remove Vera so "Grandma Vera Hale" has only a partial candidate.
	var kept []Subject
	for _, s := range tree.Subjects {
		if s.Slug != "vera-lindqvist" {
			kept = append(kept, s)
		}
	}
	tree.Subjects = kept

	got, ambs := match(t, tree, "Parents", "Grandma Vera Hale")

	if got.Subject != "" {
		t.Fatalf("matched %q on a surname alone; partial matches must be refused", got.Subject)
	}
	if len(ambs) != 1 {
		t.Fatalf("ambiguities = %d, want 1", len(ambs))
	}
	if !strings.Contains(ambs[0].Reason, "every name word") {
		t.Errorf("reason should explain the coverage rule, got %q", ambs[0].Reason)
	}
	// The near miss is reported so the cause is obvious.
	if len(ambs[0].Candidates) == 0 {
		t.Error("expected the partial match to be listed as a rejected candidate")
	}
}

func TestFullCoverageMatchesStepmother(t *testing.T) {
	got, ambs := match(t, testTree(), "Parents", "Grandma Vera Hale")

	if len(ambs) != 0 {
		t.Fatalf("unexpected ambiguities: %v", ambs)
	}
	if got.Subject != "vera-lindqvist" {
		t.Errorf("Subject = %q, want vera-lindqvist", got.Subject)
	}
}

// A surname-only heading is fine when it identifies exactly one person.
func TestSurnameOnlyHeadingResolvesWhenUnique(t *testing.T) {
	got, ambs := match(t, testTree(), "Grandparents", "Grandpa Ward")

	if len(ambs) != 0 {
		t.Fatalf("unexpected ambiguities: %v", ambs)
	}
	if got.Subject != "clarence-virgil-ward" {
		t.Errorf("Subject = %q, want clarence-virgil-ward", got.Subject)
	}
}

// Sex separates two generation-2 people who both answer to "Ward".
func TestHonorificSexDisambiguates(t *testing.T) {
	tree := testTree()

	dad, _ := match(t, tree, "Grandparents", "Grandpa Ward")
	if dad.Subject != "clarence-virgil-ward" {
		t.Errorf("Grandpa Ward -> %q", dad.Subject)
	}

	// Margaret Lucille Alderman married a Ward, so she answers to "Ward" too.
	grandma, ambs := match(t, tree, "Grandparents", "Grandma Margaret Lucille Ward")
	if len(ambs) != 0 {
		t.Fatalf("unexpected ambiguities: %v", ambs)
	}
	if grandma.Subject != "margaret-lucille-alderman" {
		t.Errorf("Grandma Margaret Lucille Ward -> %q", grandma.Subject)
	}
}

// A woman recorded under her maiden name is still found by her married one.
func TestMarriedSurnameAliasMatches(t *testing.T) {
	got, ambs := match(t, testTree(), "Grandparents", "Grandma Mary Brennan")

	if len(ambs) != 0 {
		t.Fatalf("unexpected ambiguities: %v", ambs)
	}
	if got.Subject != "mary-angeline-radley" {
		t.Errorf("Subject = %q, want mary-angeline-radley", got.Subject)
	}
}

// Generation keeps Dad's mother apart from his grandmother, who share tokens.
func TestSectionGenerationDisambiguates(t *testing.T) {
	tree := testTree()

	parent, ambs := match(t, tree, "Parents", "Margaret Irene Hale")
	if len(ambs) != 0 {
		t.Fatalf("unexpected ambiguities: %v", ambs)
	}
	if parent.Subject != "margaret-irene-ward" {
		t.Errorf("Subject = %q, want margaret-irene-ward", parent.Subject)
	}

	// The same name under Grandparents must not reach a generation-1 person.
	_, ambs = match(t, tree, "Grandparents", "Margaret Irene Hale")
	if len(ambs) != 1 {
		t.Errorf("a generation-1 name under Grandparents should not resolve, got %v", ambs)
	}
}

func TestAboutYouBecomesATopicOnThePerson(t *testing.T) {
	got, ambs := match(t, testTree(), "About You", "Childhood")

	if len(ambs) != 0 {
		t.Fatalf("unexpected ambiguities: %v", ambs)
	}
	if got.Subject != "peter-john-hale" {
		t.Errorf("Subject = %q, want the person themselves", got.Subject)
	}
	if got.Topic != "Childhood" {
		t.Errorf("Topic = %q, want Childhood", got.Topic)
	}
}

func TestSectionWithNoSubsectionGoesToFurtherBack(t *testing.T) {
	got, ambs := match(t, testTree(), "Great Grandparents", "")

	if len(ambs) != 0 {
		t.Fatalf("unexpected ambiguities: %v", ambs)
	}
	if got.Subject != FurtherBackSlug {
		t.Errorf("Subject = %q, want %q", got.Subject, FurtherBackSlug)
	}
}

func TestOverrideWins(t *testing.T) {
	tree := testTree()
	h := prompts.Heading{Person: "Dad", Section: "Parents", Subsection: "Grandma Vera Hale"}
	ov := Overrides{
		Subjects: map[string]string{h.String(): "margaret-irene-ward"},
		Topics:   map[string]string{h.String(): "Stepmother"},
	}

	ms, ambs := tree.MatchHeadings([]prompts.Heading{h},
		map[string]string{"Dad": "peter-john-hale"}, ov)

	if len(ambs) != 0 || len(ms) != 1 {
		t.Fatalf("matches=%v ambiguities=%v", ms, ambs)
	}
	if ms[0].Subject != "margaret-irene-ward" || ms[0].Topic != "Stepmother" {
		t.Errorf("override not applied: %+v", ms[0])
	}
	if ms[0].Why != "override" {
		t.Errorf("Why = %q", ms[0].Why)
	}
}

func TestOverrideNamingUnknownSubjectIsRejected(t *testing.T) {
	tree := testTree()
	h := prompts.Heading{Person: "Dad", Section: "Parents", Subsection: "Grandma Vera Hale"}
	ov := Overrides{Subjects: map[string]string{h.String(): "does-not-exist"}}

	ms, ambs := tree.MatchHeadings([]prompts.Heading{h},
		map[string]string{"Dad": "peter-john-hale"}, ov)

	if len(ms) != 0 {
		t.Error("an override naming an unknown subject must not produce a match")
	}
	if len(ambs) != 1 || !strings.Contains(ambs[0].Reason, "does-not-exist") {
		t.Errorf("ambiguities = %v", ambs)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Peter John Hale":                 "peter-john-hale",
		"Anna B. Broome":                      "anna-b-hudry",
		"Edward Agusta Brennan and Ella Vance": "edwin-agusta-brennan-and-ella-cooper",
		"  spaced  out  ":                    "spaced-out",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
