package subjects

import (
	"fmt"
	"sort"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/prompts"
)

// Match is the resolved location for one markdown heading.
type Match struct {
	Heading prompts.Heading
	Subject string // subject slug
	Topic   string // set when the heading is a topic about the person themselves
	// Why records the signals used, for the review table.
	Why string
}

// Ambiguity is a heading that could not be resolved to exactly one subject.
type Ambiguity struct {
	Heading    prompts.Heading
	Candidates []string // display names, best first
	Reason     string
}

func (a Ambiguity) Error() string {
	if len(a.Candidates) == 0 {
		return fmt.Sprintf("%s: %s", a.Heading, a.Reason)
	}
	return fmt.Sprintf("%s: %s (candidates: %s)",
		a.Heading, a.Reason, strings.Join(a.Candidates, ", "))
}

// sectionGeneration maps a markdown section to the generation above the person
// being asked. This is the strongest disambiguating signal available, because
// "Grandma Margaret Lucille Ward" and "Margaret Irene Hale" share tokens but
// sit two generations apart.
var sectionGeneration = map[string]int{
	"Parents":            1,
	"Grandparents":       2,
	"Great Grandparents": 3,
}

// honorificSex maps a leading honorific to the sex it implies, which separates
// "Grandpa Ward" from "Grandma Ward" when tokens alone cannot.
var honorificSex = map[string]string{
	"grandpa":     "M",
	"grandfather": "M",
	"papa":        "M",
	"grandma":     "F",
	"grandmother": "F",
	"nana":        "F",
}

// noiseTokens carry no identifying information.
var noiseTokens = map[string]bool{
	"grandpa": true, "grandma": true, "grandfather": true, "grandmother": true,
	"papa": true, "nana": true, "great": true, "aunt": true, "uncle": true,
	"step": true, "the": true, "and": true, "family": true, "side": true,
}

// Overrides pins headings the automatic matcher cannot resolve. It starts empty
// and only grows when the matcher reports an ambiguity.
type Overrides struct {
	// Subjects maps "Person / Section / Subsection" to a subject slug.
	Subjects map[string]string `yaml:"subjects"`
	// Topics maps the same key to a topic name, for headings about the person
	// themselves rather than an ancestor.
	Topics map[string]string `yaml:"topics"`
}

// MatchHeadings resolves every heading onto a subject. It returns the matches it
// is confident about and every ambiguity it is not, so nothing is guessed
// silently: a wrong match would file questions under the wrong ancestor and only
// be noticed after they had been answered.
func (t *Tree) MatchHeadings(headings []prompts.Heading, personSubjects map[string]string, ov Overrides) ([]Match, []Ambiguity) {
	var matches []Match
	var ambiguities []Ambiguity

	bySlug := map[string]*Subject{}
	for i := range t.Subjects {
		bySlug[t.Subjects[i].Slug] = &t.Subjects[i]
	}

	for _, h := range headings {
		key := h.String()

		if slug, ok := ov.Subjects[key]; ok {
			if _, exists := bySlug[slug]; !exists {
				ambiguities = append(ambiguities, Ambiguity{
					Heading: h,
					Reason:  fmt.Sprintf("override names unknown subject %q", slug),
				})
				continue
			}
			matches = append(matches, Match{
				Heading: h, Subject: slug, Topic: ov.Topics[key], Why: "override",
			})
			continue
		}

		selfSlug, hasSelf := personSubjects[h.Person]

		// "About You" and anything else with no ancestor generation is about the
		// person being asked; the subsection becomes a topic.
		gen, isAncestorSection := sectionGeneration[h.Section]
		if !isAncestorSection {
			if !hasSelf {
				ambiguities = append(ambiguities, Ambiguity{
					Heading: h,
					Reason:  fmt.Sprintf("no subject registered for person %q", h.Person),
				})
				continue
			}
			matches = append(matches, Match{
				Heading: h, Subject: selfSlug, Topic: h.Subsection, Why: "about the person themselves",
			})
			continue
		}

		// A section with no subsection covers the whole generation loosely, so
		// it lands in the further-back bucket rather than on an individual.
		if h.Subsection == "" {
			matches = append(matches, Match{
				Heading: h, Subject: FurtherBackSlug, Why: "section-level questions, no named ancestor",
			})
			continue
		}

		m, amb := t.matchAncestorHeading(h, gen)
		if amb != nil {
			ambiguities = append(ambiguities, *amb)
			continue
		}
		matches = append(matches, *m)
	}

	return matches, ambiguities
}

type scored struct {
	subject *Subject
	score   int
	tokens  int
}

func (t *Tree) matchAncestorHeading(h prompts.Heading, gen int) (*Match, *Ambiguity) {
	headingTokens := identifyingTokens(h.Subsection)
	wantSex := impliedSex(h.Subsection)

	if len(headingTokens) == 0 {
		return nil, &Ambiguity{
			Heading: h,
			Reason:  "heading contains no identifying name words",
		}
	}

	// Full coverage is required: every identifying word in the heading must be
	// accounted for by the person. A partial match is not a weak signal, it is
	// a wrong one — "Grandma Vera Hale" overlaps Margaret Irene Ward on the
	// married surname "Hale" alone, and accepting that would file a
	// stepmother's questions under Dad's actual mother.
	//
	// Great-grandparents are generation-3 couples, so a heading naming one
	// member matches the couple containing them.
	var candidates []scored
	var partial []string
	for i := range t.Subjects {
		s := &t.Subjects[i]
		if s.Generation != gen {
			continue
		}
		best, covered := 0, false
		for _, memberID := range s.MemberIDs {
			p := t.People[memberID]
			if p == nil {
				continue
			}
			if wantSex != "" && p.Sex != "" && p.Sex != wantSex {
				continue
			}
			tokens := personTokens(p)
			n := overlap(headingTokens, tokens)
			if n > best {
				best = n
			}
			if n == len(headingTokens) {
				covered = true
			}
		}
		switch {
		case covered:
			candidates = append(candidates, scored{subject: s, score: best, tokens: len(headingTokens)})
		case best > 0:
			partial = append(partial, fmt.Sprintf("%s (%d/%d words)", s.DisplayName, best, len(headingTokens)))
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].subject.Slug < candidates[j].subject.Slug
	})
	sort.Strings(partial)

	if len(candidates) == 0 {
		reason := fmt.Sprintf("no generation-%d person matches every name word", gen)
		if len(partial) > 0 {
			reason += "; closest partial matches rejected as unsafe"
		} else {
			reason += "; may be a step-relative absent from the ancestor walk"
		}
		return nil, &Ambiguity{Heading: h, Candidates: partial, Reason: reason}
	}
	if len(candidates) > 1 {
		var names []string
		for _, c := range candidates {
			names = append(names, c.subject.DisplayName)
		}
		return nil, &Ambiguity{
			Heading:    h,
			Candidates: names,
			Reason:     fmt.Sprintf("%d generation-%d people match every name word", len(names), gen),
		}
	}

	why := fmt.Sprintf("gen %d", gen)
	if wantSex != "" {
		why += ", sex " + wantSex
	}
	why += fmt.Sprintf(", all %d name words", len(headingTokens))

	return &Match{Heading: h, Subject: candidates[0].subject.Slug, Why: why}, nil
}

// impliedSex reads a leading honorific such as "Grandpa" or "Grandma".
func impliedSex(heading string) string {
	for _, tok := range rawTokens(heading) {
		if sex, ok := honorificSex[tok]; ok {
			return sex
		}
	}
	return ""
}

// identifyingTokens strips honorifics and filler, leaving name words.
func identifyingTokens(heading string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range rawTokens(heading) {
		if noiseTokens[tok] || len(tok) < 2 {
			continue
		}
		out[tok] = true
	}
	return out
}

// personTokens is everything a person might be referred to by, including
// surnames they married into.
func personTokens(p *Person) map[string]bool {
	out := map[string]bool{}
	for _, tok := range rawTokens(p.Given + " " + p.Surname) {
		if len(tok) >= 2 {
			out[tok] = true
		}
	}
	for _, alias := range p.AliasSurnames {
		for _, tok := range rawTokens(alias) {
			if len(tok) >= 2 {
				out[tok] = true
			}
		}
	}
	return out
}

// rawTokens lowercases and splits on anything that is not a letter or digit, so
// "Grandma Margaret (Fletcher)Hale" yields margaret, fletcher, hale.
func rawTokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

func overlap(a, b map[string]bool) int {
	n := 0
	for tok := range a {
		if b[tok] {
			n++
		}
	}
	return n
}

// PersonSubjects maps the display name used in the markdown ("Dad", "Mom") to
// the subject slug for that person, so "About You" questions land on them.
func (t *Tree) PersonSubjects(rootLabels map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for label, gedcomName := range rootLabels {
		var found string
		for i := range t.Subjects {
			s := &t.Subjects[i]
			if s.Generation != 0 || len(s.MemberIDs) != 1 {
				continue
			}
			p := t.People[s.MemberIDs[0]]
			if p == nil {
				continue
			}
			if p.FullName() == strings.ReplaceAll(strings.ReplaceAll(gedcomName, "/", ""), "  ", " ") {
				found = s.Slug
				break
			}
		}
		if found == "" {
			return nil, fmt.Errorf("no generation-0 subject for %s (%s)", label, gedcomName)
		}
		out[label] = found
	}
	return out, nil
}
