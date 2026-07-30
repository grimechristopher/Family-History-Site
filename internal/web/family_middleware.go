package web

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

type familyCtxKey struct{}
type membershipCtxKey struct{}

// FamilyOf returns the family the current request is scoped to, or nil outside a
// family-scoped route.
func FamilyOf(ctx context.Context) *store.Family {
	f, _ := ctx.Value(familyCtxKey{}).(*store.Family)
	return f
}

// MembershipOf returns what the signed-in person is within this family.
func MembershipOf(ctx context.Context) *store.Membership {
	m, _ := ctx.Value(membershipCtxKey{}).(*store.Membership)
	return m
}

// inFamily resolves {family} from the path, refuses anyone who is not a member,
// and runs the handler inside a transaction that has app.family_id set.
//
// The whole request is one transaction because SET LOCAL lasts exactly that long,
// which is what makes the setting safe on a connection shared with every other
// request. Row-level security then filters every read and refuses every write that
// names another family, so a handler which forgets to scope itself returns nothing
// rather than somebody else's family.
func (s *Server) inFamily(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.User(r.Context())
		if u == nil {
			// Unreachable: every family route is wrapped in Require first, which
			// sends a signed-out visitor to the login page. Guarded anyway, and
			// as a 404 rather than a redirect, so a mistake in the route table
			// cannot turn into an unauthenticated read.
			http.NotFound(w, r)
			return
		}

		fam, err := s.Store.FamilyBySlug(r.Context(), r.PathValue("family"))
		if errors.Is(err, store.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}

		// Not a member is 404 rather than 403. A 403 confirms the family exists,
		// which tells a stranger something true about a private site.
		member, err := s.Store.MembershipOf(r.Context(), fam.ID, u.ID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				http.NotFound(w, r)
				return
			}
			s.serverError(w, r, err)
			return
		}

		// The person as they are in this family: their role, which person in the
		// tree they are, and where they are in the card stack all differ per
		// family, so the handlers see a copy carrying this family's answers.
		here := *u
		here.Role = member.Role
		here.PersonID = member.PersonID
		here.QueueMode = member.QueueMode
		here.QueueSeed = member.QueueSeed
		here.QueueFocusSubjectID = member.QueueFocusSubjectID
		here.DigestEnabled = member.DigestEnabled

		err = pgx.BeginFunc(r.Context(), s.Store.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(r.Context(),
				"SELECT set_config('app.family_id', $1, true)",
				strconv.FormatInt(fam.ID, 10)); err != nil {
				return err
			}

			ctx := store.WithTx(r.Context(), tx)
			ctx = store.WithFamily(ctx, fam.ID)
			ctx = context.WithValue(ctx, familyCtxKey{}, fam)
			ctx = context.WithValue(ctx, membershipCtxKey{}, member)
			ctx = auth.WithUser(ctx, &here)

			// So the root sends them back here next time instead of asking.
			rememberFamily(w, fam.Slug, s.Sessions.Secure)

			next.ServeHTTP(w, r.WithContext(ctx))
			return nil
		})
		if err != nil {
			s.serverError(w, r, err)
		}
	})
}

// famPath builds a link inside the request's family. Handlers use it for every
// redirect, so a path can never be emitted without its family and land on the
// chooser -- or, worse, on the same page in somebody else's family.
func famPath(ctx context.Context, path string) string {
	f := FamilyOf(ctx)
	if f == nil {
		return path
	}
	return "/f/" + f.Slug + path
}

// famSlug is the slug of the request's family, for views that carry it to a
// partial.
func famSlug(ctx context.Context) string {
	if f := FamilyOf(ctx); f != nil {
		return f.Slug
	}
	return ""
}
