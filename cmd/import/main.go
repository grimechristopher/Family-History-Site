// Command import populates the database from the GEDCOM and the prompts file.
//
// It is idempotent: re-running updates rows in place rather than duplicating
// them, and questions removed from the markdown are archived rather than deleted
// so that answers already written against them survive.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
	"github.com/grimechristopher/family-history-site/internal/importer"
	"github.com/grimechristopher/family-history-site/internal/migrate"
	"github.com/grimechristopher/family-history-site/internal/prompts"
	"github.com/grimechristopher/family-history-site/internal/store"
	"github.com/grimechristopher/family-history-site/internal/subjects"
)

func main() {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	gedPath := fs.String("gedcom", "", "path to the GEDCOM export (required)")
	promptsPath := fs.String("prompts", "", "path to Prompts 3.md (required)")
	databaseURL := fs.String("database-url", os.Getenv("DATABASE_URL"), "Postgres URL (defaults to $DATABASE_URL)")
	dadEmail := fs.String("dad-email", "", "Dad's email address (required)")
	momEmail := fs.String("mom-email", "", "Mom's email address (required)")
	adminEmail := fs.String("admin-email", "", "your email address, for admin access (required)")
	dryRun := fs.Bool("dry-run", false, "parse and match, then roll back without committing")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: import -gedcom FILE -prompts FILE -dad-email A -mom-email B -admin-email C\n\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}

	missing := map[string]string{
		"-gedcom":       *gedPath,
		"-prompts":      *promptsPath,
		"-database-url": *databaseURL,
		"-dad-email":    *dadEmail,
		"-mom-email":    *momEmail,
		"-admin-email":  *adminEmail,
	}
	var absent []string
	for name, value := range missing {
		if value == "" {
			absent = append(absent, name)
		}
	}
	if len(absent) > 0 {
		sort.Strings(absent)
		fmt.Fprintf(os.Stderr, "missing required flags: %v\n\n", absent)
		fs.Usage()
		os.Exit(2)
	}

	if err := run(*gedPath, *promptsPath, *databaseURL, *dadEmail, *momEmail, *adminEmail, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "\nimport failed: %v\n", err)
		os.Exit(1)
	}
}

func run(gedPath, promptsPath, databaseURL, dadEmail, momEmail, adminEmail string, dryRun bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	gf, err := os.Open(gedPath)
	if err != nil {
		return fmt.Errorf("open gedcom: %w", err)
	}
	defer gf.Close()
	ged, err := gedcom.Parse(gf)
	if err != nil {
		return fmt.Errorf("parse gedcom: %w", err)
	}
	fmt.Printf("parsed gedcom: %d individuals, %d families\n", len(ged.Individuals), len(ged.Families))

	pf, err := os.Open(promptsPath)
	if err != nil {
		return fmt.Errorf("open prompts: %w", err)
	}
	defer pf.Close()
	qs, err := prompts.Parse(pf)
	if err != nil {
		return fmt.Errorf("parse prompts: %w", err)
	}
	headings, _ := prompts.Headings(qs)
	fmt.Printf("parsed prompts: %d questions across %d headings\n", len(qs), len(headings))

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	if err := migrate.Run(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	fmt.Println("schema up to date")

	opts := importer.Options{
		Tree: subjects.DefaultOptions(),
		Contributors: []importer.Contributor{
			{Label: "Dad", Email: dadEmail, GedcomName: "Peter John /Hale/"},
			{Label: "Mom", Email: momEmail, GedcomName: "Ruth Ann /Brennan/"},
		},
		Admins: []importer.Admin{{Label: "Chris", Email: adminEmail}},
	}

	s := store.New(pool)
	var res *importer.Result

	// A dry run does the entire import and then deliberately fails, so the
	// transaction rolls back and nothing is written.
	errSentinel := fmt.Errorf("dry run: rolling back")
	err = s.InTx(ctx, func(db store.DBTX) error {
		r, runErr := importer.Run(ctx, db, ged, qs, opts)
		if runErr != nil {
			return runErr
		}
		res = r
		if dryRun {
			return errSentinel
		}
		return nil
	})
	if err != nil && err != errSentinel {
		return err
	}

	fmt.Printf("\npeople:    %d\n", res.People)
	fmt.Printf("subjects:  %d\n", res.Subjects)
	fmt.Printf("users:     %d\n", res.Users)
	fmt.Printf("questions: %d\n", res.Questions)
	for _, label := range sortedKeys(res.PerPerson) {
		fmt.Printf("  %-6s %d\n", label, res.PerPerson[label])
	}
	if res.Archived > 0 {
		fmt.Printf("archived:  %d question(s) no longer in the markdown\n", res.Archived)
	}

	if dryRun {
		fmt.Println("\ndry run: nothing was written")
		return nil
	}
	fmt.Println("\nimport committed")
	return nil
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
