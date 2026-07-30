package subjects

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The great-grandparent sections of the prompts file are written as surname
// lines rather than as people — "the Aldermans were immigrants from Sweden" — so
// every question under them resolves to the further-back bucket. Most of them
// name a specific couple, and belong on that couple's page.
//
// This routes only the unambiguous ones. Two rules keep it honest:
//
//   - The surname has to be written as a family: "the Aldermans", "the Osgood
//     family", "the Alderman's", "the Nash side", "the Vale line". A bare
//     mention is not enough, because these questions also name people in
//     passing.
//   - Exactly one couple may match. "the Hales, and the Fletchers" names two and
//     stays put, as does "Radley and Vance ancestors".
//
// Everything it declines is reported rather than dropped, so the decisions can
// be reviewed instead of trusted.

// familyMention matches a surname written as a family line. The plural is
// captured and stripped afterwards: a greedy [a-z]+ swallows it either way.
var familyMention = regexp.MustCompile(
	`(?i)\bthe\s+([A-Z][a-z]+)\b|\b([A-Z][a-z]+)'s\b|\b([A-Z][a-z]+)\s+(?:family|side|line|ancestors)\b`)

// notSurnames are words the patterns above pick up which are plainly not family
// names. They match no couple either way; the list only keeps the explanations
// readable.
var notSurnames = map[string]bool{
	"what": true, "one": true, "any": true, "that": true, "these": true,
	"family": true, "midwest": true, "united": true, "same": true, "other": true,
}

// singular strips a plural or possessive ending, so "Aldermans", "Alderman's" and
// "Aldermans'" all reach "alderman".
//
// The possessive has to go before the plural: stripping the apostrophe first
// left "alderman'" and matched nothing.
func singular(name string) string {
	n := strings.ToLower(name)
	n = strings.TrimSuffix(n, "'s")
	n = strings.TrimSuffix(n, "s'")
	n = strings.TrimSuffix(n, "'")
	if strings.HasSuffix(n, "s") && len(n) > 3 {
		// "Osgood" is already singular, so callers try both forms.
		return strings.TrimSuffix(n, "s")
	}
	return n
}

// FamilyNamesIn returns the surnames a question mentions as families.
func FamilyNamesIn(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range familyMention.FindAllStringSubmatch(text, -1) {
		for _, group := range m[1:] {
			if group == "" {
				continue
			}
			name := singular(group)
			if notSurnames[name] || seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// coupleSurnames indexes every surname a couple answers to, singular and as
// written, maiden and married.
func (t *Tree) coupleSurnames() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, s := range t.Subjects {
		if s.Kind != KindCouple {
			continue
		}
		names := map[string]bool{}
		for _, memberID := range s.MemberIDs {
			p := t.People[memberID]
			if p == nil {
				continue
			}
			for _, c := range append([]string{p.Surname, p.MarriedSurname}, p.AliasSurnames...) {
				if c == "" {
					continue
				}
				names[strings.ToLower(c)] = true
				names[singular(c)] = true
			}
		}
		out[s.Slug] = names
	}
	return out
}

// Routing is one decision about one question.
type Routing struct {
	Body    string
	Subject string   // the couple it moves to, empty when it stays
	Names   []string // family names the question mentioned
	Reason  string   // why it stayed, when it did
}

// RouteToCouple decides where a further-back question belongs.
//
// Two different reads of the text are used on purpose. Whether the question
// means to name a family at all comes from the family-style phrasing — "the
// Aldermans", "the Nash side", "the Vale line". Whether it names more than
// one comes from scanning every capitalised word against the couples' surnames,
// which is what catches "Grandma Alice's Radley and Vance ancestors": the
// phrasing only marks Vance, but Radley is there too, so the question is
// ambiguous and stays put.
func (t *Tree) RouteToCouple(body string) Routing {
	names := FamilyNamesIn(body)
	out := Routing{Body: body, Names: names}

	if len(names) == 0 {
		out.Reason = "names no family"
		return out
	}

	// Every capitalised word, so an ambiguity cannot hide behind phrasing.
	words := regexp.MustCompile(`\b[A-Z][a-zA-Z']+\b`).FindAllString(body, -1)
	mentioned := map[string]bool{}
	for _, w := range words {
		mentioned[strings.ToLower(w)] = true
		mentioned[singular(w)] = true
	}

	byCouple := t.coupleSurnames()
	var matched []string
	for slug, surnames := range byCouple {
		for name := range mentioned {
			if surnames[name] {
				matched = append(matched, slug)
				break
			}
		}
	}
	sort.Strings(matched)

	switch len(matched) {
	case 1:
		out.Subject = matched[0]
	case 0:
		out.Reason = "no couple carries " + strings.Join(names, " or ")
	default:
		out.Reason = "names " + strconv.Itoa(len(matched)) + " couples"
	}
	return out
}
