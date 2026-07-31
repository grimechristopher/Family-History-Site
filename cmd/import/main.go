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
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grimechristopher/family-history-site/internal/gedcom"
	"github.com/grimechristopher/family-history-site/internal/importer"
	"github.com/grimechristopher/family-history-site/internal/migrate"
	"github.com/grimechristopher/family-history-site/internal/prompts"
	"github.com/grimechristopher/family-history-site/internal/store"
	"github.com/grimechristopher/family-history-site/internal/subjects"
	"github.com/grimechristopher/family-history-site/internal/tree"
)

func main() {
	fs := flag.NewFlagSet("import", flag.ExitOnError)
	gedPath := fs.String("gedcom", "", "path to the GEDCOM export (required)")
	promptsPath := fs.String("prompts", "", "path to Prompts 3.md (required)")
	databaseURL := fs.String("database-url", os.Getenv("DATABASE_URL"), "Postgres URL (defaults to $DATABASE_URL)")
	dadEmail := fs.String("dad-email", "", "Dad's email address (required)")
	momEmail := fs.String("mom-email", "", "Mom's email address (required)")
	adminEmail := fs.String("admin-email", "", "your email address, for admin access (required)")
	// The family's own names are configuration, not code: they come from the
	// environment so this repository never has to carry them.
	dadName := fs.String("dad-name", os.Getenv("DAD_GEDCOM_NAME"),
		`GEDCOM name of the first person being asked, "Given /Surname/" (defaults to $DAD_GEDCOM_NAME)`)
	momName := fs.String("mom-name", os.Getenv("MOM_GEDCOM_NAME"),
		`GEDCOM name of the second person (defaults to $MOM_GEDCOM_NAME)`)
	extraNames := fs.String("extra-names", os.Getenv("EXTRA_GEDCOM_NAMES"),
		"comma-separated GEDCOM names to include who are not blood ancestors, such as a "+
			"step-parent (defaults to $EXTRA_GEDCOM_NAMES)")
	generations := fs.Int("generations", 3, "generations above the two roots to import")
	siblingsUpTo := fs.Int("siblings", 1,
		"include brothers and sisters of everybody this many generations up: 1 covers "+
			"the roots' own siblings and their aunts and uncles; -1 for none")
	cousins := fs.Bool("cousins", true, "also include the children of those siblings")
	familySlug := fs.String("family", "home", "slug of the family to import into")
	var people personList
	fs.Var(&people, "person",
		`somebody questions are asked of, as "Heading=email=GEDCOM name". Repeatable.
	The heading is the "# ..." line in the prompts file. The GEDCOM name may be left
	off for somebody not in the tree yet, who is then asked questions but is not a
	root of the ancestor walk. Overrides -dad-name and -mom-name entirely.`)
	adminLabel := fs.String("admin-label", envOr("ADMIN_LABEL", "Admin"),
		"how the admin is named on the site (defaults to $ADMIN_LABEL)")
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
		"-admin-email":  *adminEmail,
	}
	if len(people) == 0 {
		missing["-dad-name"] = *dadName
		missing["-mom-name"] = *momName
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

	var extras []string
	for _, name := range strings.Split(*extraNames, ",") {
		if name = strings.TrimSpace(name); name != "" {
			extras = append(extras, name)
		}
	}

	// -person, when given, replaces the two-parent shape entirely: a family may be
	// one parent, or four siblings, and nothing about the import needs there to be
	// exactly two of them.
	contributors := []importer.Contributor(people)
	if len(contributors) == 0 {
		contributors = []importer.Contributor{
			{Label: "Dad", Email: *dadEmail, GedcomName: *dadName},
			{Label: "Mom", Email: *momEmail, GedcomName: *momName},
		}
	}

	cfg := importConfig{
		GedPath: *gedPath, PromptsPath: *promptsPath, DatabaseURL: *databaseURL,
		DadEmail: *dadEmail, MomEmail: *momEmail, AdminEmail: *adminEmail,
		DadName: *dadName, MomName: *momName, AdminLabel: *adminLabel, Family: *familySlug,
		ExtraNames: extras, Generations: *generations, DryRun: *dryRun,
		SiblingsUpTo: *siblingsUpTo, Cousins: *cousins,
		Contributors: contributors,
	}
	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "\nimport failed: %v\n", err)
		os.Exit(1)
	}
}

// importConfig is everything the import needs. A struct rather than a dozen
// positional arguments, which is what it had grown into once the family's names
// moved out of the code and became configuration too.
type importConfig struct {
	GedPath, PromptsPath, DatabaseURL string
	DadEmail, MomEmail, AdminEmail    string
	// GEDCOM-form names of the two people being asked, and how they are labelled.
	DadName, MomName, AdminLabel string
	// Family is the slug this import writes into.
	Family      string
	ExtraNames  []string
	Generations int
	// SiblingsUpTo and Cousins widen the walk beyond the line of descent, to the
	// brothers, sisters and cousins a family actually talks about.
	SiblingsUpTo int
	Cousins      bool
	DryRun       bool
	// Contributors are everybody questions are asked of. A family may have one, two
	// or several: two parents, or four siblings recording their own parents.
	Contributors []importer.Contributor
}

// personList collects repeated -person flags.
type personList []importer.Contributor

func (p *personList) String() string { return "" }

func (p *personList) Set(v string) error {
	parts := strings.SplitN(v, "=", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return fmt.Errorf(`want "Heading=email" or "Heading=email=GEDCOM name", got %q`, v)
	}
	c := importer.Contributor{Label: strings.TrimSpace(parts[0]), Email: strings.TrimSpace(parts[1])}
	if len(parts) == 3 {
		c.GedcomName = strings.TrimSpace(parts[2])
	}
	*p = append(*p, c)
	return nil
}

// envOr reads an environment variable, falling back to a default that names
// nobody.
func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func run(cfg importConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	gf, err := os.Open(cfg.GedPath)
	if err != nil {
		return fmt.Errorf("open gedcom: %w", err)
	}
	defer gf.Close()
	// JSON is the family's own file, readable and correctable. A GEDCOM still works
	// so an export can be imported directly, but nothing needs one to deploy.
	var ged *gedcom.File
	if strings.HasSuffix(strings.ToLower(cfg.GedPath), ".json") {
		ged, err = tree.Load(gf)
		if err != nil {
			return fmt.Errorf("read tree: %w", err)
		}
		fmt.Printf("read tree: %d people, %d families\n", len(ged.Individuals), len(ged.Families))
	} else {
		ged, err = gedcom.Parse(gf)
		if err != nil {
			return fmt.Errorf("parse gedcom: %w", err)
		}
		fmt.Printf("parsed gedcom: %d individuals, %d families\n", len(ged.Individuals), len(ged.Families))
	}

	// The same person entered twice, which a tree built over years by several
	// people always has. Reported rather than done quietly: changing the shape of
	// somebody's family is not a thing to do in silence, and if the rule ever folds
	// two people who are not one person, this is where it would be noticed.
	//
	// The JSON is written with the duplicates already folded, so this normally finds
	// nothing there and everything in a fresh GEDCOM.
	if merged := ged.MergeDuplicates(); len(merged) > 0 {
		fmt.Printf("merged %d duplicate record(s):\n", len(merged))
		for _, m := range merged {
			fmt.Printf("  %s (%s into %s)\n", m.Name, m.Removed, m.Kept)
		}
	}

	pf, err := os.Open(cfg.PromptsPath)
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

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	if err := migrate.Run(ctx, pool); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	fmt.Println("schema up to date")

	// The roots are whoever is actually in the tree. A family may have one -- a
	// widow, or one side of a couple whose in-laws are a family of their own.
	var roots []string
	for _, c := range cfg.Contributors {
		if c.GedcomName != "" {
			roots = append(roots, c.GedcomName)
		}
	}
	if len(roots) == 0 {
		return fmt.Errorf("no contributor names anybody in the tree, so there is nothing to walk from")
	}

	opts := importer.Options{
		Tree: subjects.Options{
			RootNames:    roots,
			Generations:  cfg.Generations,
			ExtraNames:   cfg.ExtraNames,
			SiblingsUpTo: cfg.SiblingsUpTo,
			Cousins:      cfg.Cousins,
		},
		Contributors: cfg.Contributors,
		Admins:       []importer.Admin{{Label: cfg.AdminLabel, Email: cfg.AdminEmail}},
	}

	s := store.New(pool)

	// Which family this import belongs to. Everything it writes carries the id, and
	// the transaction sets app.family_id so row-level security applies to the
	// importer exactly as it does to the site.
	fam, err := s.FamilyBySlug(ctx, cfg.Family)
	if err != nil {
		return fmt.Errorf("family %q: %w (create it with cmd/family)", cfg.Family, err)
	}
	ctx = store.WithFamily(ctx, fam.ID)
	fmt.Printf("importing into %s (%s)\n", fam.DisplayName, fam.Slug)

	var res *importer.Result

	// A dry run does the entire import and then deliberately fails, so the
	// transaction rolls back and nothing is written.
	errSentinel := fmt.Errorf("dry run: rolling back")
	err = s.InTx(ctx, func(db store.DBTX) error {
		if _, err := db.Exec(ctx, "SELECT set_config('app.family_ids', $1, true)",
			strconv.FormatInt(fam.ID, 10)); err != nil {
			return fmt.Errorf("scope the import to family %d: %w", fam.ID, err)
		}
		r, runErr := importer.Run(ctx, db, ged, qs, opts)
		if runErr != nil {
			return runErr
		}
		res = r
		if cfg.DryRun {
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
	if res.Generic > 0 {
		fmt.Printf("  of which %d are generic questions for the great-grandparent couples\n", res.Generic)
	}
	for _, label := range sortedKeys(res.PerPerson) {
		fmt.Printf("  %-6s %d\n", label, res.PerPerson[label])
	}
	if len(res.Routed) > 0 {
		fmt.Printf("\nmoved out of \"Further Back\" onto a named couple:\n")
		for _, r := range res.Routed {
			body := r.Body
			if len(body) > 62 {
				body = body[:62] + "..."
			}
			fmt.Printf("  %-46s %s\n", r.Subject, body)
		}
		fmt.Println()
	}
	if res.PrunedSubjects > 0 || res.PrunedPeople > 0 {
		fmt.Printf("\nremoved, no longer in this family: %d subject(s), %d person/people\n",
			res.PrunedSubjects, res.PrunedPeople)
	}
	if res.KeptSubjects > 0 {
		fmt.Printf("kept %d subject(s) that are no longer derived but have answers or "+
			"questions attached\n", res.KeptSubjects)
	}
	if res.Archived > 0 {
		fmt.Printf("archived:  %d question(s) no longer in the markdown\n", res.Archived)
	}

	if cfg.DryRun {
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
