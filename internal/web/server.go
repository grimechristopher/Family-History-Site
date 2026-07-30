// Package web serves the site: routes, templates, and handlers.
package web

import (
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/grimechristopher/family-history-site/assets"
	"github.com/grimechristopher/family-history-site/internal/auth"
	"github.com/grimechristopher/family-history-site/internal/config"
	"github.com/grimechristopher/family-history-site/internal/storage"
	"github.com/grimechristopher/family-history-site/internal/store"
)

// Server holds everything the handlers need.
type Server struct {
	Config   config.Config
	Store    *store.Store
	Sessions *auth.Sessions
	Supabase *auth.Supabase
	Storage  *storage.Client
	Log      *slog.Logger

	templates    map[string]*template.Template
	assetVersion string
}

func New(cfg config.Config, s *store.Store, log *slog.Logger, assetVersion string) (*Server, error) {
	srv := &Server{
		Config:   cfg,
		Store:    s,
		Sessions: &auth.Sessions{Store: s, Secure: strings.HasPrefix(cfg.BaseURL, "https://")},
		Supabase: auth.NewSupabase(cfg.SupabaseURL, cfg.SupabaseAnonKey),
		Storage:  storage.New(cfg.SupabaseURL, cfg.SupabaseServiceKey),
		Log:      log,

		assetVersion: assetVersion,
	}
	if err := srv.parseTemplates(); err != nil {
		return nil, err
	}
	return srv, nil
}

// parseTemplates builds one template set per page. Parsing per page rather than
// all at once means every page can define "content" without colliding.
func (s *Server) parseTemplates() error {
	pages := []string{"home", "login", "denied", "callback", "cards",
		"questions", "question", "stories", "subjects", "subject", "tree", "families", "people"}
	s.templates = make(map[string]*template.Template, len(pages))

	for _, name := range pages {
		t, err := template.New(name).Funcs(templateFuncs()).ParseFS(assets.FS,
			"templates/layout.html",
			"templates/partials/*.html",
			"templates/pages/"+name+".html",
		)
		if err != nil {
			return fmt.Errorf("parse template %s: %w", name, err)
		}
		s.templates[name] = t
	}
	return nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		// Family-relative link: {{fam $.FamilySlug "/questions"}} -> /f/home/questions.
		// Every in-site link goes through this, so nothing can accidentally point
		// at another family's page or drop the prefix.
		"fam": func(slug, path string) string { return "/f/" + slug + path },

		// A question row needs the family for its link, and a partial receives only
		// the item. Embedding the item means every other field in the template --
		// .Body, .SubjectName, .ReplyCount -- goes on resolving unchanged.
		"qrow": func(slug string, item store.QuestionListItem) qrowView {
			return qrowView{QuestionListItem: item, FamilySlug: slug}
		},
	}
}

type qrowView struct {
	store.QuestionListItem
	FamilySlug string
}

// pageData is the shape every template receives.
type pageData struct {
	Title        string
	Nav          string // which tab is current
	AssetVersion string
	User         *store.User
	Progress     *store.Progress
	Flash        string

	// login
	Email   string
	Sent    bool
	Expired bool
	Error   string
	// Notice is something worth saying that is not a failure, so it is styled
	// quietly. Asking for a second link reads as an error otherwise.
	Notice string

	// home
	Greeting string

	// cards
	Cards     []store.Card
	Mode      string
	Subjects  []store.Subject
	FocusSlug string
	FocusName string
	Focused   bool

	// questions list
	Unanswered []store.QuestionListItem
	Answered   []store.QuestionListItem
	// Groups is the section currently being shown, and Show says which.
	Groups          []store.QuestionGroup
	Show            string
	Counts          *store.ListCounts
	SubjectProgress []store.SubjectProgress
	Contributors    []*store.User
	FilterSubject   string
	FilterAskedOf   string
	ViewerIsAdmin   bool
	// NothingMatches distinguishes an empty filter from having answered
	// everything. Congratulating somebody for an empty result is misleading.
	NothingMatches bool

	// question detail
	Question        *store.QuestionDetail
	PrimaryAnswers  []answerView
	OtherAnswers    []answerView
	MyAnswerBody    string
	MyAnswerIsDraft bool
	ViewerIsAskedOf bool

	// stories
	Stories []storyView

	// photos
	PhotosEnabled bool

	// families
	Families   []store.Family
	FamilySlug string
	Members2   []store.Member
	TreePeople []store.TreePerson
	Added      string

	// tree and subject pages
	Tree    []*treeNode
	Subject *store.SubjectProgress
	Members []store.TreePerson
}

func (s *Server) newPageData(r *http.Request, title string) pageData {
	d := pageData{
		Title:         title,
		AssetVersion:  s.assetVersion,
		User:          auth.User(r.Context()),
		PhotosEnabled: s.Storage.Configured(),
	}
	if f := FamilyOf(r.Context()); f != nil {
		d.FamilySlug = f.Slug
	}
	return d
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, page string, data pageData) {
	s.renderNamed(w, r, page, "layout", data)
}

// renderNamed renders a specific template, which is how htmx gets a fragment
// rather than a whole document.
func (s *Server) renderNamed(w http.ResponseWriter, r *http.Request, page, name string, data pageData) {
	t, ok := s.templates[page]
	if !ok {
		s.serverError(w, r, fmt.Errorf("unknown template %q", page))
		return
	}

	// Render to memory first: a template error midway through a direct write
	// would emit a half-built page with a 200 already sent.
	var buf strings.Builder
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		s.serverError(w, r, fmt.Errorf("render %s/%s: %w", page, name, err))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, buf.String())
}

func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusInternalServerError)
	io.WriteString(w, `<!doctype html><meta charset="utf-8">`+
		`<title>Something went wrong</title>`+
		`<link rel="stylesheet" href="/static/css/app.css">`+
		`<main class="page"><h1>Something went wrong on our end</h1>`+
		`<p class="lede">Nothing you wrote has been lost. Try again in a moment.</p></main>`)
}

// Routes returns the site's handler.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	static, err := fs.Sub(assets.FS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/",
		cacheStatic(http.FileServer(http.FS(static)))))

	mux.Handle("GET /healthz", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := s.Store.Pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unreachable", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "ok")
	}))

	// Auth is deliberately reachable without a session.
	mux.Handle("GET /login", s.Sessions.Optional(http.HandlerFunc(s.handleLoginForm)))
	mux.Handle("POST /login", http.HandlerFunc(s.handleLoginSubmit))
	mux.Handle("GET /auth/callback", http.HandlerFunc(s.handleCallback))
	mux.Handle("POST /auth/session", http.HandlerFunc(s.handleSession))
	mux.Handle("POST /login/code", http.HandlerFunc(s.handleCodeSubmit))
	mux.Handle("POST /logout", http.HandlerFunc(s.handleLogout))

	// Signs in as a contributor with no magic link, for checking the site as Mom or
	// Dad. Registered only when DEV_LOGIN=1, so in production the route does not
	// exist at all rather than existing and refusing.
	if s.Config.DevLogin {
		s.Log.Warn("DEV_LOGIN is on: /dev/login/{name} will sign in as any contributor without a link")
		mux.Handle("GET /dev/login/{name}", http.HandlerFunc(s.handleDevLogin))
		mux.Handle("GET /dev/login/{family}/{name}", http.HandlerFunc(s.handleDevLogin))
	}

	require := s.Sessions.Require
	// Signed in, and a member of the family named in the path. Order matters:
	// membership is a question about a user, so the session is resolved first.
	family := func(h http.HandlerFunc) http.Handler { return require(s.inFamily(h)) }

	// The root belongs to no family: it sends you to yours.
	mux.Handle("GET /{$}", require(http.HandlerFunc(s.handleRoot)))
	mux.Handle("GET /f/{family}/{$}", family(s.handleHome))
	mux.Handle("GET /f/{family}/cards", family(s.handleCards))
	mux.Handle("POST /f/{family}/cards/mode", family(s.handleSetMode))
	mux.Handle("POST /f/{family}/cards/{id}/defer", family(s.handleDefer))
	mux.Handle("POST /f/{family}/cards/{id}/answer", family(s.handleAnswer))
	mux.Handle("POST /f/{family}/cards/{id}/draft", family(s.handleDraft))

	mux.Handle("GET /f/{family}/questions", family(s.handleQuestions))
	mux.Handle("GET /f/{family}/questions/{id}", family(s.handleQuestion))
	mux.Handle("POST /f/{family}/questions/{id}/answer", family(s.handleQuestionAnswer))
	mux.Handle("POST /f/{family}/entries/{id}/replies", family(s.handleReply))

	mux.Handle("GET /f/{family}/stories", family(s.handleStories))
	mux.Handle("POST /f/{family}/stories", family(s.handleCreateStory))
	mux.Handle("POST /f/{family}/stories/{id}/delete", family(s.handleDeleteStory))

	mux.Handle("POST /f/{family}/entries/{id}/photos", family(s.handleUploadPhoto))
	mux.Handle("POST /f/{family}/photos/{id}/delete", family(s.handleDeletePhoto))

	mux.Handle("GET /f/{family}/tree", family(s.handleTree))
	mux.Handle("GET /f/{family}/tree.json", family(s.handleTreeJSON))
	mux.Handle("GET /f/{family}/subjects", family(s.handleSubjects))
	mux.Handle("GET /f/{family}/subjects/{slug}", family(s.handleSubject))
	mux.Handle("POST /f/{family}/subjects/{slug}/focus", family(s.handleFocusSubject))
	mux.Handle("POST /f/{family}/subjects/{slug}/questions", family(s.handleAskQuestion))

	mux.Handle("GET /f/{family}/people", family(s.handlePeople))
	mux.Handle("POST /f/{family}/people", family(s.handleAddPerson))

	return s.securityHeaders(mux)
}

func questionID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Assets are versioned by query string, so they can be cached hard.
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "same-origin")
		// Scripts and styles come only from this origin. Images additionally come
		// from Supabase Storage, because signed photo URLs point there.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data: "+s.Config.SupabaseURL+"; "+
				"style-src 'self'; script-src 'self'; connect-src 'self'; "+
				"form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
