// Command user sets the email address a person signs in with.
//
// Two places have to agree before anyone can sign in:
//
//   - family.users, this site's allowlist. An address that is not in here is
//     turned away even with a valid Supabase login.
//   - Supabase's own auth.users. The site asks for magic links with
//     should_create_user: false, so an address Supabase has never heard of gets
//     no email at all.
//
// This does both. It exists rather than being a note in the README because the
// obvious way -- re-running the importer with new -dad-email and -mom-email --
// quietly does the wrong thing: users are keyed on email, so a new address
// inserts a new row and leaves every question still pointing at the old one.
// Dad would end up with a working login and no questions.
//
//	go run ./cmd/user -name Dad -email dad@theirdomain.com -create-supabase
//
// It also sets who runs a line, which is held on their membership of it and is
// otherwise only settable when the person is first created:
//
//	go run ./cmd/user -name Christina -email christina@theirdomain.com \
//	  -family lucero -role admin
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grimechristopher/family-history-site/internal/store"
)

func main() {
	name := flag.String("name", "", "display name of the person, as shown on the site: Dad, Mom, Chris")
	email := flag.String("email", "", "the address they will sign in with")
	role := flag.String("role", "contributor", "contributor or admin; given explicitly, it also changes an existing person's")
	databaseURL := flag.String("database-url", os.Getenv("DATABASE_URL"), "postgres connection string")
	familySlug := flag.String("family", "home", "slug of the family they belong to")
	createSupabase := flag.Bool("create-supabase", false,
		"also create the Supabase account, pre-confirmed, so magic links work immediately")
	flag.Parse()

	// Only when it was actually typed. The default is "contributor", and applying
	// it to everybody the tool touches would quietly demote an admin the next time
	// somebody changed their email address.
	roleGiven := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "role" {
			roleGiven = true
		}
	})

	if *name == "" || *email == "" {
		flag.Usage()
		log.Fatal("both -name and -email are required")
	}
	addr := strings.ToLower(strings.TrimSpace(*email))
	if !strings.Contains(addr, "@") {
		log.Fatalf("%q does not look like an email address", *email)
	}
	if *role != store.RoleContributor && *role != store.RoleAdmin {
		log.Fatalf("role %q is neither %s nor %s", *role, store.RoleContributor, store.RoleAdmin)
	}
	if *databaseURL == "" {
		log.Fatal("set -database-url or DATABASE_URL")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *databaseURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	s := store.New(pool)

	existing, err := s.UserByDisplayName(ctx, *name)
	switch {
	case errors.Is(err, store.ErrNotFound):
		id, err := store.UpsertUser(ctx, pool, addr, *name)
		if err != nil {
			log.Fatalf("create %s: %v", *name, err)
		}
		fam, err := s.FamilyBySlug(ctx, *familySlug)
		if err != nil {
			log.Fatalf("family %q: %v", *familySlug, err)
		}
		if err := store.AddMemberTx(ctx, pool, fam.ID, id, *role); err != nil {
			log.Fatalf("add %s to %s: %v", *name, *familySlug, err)
		}
		fmt.Printf("created %s (%s) as %s in %s, id %d\n", *name, addr, *role, *familySlug, id)
	case err != nil:
		log.Fatalf("look up %s: %v", *name, err)
	default:
		if existing.Email == addr {
			fmt.Printf("%s already signs in with %s\n", *name, addr)
		} else {
			// Moves the address onto the row that already owns their questions,
			// which is the whole point of doing it here instead of by re-import.
			if err := s.SetUserEmail(ctx, existing.ID, addr); err != nil {
				log.Fatalf("set email for %s: %v", *name, err)
			}
			fmt.Printf("%s now signs in with %s, was %s\n", *name, addr, existing.Email)
			fmt.Println("  cleared the stored Supabase identity, so it binds again on their next sign-in")
		}
		// A role is held in a family, not on the person: somebody may run one line
		// and simply be asked questions in another. AddMemberTx writes the role on
		// the membership that is already there.
		if roleGiven {
			fam, err := s.FamilyBySlug(ctx, *familySlug)
			if err != nil {
				log.Fatalf("family %q: %v", *familySlug, err)
			}
			if err := store.AddMemberTx(ctx, pool, fam.ID, existing.ID, *role); err != nil {
				log.Fatalf("set role for %s: %v", *name, err)
			}
			fmt.Printf("%s is now %s of %s\n", *name, *role, *familySlug)
		}
	}

	if *createSupabase {
		if err := ensureSupabaseAccount(ctx, addr); err != nil {
			log.Fatalf("supabase: %v", err)
		}
	} else {
		fmt.Println("\nSupabase account not touched. Without one, no link is ever emailed to this")
		fmt.Println("address: re-run with -create-supabase, or invite them from the Supabase dashboard.")
	}
}

// ensureSupabaseAccount creates the account already confirmed. An unconfirmed
// account cannot be sent a magic link, and confirming it needs an email the
// person has to receive first, which is the chicken and egg this avoids.
func ensureSupabaseAccount(ctx context.Context, email string) error {
	base := strings.TrimSuffix(os.Getenv("SUPABASE_URL"), "/")
	key := os.Getenv("SUPABASE_SERVICE_ROLE_KEY")
	if base == "" || key == "" {
		return errors.New("SUPABASE_URL and SUPABASE_SERVICE_ROLE_KEY must be set for -create-supabase")
	}

	body, err := json.Marshal(map[string]any{"email": email, "email_confirm": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/auth/v1/admin/users",
		bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", key)
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("reach supabase: %w", err)
	}
	defer resp.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		fmt.Printf("created the Supabase account for %s, already confirmed\n", email)
		return nil
	case resp.StatusCode == http.StatusUnprocessableEntity &&
		bytes.Contains(bytes.ToLower(detail), []byte("already")):
		// Idempotent on purpose: this is meant to be safe to re-run.
		fmt.Printf("Supabase already has an account for %s\n", email)
		return nil
	default:
		return fmt.Errorf("supabase returned %s: %s", resp.Status, bytes.TrimSpace(detail))
	}
}
