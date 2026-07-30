// Command family creates a family and puts its first admin in it.
//
// Only the first one needs this: everybody after can be added from the site, by
// any member, on the family's people page. It exists because a family with nobody
// in it cannot be reached, and nobody can be added to a family that does not
// exist.
//
//	go run ./cmd/family -slug hale -name "The Hales" -admin chris@example.com
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grimechristopher/family-history-site/internal/migrate"
	"github.com/grimechristopher/family-history-site/internal/store"
)

func main() {
	slug := flag.String("slug", "", "short name used in the address bar, such as hale (required)")
	name := flag.String("name", "", "how the family is shown on screen (required)")
	admin := flag.String("admin", "", "email address of the first admin (required)")
	adminName := flag.String("admin-name", "", "how that person is named on the site (defaults to the address)")
	databaseURL := flag.String("database-url", os.Getenv("DATABASE_URL"), "postgres connection string")
	flag.Parse()

	if *slug == "" || *name == "" || *admin == "" {
		flag.Usage()
		log.Fatal("-slug, -name and -admin are all required")
	}
	if strings.ContainsAny(*slug, " /?&#") {
		log.Fatalf("%q cannot be a slug: it goes in the address bar", *slug)
	}
	if *databaseURL == "" {
		log.Fatal("set -database-url or DATABASE_URL")
	}
	email := strings.ToLower(strings.TrimSpace(*admin))
	label := *adminName
	if label == "" {
		label = email
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, *databaseURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	// This is the bootstrap command, so it may well be the first thing ever run
	// against the database. Migrating here means a fresh install is one command
	// rather than "start the server once, then create a family".
	if err := migrate.Run(ctx, pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	s := store.New(pool)

	// Idempotent: running it again reports the family rather than failing, so it is
	// safe to re-run against a database that has been partly set up.
	fam, err := s.FamilyBySlug(ctx, *slug)
	switch {
	case errors.Is(err, store.ErrNotFound):
		id, err := s.CreateFamily(ctx, *slug, *name)
		if err != nil {
			log.Fatalf("create family: %v", err)
		}
		fam = &store.Family{ID: id, Slug: *slug, DisplayName: *name}
		fmt.Printf("created %s (%s)\n", fam.DisplayName, fam.Slug)
	case err != nil:
		log.Fatalf("look up family: %v", err)
	default:
		fmt.Printf("%s already exists\n", fam.Slug)
	}

	uid, err := store.UpsertUser(ctx, pool, email, label)
	if err != nil {
		log.Fatalf("create admin: %v", err)
	}
	if err := s.AddMember(store.WithFamily(ctx, fam.ID), fam.ID, uid, store.RoleAdmin); err != nil {
		log.Fatalf("add admin: %v", err)
	}
	fmt.Printf("%s is an admin of %s\n", email, fam.Slug)
	fmt.Printf("\nNow import into it:\n  make import FAMILY=%s\n", fam.Slug)
}
