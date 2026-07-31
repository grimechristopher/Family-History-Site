// Command treejson converts a GEDCOM export into the JSON the importer reads.
//
// Run once per export. After that the JSON is the family's file: readable,
// correctable, and free of the hundred thousand lines of sources and citations that
// nothing here has ever looked at.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
	"github.com/grimechristopher/family-history-site/internal/tree"
)

func main() {
	in := flag.String("gedcom", "", "the GEDCOM export to read (required)")
	out := flag.String("out", "", "the JSON file to write (required)")
	generations := flag.Int("generations", 3, "generations above each root to keep")
	siblingsUpTo := flag.Int("siblings", 1,
		"keep the brothers and sisters of everybody this many generations up; -1 for none")
	cousins := flag.Bool("cousins", true, "keep the children of those siblings")
	extra := flag.String("extra-names", "",
		`comma-separated names to keep who no ancestor walk reaches, "Given /Surname/"`)
	var roots namesFlag
	flag.Var(&roots, "root",
		`somebody a line is drawn from, "Given /Surname/". Repeatable. Without any, the whole export is written.`)
	flag.Parse()
	if *in == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}

	fh, err := os.Open(*in)
	if err != nil {
		fail(err)
	}
	defer fh.Close()
	parsed, err := gedcom.Parse(fh)
	if err != nil {
		fail(err)
	}

	// Folded before writing, so the JSON does not carry a duplicate somebody would
	// then have to notice for themselves.
	merged := parsed.MergeDuplicates()

	// Only the people the import will look for. An export has thousands of records
	// reached through fifth cousins and their in-laws, and a file nobody can read is
	// a file nobody will correct.
	var keep map[string]bool
	if len(roots) > 0 {
		rootIDs := make([]string, 0, len(roots))
		for _, name := range roots {
			id, err := parsed.FindByName(name)
			if err != nil {
				fail(fmt.Errorf("root %q: %w", name, err))
			}
			rootIDs = append(rootIDs, id)
		}
		var extraNames []string
		for _, n := range strings.Split(*extra, ",") {
			if n = strings.TrimSpace(n); n != "" {
				extraNames = append(extraNames, n)
			}
		}
		keep = tree.Window(parsed, rootIDs, *generations, *siblingsUpTo, *cousins, extraNames)
		fmt.Printf("keeping %d of %d people: %d generations up, siblings to %d\n",
			len(keep), len(parsed.Individuals), *generations, *siblingsUpTo)
	}

	doc := tree.FromGedcom(parsed, keep)
	f, err := os.Create(*out)
	if err != nil {
		fail(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		fail(err)
	}

	fmt.Printf("%d people written to %s\n", len(doc.People), *out)
	if len(merged) > 0 {
		fmt.Printf("folded %d duplicate record(s) on the way:\n", len(merged))
		for _, m := range merged {
			fmt.Printf("  %s\n", m.Name)
		}
	}

	// Read it straight back, so the file is known to build the same tree rather than
	// assumed to.
	check, err := os.Open(*out)
	if err != nil {
		fail(err)
	}
	defer check.Close()
	rebuilt, err := tree.Load(check)
	if err != nil {
		fail(fmt.Errorf("the file just written does not load: %w", err))
	}
	fmt.Printf("read back: %d people, %d families\n",
		len(rebuilt.Individuals), len(rebuilt.Families))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "treejson:", err)
	os.Exit(1)
}

// namesFlag collects a repeated -root.
type namesFlag []string

func (n *namesFlag) String() string { return strings.Join(*n, ", ") }
func (n *namesFlag) Set(v string) error {
	*n = append(*n, v)
	return nil
}
