package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserFromTokenAcceptsARealUser(t *testing.T) {
	var gotAuth, gotAPIKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("apikey")
		gotPath = r.URL.Path
		io.WriteString(w, `{"id":"3f1c9a44-6b2e-4f7a-9c11-0d8e5b7a2c33",
		                    "email":"Dad@Example.com","role":"authenticated"}`)
	}))
	defer srv.Close()

	claims, err := NewSupabase(srv.URL, "anon-key").UserFromToken(context.Background(), "user-token")
	if err != nil {
		t.Fatalf("UserFromToken: %v", err)
	}
	if claims.Subject != "3f1c9a44-6b2e-4f7a-9c11-0d8e5b7a2c33" {
		t.Errorf("Subject = %q", claims.Subject)
	}
	// Lowercased, since the allowlist is matched on email.
	if claims.Email != "dad@example.com" {
		t.Errorf("Email = %q", claims.Email)
	}
	if gotPath != "/auth/v1/user" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer user-token" || gotAPIKey != "anon-key" {
		t.Errorf("headers = %q / %q", gotAuth, gotAPIKey)
	}
}

// Supabase refusing the token must be a clean failure, carrying its reason.
func TestUserFromTokenRejectsWhatSupabaseRejects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"msg":"invalid claim: missing sub claim"}`)
	}))
	defer srv.Close()

	_, err := NewSupabase(srv.URL, "anon").UserFromToken(context.Background(), "anon-key-as-a-login")
	if err == nil {
		t.Fatal("expected rejection")
	}
	if !strings.Contains(err.Error(), "missing sub claim") {
		t.Errorf("error should carry Supabase's reason, got: %v", err)
	}
}

// The same guards as local verification, in case a future Supabase returns a
// service token's details here.
func TestUserFromTokenEnforcesRoleUUIDAndEmail(t *testing.T) {
	cases := map[string]string{
		"non-authenticated role": `{"id":"3f1c9a44-6b2e-4f7a-9c11-0d8e5b7a2c33","email":"a@b.c","role":"service_role"}`,
		"non-UUID id":            `{"id":"not-a-uuid","email":"a@b.c","role":"authenticated"}`,
		"missing email":          `{"id":"3f1c9a44-6b2e-4f7a-9c11-0d8e5b7a2c33","email":"","role":"authenticated"}`,
		"empty body":             `{}`,
	}
	for name, body := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, body)
		}))
		if _, err := NewSupabase(srv.URL, "anon").UserFromToken(context.Background(), "tok"); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
		srv.Close()
	}
}

func TestUserFromTokenRequiresAToken(t *testing.T) {
	if _, err := NewSupabase("https://example.com", "anon").UserFromToken(context.Background(), ""); err == nil {
		t.Error("an empty token must be refused without a network call")
	}
}

func TestSendMagicLinkDoesNotCreateUsers(t *testing.T) {
	var body, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		query = r.URL.RawQuery
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := NewSupabase(srv.URL, "anon").SendMagicLink(context.Background(),
		"dad@example.com", "https://family.example.com/auth/callback")
	if err != nil {
		t.Fatalf("SendMagicLink: %v", err)
	}
	// A family-only site must not provoke account creation for a stranger.
	if !strings.Contains(body, `"should_create_user":false`) {
		t.Errorf("should_create_user must be false, got: %s", body)
	}
	if !strings.Contains(query, "redirect_to=") {
		t.Errorf("redirect_to must be passed, got: %s", query)
	}
}
