// Package gedcom parses the subset of GEDCOM 5.5 needed to build a family
// tree: names, sex, birth and death years, and parent/child links.
//
// A dependency is deliberately avoided. Only nine tags matter, the format is
// line-oriented, and this runs once as an import.
package gedcom

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Individual struct {
	ID        string // GEDCOM xref, e.g. "@I1@"
	Given     string
	Surname   string
	Sex       string
	BirthYear int // 0 means unknown
	DeathYear int
	FamC      []string // families in which this person is a child
	FamS      []string // families in which this person is a spouse
}

// Name returns the individual's name in GEDCOM form, "Given /Surname/", which
// is the form used as a key in mapping/subjects.yaml.
func (i *Individual) Name() string {
	return fmt.Sprintf("%s /%s/", i.Given, i.Surname)
}

type Family struct {
	ID        string
	HusbandID string
	WifeID    string
	ChildIDs  []string
}

type File struct {
	Individuals map[string]*Individual
	Families    map[string]*Family
}

func Parse(r io.Reader) (*File, error) {
	f := &File{
		Individuals: map[string]*Individual{},
		Families:    map[string]*Family{},
	}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		curIndi  *Individual
		curFam   *Family
		curEvent string // "BIRT", "DEAT", or "" — scopes the level-2 DATE
	)

	for sc.Scan() {
		level, xref, tag, value, ok := parseLine(sc.Text())
		if !ok {
			continue
		}
		switch level {
		case 0:
			curIndi, curFam, curEvent = nil, nil, ""
			switch tag {
			case "INDI":
				curIndi = &Individual{ID: xref}
				f.Individuals[xref] = curIndi
			case "FAM":
				curFam = &Family{ID: xref}
				f.Families[xref] = curFam
			}
		case 1:
			curEvent = ""
			switch {
			case curIndi != nil:
				switch tag {
				case "NAME":
					// Some records carry several NAME lines; the first wins.
					if curIndi.Given == "" && curIndi.Surname == "" {
						curIndi.Given, curIndi.Surname = splitName(value)
					}
				case "SEX":
					curIndi.Sex = value
				case "BIRT", "DEAT":
					curEvent = tag
				case "FAMC":
					curIndi.FamC = append(curIndi.FamC, value)
				case "FAMS":
					curIndi.FamS = append(curIndi.FamS, value)
				}
			case curFam != nil:
				switch tag {
				case "HUSB":
					curFam.HusbandID = value
				case "WIFE":
					curFam.WifeID = value
				case "CHIL":
					curFam.ChildIDs = append(curFam.ChildIDs, value)
				}
			}
		case 2:
			if curIndi == nil || tag != "DATE" {
				continue
			}
			year := extractYear(value)
			if year == 0 {
				continue
			}
			switch curEvent {
			case "BIRT":
				if curIndi.BirthYear == 0 {
					curIndi.BirthYear = year
				}
			case "DEAT":
				if curIndi.DeathYear == 0 {
					curIndi.DeathYear = year
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan gedcom: %w", err)
	}
	return f, nil
}

// FindByName resolves a GEDCOM-form name ("Given /Surname/") to an xref.
//
// Exactly one match is required: zero or several is an error. Guessing here
// would silently file questions under the wrong ancestor, and the mistake would
// only surface after they had been answered.
func (f *File) FindByName(name string) (string, error) {
	return f.FindByNameIn(name, nil)
}

// FindByNameIn resolves a name considering only individuals present in within,
// a set produced by Ancestors. Passing nil considers everyone.
//
// The restriction is not a convenience: the real tree contains Ancestry merge
// duplicates — "Bertram Lyle /Fletcher/" appears twice — and only one of each
// pair is an actual ancestor. Scoping the search to the imported window
// disambiguates on a real property rather than a hand-picked xref, and still
// fails loudly if two candidates survive.
func (f *File) FindByNameIn(name string, within map[string]int) (string, error) {
	var matches []string
	for id, ind := range f.Individuals {
		if ind.Name() != name {
			continue
		}
		if within != nil {
			if _, ok := within[id]; !ok {
				continue
			}
		}
		matches = append(matches, id)
	}
	sort.Strings(matches)

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		if within != nil {
			return "", fmt.Errorf("no individual named %q within the imported generations", name)
		}
		return "", fmt.Errorf("no individual named %q", name)
	default:
		return "", fmt.Errorf("%d individuals named %q: %v", len(matches), name, matches)
	}
}

// parseLine splits a GEDCOM line into "LEVEL [@XREF@] TAG [value]".
func parseLine(s string) (level int, xref, tag, value string, ok bool) {
	// Ancestry exports sometimes carry a UTF-8 byte-order mark on line one.
	s = strings.TrimRight(strings.TrimPrefix(s, "\uFEFF"), "\r\n")
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return 0, "", "", "", false
	}

	sp := strings.IndexByte(s, ' ')
	if sp < 0 {
		return 0, "", "", "", false
	}
	level, err := strconv.Atoi(s[:sp])
	if err != nil {
		return 0, "", "", "", false
	}
	rest := s[sp+1:]

	if strings.HasPrefix(rest, "@") {
		end := strings.IndexByte(rest[1:], '@')
		if end < 0 {
			return 0, "", "", "", false
		}
		xref = rest[:end+2]
		rest = strings.TrimLeft(rest[end+2:], " ")
	}

	sp = strings.IndexByte(rest, ' ')
	if sp < 0 {
		return level, xref, rest, "", true
	}
	return level, xref, rest[:sp], rest[sp+1:], true
}

// splitName turns "Thomas James /Hale/" into given and surname parts.
func splitName(v string) (given, surname string) {
	i := strings.IndexByte(v, '/')
	if i < 0 {
		return strings.TrimSpace(v), ""
	}
	given = strings.TrimSpace(v[:i])
	rest := v[i+1:]
	if j := strings.IndexByte(rest, '/'); j >= 0 {
		rest = rest[:j]
	}
	return given, strings.TrimSpace(rest)
}

var yearRe = regexp.MustCompile(`\b(\d{4})\b`)

// extractYear pulls a plausible year out of a GEDCOM date value, tolerating
// forms like "12 MAR 1985", "ABT 1900", and "BET 1910 AND 1912" (last wins).
func extractYear(v string) int {
	m := yearRe.FindAllStringSubmatch(v, -1)
	if len(m) == 0 {
		return 0
	}
	y, err := strconv.Atoi(m[len(m)-1][1])
	if err != nil || y < 1000 || y > 2200 {
		return 0
	}
	return y
}
