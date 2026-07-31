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

	"github.com/grimechristopher/family-history-site/internal/gedcom"
	"github.com/grimechristopher/family-history-site/internal/tree"
)

func main() {
	in := flag.String("gedcom", "", "the GEDCOM export to read (required)")
	out := flag.String("out", "", "the JSON file to write (required)")
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

	doc := tree.FromGedcom(parsed)
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
