package web

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/store"
)

type familiesCtxKey struct{}

// FamiliesOf returns every family the person making this request belongs to.
func FamiliesOf(ctx context.Context) []store.Family {
	f, _ := ctx.Value(familiesCtxKey{}).([]store.Family)
	return f
}

// inFamilies runs the handler inside a transaction scoped to whichever families
// this person belongs to.
//
// The scope is a list rather than one family because being in several is normal --
// a married couple have two parents' lines each -- and making somebody switch
// between four half-empty pages is worse than one list they can filter.
//
// What the policies enforce is membership, which is the part that matters for
// safety: the setting is built from core.family_members and never from anything
// the browser sends, so a filter in the interface can only narrow what somebody was
// already entitled to. Frank cannot reach Violeta's line by editing a query string,
// because he is not a member of it.
//
// The whole request is one transaction because SET LOCAL lasts exactly that long,
// which is what makes the setting safe on a connection shared with everybody else.
func (s *Server) inFamilies(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.User(r.Context())
		if u == nil {
			// Unreachable: these routes are wrapped in Require first. Guarded as a
			// 404 anyway, so a mistake in the route table cannot become an
			// unauthenticated read.
			http.NotFound(w, r)
			return
		}

		families, err := s.Store.FamiliesOf(r.Context(), u.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}

		ids := make([]string, 0, len(families))
		for _, f := range families {
			ids = append(ids, strconv.FormatInt(f.ID, 10))
		}

		err = pgx.BeginFunc(r.Context(), s.Store.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(r.Context(),
				"SELECT set_config('app.family_ids', $1, true)",
				strings.Join(ids, ",")); err != nil {
				return err
			}

			ctx := store.WithTx(r.Context(), tx)
			ctx = context.WithValue(ctx, familiesCtxKey{}, families)
			// Queries against core have no row-level security of their own, so they
			// name the families explicitly and read them from here.
			ids64 := make([]int64, 0, len(families))
			for _, f := range families {
				ids64 = append(ids64, f.ID)
			}
			ctx = store.WithFamilies(ctx, ids64)

			// The person as they stand across their families: role, and where they
			// are in the card stack. Both live on membership, and the handlers read
			// them off the user.
			st, err := s.Store.StandingOf(ctx, u.ID)
			if err != nil {
				return err
			}
			here := *u
			here.Role = st.Role
			here.PersonID = st.PersonID
			here.QueueMode = st.QueueMode
			here.QueueSeed = st.QueueSeed
			here.QueueFocusSubjectID = st.QueueFocusSubjectID
			ctx = auth.WithUser(ctx, &here)

			next.ServeHTTP(w, r.WithContext(ctx))
			return nil
		})
		if err != nil {
			s.serverError(w, r, err)
		}
	})
}
