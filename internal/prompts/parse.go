// Package prompts parses the Obsidian markdown file of interview questions.
//
// Structure: "#" is the person the questions are asked of, "##" a section,
// "###" a subject or topic, "####" a modifier (only "Proposed" is used).
// Any other non-blank line is a question.
package prompts

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

type Question struct {
	Person     string // "Dad", "Mom"
	Section    string // "Parents", "About You"
	Subsection string // "James R Hale", "Childhood", or "" when absent
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

// ImportKey is the stable identity of a question across re-imports. Keying on
// position rather than content means rewording a question in Obsidian updates
// the existing row instead of creating a duplicate.
func (q Question) ImportKey() string {
	return fmt.Sprintf("%s|%s|%s|%d", q.Person, q.Section, q.Subsection, q.Ordinal)
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
