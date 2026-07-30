// Package prompts parses the Obsidian markdown file of interview questions.
//
// Structure: "#" is the person the questions are asked of, "##" a section,
// "###" a subject or topic, "####" a modifier (only "Proposed" is used).
// Any other non-blank line is a question.
package prompts

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
)

type Question struct {
	Person     string // "Dad", "Mom"
	Section    string // "Parents", "About You"
	Subsection string // "Peter S Hale", "Childhood", or "" when absent
	IsProposed bool
	Ordinal    int // 1-based position within (Person, Section, Subsection)
	Body       string
}

// Heading identifies the markdown location a question came from.
type Heading struct {
	Person     string
	Section    string
	Subsection string
}

func (q Question) Heading() Heading {
	return Heading{Person: q.Person, Section: q.Section, Subsection: q.Subsection}
}

func (h Heading) String() string {
	if h.Subsection == "" {
		return fmt.Sprintf("%s / %s", h.Person, h.Section)
	}
	return fmt.Sprintf("%s / %s / %s", h.Person, h.Section, h.Subsection)
}

// ImportKey builds the stable identity of a question across re-imports.
//
// It is content-addressed: who is being asked, who the question is about, and a
// hash of the question itself. Nothing about position is involved, so questions
// can be added, removed or reordered in the prompts file freely.
//
// Two positional schemes were tried first and both corrupted data. Keying on the
// heading text meant renaming "### Alice May Fletcher" gave all seventeen of her
// questions new identities and archived their answers. Keying on position within
// the subject meant that routing a question onto a different subject shifted
// every ordinal after it, so the next import wrote one question's text into
// another question's row through ON CONFLICT DO UPDATE -- ten questions silently
// lost their wording that way.
//
// subject is the one the heading resolves to, before any routing. Routing must
// not change a question's identity, or moving it would archive its answers. It is
// in the hash rather than left out because the prompts file asks four questions in
// identical words of different people -- "What was her personality like?" -- and
// without the subject those would be told apart only by which came first in the
// file, so inserting a question above them could quietly swap their answers over.
//
// occurrence separates the same words asked twice about the same person, which is
// the only case left where order decides, and a harmless one: the two rows are
// indistinguishable anyway.
//
// Content addressing has one real cost. Rewording a question creates a new row and
// archives the old one, taking its answers into the archive rather than carrying
// them forward. That is the safer failure: an answer stays attached to the words it
// was actually answering, and nothing is ever overwritten.
// The person is deliberately not part of the key. Robert, Frank, Tony and Inez are
// given the same prompts about their parents, and keying by person made that four
// rows of the same question -- four cards, four places to answer, the same sentence
// printed four times in any list. Keyed by content alone they are one question, and
// the four of them are recorded against it as the people asked.
//
// Occurrence is still counted per person, so somebody asked the same thing twice in
// their own section still gets two rows, and so does everybody else.
func ImportKey(subject, body string, occurrence int) string {
	sum := sha256.Sum256([]byte(subject + "\n" + normaliseBody(body)))
	return fmt.Sprintf("%s|%d", hex.EncodeToString(sum[:8]), occurrence)
}

// normaliseBody collapses whitespace so that reflowing a line in Obsidian, or
// changing trailing spaces, is not treated as a different question.
func normaliseBody(body string) string {
	return strings.Join(strings.Fields(strings.ToLower(body)), " ")
}

func Parse(r io.Reader) ([]Question, error) {
	var (
		out        []Question
		person     string
		section    string
		subsection string
		proposed   bool
		ordinals   = map[string]int{}
	)

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "> ") {
			continue
		}

		switch {
		case strings.HasPrefix(line, "#### "):
			// The only modifier in use marks a block of proposed questions.
			proposed = strings.EqualFold(strings.TrimPrefix(line, "#### "), "Proposed")
			continue
		case strings.HasPrefix(line, "### "):
			subsection = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			proposed = false
			continue
		case strings.HasPrefix(line, "## "):
			section = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			subsection, proposed = "", false
			continue
		case strings.HasPrefix(line, "# "):
			person = strings.TrimSpace(strings.TrimPrefix(line, "# "))
			section, subsection, proposed = "", "", false
			continue
		case strings.HasPrefix(line, "#"):
			// Deeper or malformed heading; ignore rather than treat as a question.
			continue
		}

		if person == "" {
			return nil, fmt.Errorf("question before any person heading: %q", line)
		}

		key := person + "|" + section + "|" + subsection
		ordinals[key]++

		out = append(out, Question{
			Person:     person,
			Section:    section,
			Subsection: subsection,
			IsProposed: proposed,
			Ordinal:    ordinals[key],
			Body:       line,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan prompts: %w", err)
	}
	return out, nil
}

// Headings returns the distinct headings in first-appearance order, with the
// number of questions under each.
func Headings(qs []Question) ([]Heading, map[Heading]int) {
	counts := map[Heading]int{}
	var order []Heading
	for _, q := range qs {
		h := q.Heading()
		if _, seen := counts[h]; !seen {
			order = append(order, h)
		}
		counts[h]++
	}
	return order, counts
}
