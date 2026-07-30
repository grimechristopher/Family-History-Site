// Command rewrite-prompts canonicalises the ancestor headings in the prompts
// markdown.
//
// The file was written conversationally — "### Grandpa Ward", "### Grandma
// Margaret (Fletcher)Hale" — which is fine for a person reading it and poor for
// anyone trying to work out who is meant. This rewrites each ancestor heading to
// the full name in NEHGS Register style, "Given (Maiden) Married", so the file
// and the site say the same thing.
//
// Topic headings under "About You" (Childhood, School, and so on) are left
// alone: they are not people. Everything that is not a heading is copied
// through byte for byte.
//
// Renaming headings is safe for existing answers: question identity keys on the
// subject a question resolves to, not on the words above it.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
	"github.com/grimechristopher/family-history-site/internal/prompts"
	"github.com/grimechristopher/family-history-site/internal/subjects"
)

func main() {
	gedPath := flag.String("gedcom", "", "path to the GEDCOM export (required)")
	promptsPath := flag.String("prompts", "", "path to Prompts 3.md (required)")
	out := flag.String("out", "", "where to write the rewritten file (required)")
	// Names come from the environment, so this repository carries none.
	dadName := flag.String("dad-name", os.Getenv("DAD_GEDCOM_NAME"),
		`GEDCOM name of the first person being asked (defaults to $DAD_GEDCOM_NAME)`)
	momName := flag.String("mom-name", os.Getenv("MOM_GEDCOM_NAME"),
		`GEDCOM name of the second person (defaults to $MOM_GEDCOM_NAME)`)
	extraNames := flag.String("extra-names", os.Getenv("EXTRA_GEDCOM_NAMES"),
		"comma-separated GEDCOM names to include who are not blood ancestors "+
			"(defaults to $EXTRA_GEDCOM_NAMES)")
	generations := flag.Int("generations", 3, "generations above the two roots")
	flag.Parse()

	if *gedPath == "" || *promptsPath == "" || *out == "" {
		fmt.Fprintln(os.Stderr, "usage: rewrite-prompts -gedcom FILE -prompts FILE -out FILE")
		os.Exit(2)
	}
	var extras []string
	for _, name := range strings.Split(*extraNames, ",") {
		if name = strings.TrimSpace(name); name != "" {
			extras = append(extras, name)
		}
	}
	if *dadName == "" || *momName == "" {
		fmt.Fprintln(os.Stderr, "rewrite-prompts: -dad-name and -mom-name are required "+
			"(or set $DAD_GEDCOM_NAME and $MOM_GEDCOM_NAME)")
		os.Exit(2)
	}

	if err := run(*gedPath, *promptsPath, *out, *dadName, *momName, extras, *generations); err != nil {
		fmt.Fprintf(os.Stderr, "rewrite-prompts: %v\n", err)
		os.Exit(1)
	}
}

func run(gedPath, promptsPath, outPath, dadName, momName string, extraNames []string, generations int) error {
	gf, err := os.Open(gedPath)
	if err != nil {
		return err
	}
	defer gf.Close()
	ged, err := gedcom.Parse(gf)
	if err != nil {
		return fmt.Errorf("parse gedcom: %w", err)
	}

	tree, err := subjects.Derive(ged, subjects.Options{
		RootNames:   []string{dadName, momName},
		Generations: generations,
		ExtraNames:  extraNames,
	})
	if err != nil {
		return fmt.Errorf("derive tree: %w", err)
	}

	pf, err := os.Open(promptsPath)
	if err != nil {
		return err
	}
	qs, err := prompts.Parse(pf)
	pf.Close()
	if err != nil {
		return fmt.Errorf("parse prompts: %w", err)
	}

	personSubjects, err := tree.PersonSubjects(map[string]string{
		"Dad": dadName,
		"Mom": momName,
	})
	if err != nil {
		return err
	}

	headings, _ := prompts.Headings(qs)
	matches, ambiguities := tree.MatchHeadings(headings, personSubjects, subjects.Overrides{})
	if len(ambiguities) > 0 {
		for _, a := range ambiguities {
			fmt.Fprintf(os.Stderr, "unresolved: %s\n", a.Error())
		}
		return fmt.Errorf("%d heading(s) could not be resolved; refusing to rewrite", len(ambiguities))
	}

	displayBySlug := map[string]string{}
	for _, s := range tree.Subjects {
		displayBySlug[s.Slug] = s.DisplayName
	}

	// Only headings that name a person get rewritten, and only when the resolved
	// name actually differs.
	rename := map[prompts.Heading]string{}
	for _, m := range matches {
		if m.Heading.Subsection == "" || m.Topic != "" {
			continue
		}
		want, ok := displayBySlug[m.Subject]
		if !ok || want == m.Heading.Subsection {
			continue
		}
		rename[m.Heading] = want
	}

	src, err := os.Open(promptsPath)
	if err != nil {
		return err
	}
	defer src.Close()

	var buf strings.Builder
	sc := bufio.NewScanner(src)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var person, section string
	changed := 0

	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "### "):
			sub := strings.TrimSpace(strings.TrimPrefix(trimmed, "### "))
			h := prompts.Heading{Person: person, Section: section, Subsection: sub}
			if want, ok := rename[h]; ok {
				fmt.Printf("  %-46s -> %s\n", sub, want)
				buf.WriteString("### " + want + "\n")
				changed++
				continue
			}
		case strings.HasPrefix(trimmed, "## "):
			section = strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
		case strings.HasPrefix(trimmed, "# "):
			person = strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
			section = ""
		}
		buf.WriteString(line + "\n")
	}
	if err := sc.Err(); err != nil {
		return err
	}

	if err := os.WriteFile(outPath, []byte(buf.String()), 0o644); err != nil {
		return err
	}
	fmt.Printf("\n%d heading(s) rewritten -> %s\n", changed, outPath)
	return nil
}
