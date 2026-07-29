package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExtensionForAcceptsWhatIPadsProduce(t *testing.T) {
	// HEIC matters: it is what an iPad shoots by default.
	for _, ct := range []string{"image/jpeg", "image/png", "image/webp", "image/gif", "image/heic", "image/heif"} {
		if _, err := ExtensionFor(ct); err != nil {
			t.Errorf("%s should be accepted: %v", ct, err)
		}
	}
	if ext, _ := ExtensionFor("image/jpeg; charset=binary"); ext != ".jpg" {
		t.Errorf("parameters should be ignored, got %q", ext)
	}
	if ext, _ := ExtensionFor("  IMAGE/PNG  "); ext != ".png" {
		t.Errorf("case and spacing should be tolerated, got %q", ext)
	}
}

func TestExtensionForRefusesAnythingElse(t *testing.T) {
	for _, ct := range []string{
		"application/pdf",
		"text/html",
		"image/svg+xml", // scriptable, so not a picture we want to serve back
		"application/octet-stream",
		"",
	} {
		if _, err := ExtensionFor(ct); err == nil {
			t.Errorf("%q must be refused", ct)
		}
	}
}

func TestNotConfiguredWithoutServiceKey(t *testing.T) {
	c := New("https://supabase.example.com", "")
	if c.Configured() {
		t.Error("should not be configured without a service key")
	}
	// Every operation must fail clearly rather than panicking or hanging.
	ctx := context.Background()
	if err := c.Upload(ctx, "b", "p", "image/png", strings.NewReader("x")); err == nil {
		t.Error("Upload should fail when unconfigured")
	}
	if _, err := c.SignedURL(ctx, "b", "p", time.Minute); err == nil {
		t.Error("SignedURL should fail when unconfigured")
	}
	if err := c.Delete(ctx, "b", "p"); err == nil {
		t.Error("Delete should fail when unconfigured")
	}
}

// A path must stay inside its prefix: no climbing out with "..".
func TestEscapeObjectPathCannotTraverse(t *testing.T) {
	cases := map[string]string{
		"entries/12/abc.jpg":            "entries/12/abc.jpg",
		"../../etc/passwd":              "etc/passwd",
		"entries/12/../../../secret":    "secret",
		"entries/12/a file with spaces": "entries/12/a%20file%20with%20spaces",
	}
	for in, want := range cases {
		if got := escapeObjectPath(in); got != want {
			t.Errorf("escapeObjectPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUploadSendsServiceKeyAndRefusesOverwrite(t *testing.T) {
	var gotAuth, gotAPIKey, gotUpsert, gotPath, gotType, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAPIKey = r.Header.Get("apikey")
		gotUpsert = r.Header.Get("x-upsert")
		gotPath = r.URL.Path
		gotType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "service-key")
	err := c.Upload(context.Background(), "family-questions-photos",
		"entries/7/tok.jpg", "image/jpeg", strings.NewReader("JPEGDATA"))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if gotAuth != "Bearer service-key" || gotAPIKey != "service-key" {
		t.Errorf("auth headers = %q / %q", gotAuth, gotAPIKey)
	}
	// Silently replacing an existing photograph would lose the original.
	if gotUpsert != "false" {
		t.Errorf("x-upsert = %q, want false", gotUpsert)
	}
	if gotPath != "/storage/v1/object/family-questions-photos/entries/7/tok.jpg" {
		t.Errorf("path = %q", gotPath)
	}
	if gotType != "image/jpeg" || gotBody != "JPEGDATA" {
		t.Errorf("content type = %q, body = %q", gotType, gotBody)
	}
}

func TestUploadReportsServerErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"Duplicate"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "key")
	err := c.Upload(context.Background(), "b", "p.jpg", "image/jpeg", strings.NewReader("x"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Duplicate") {
		t.Errorf("error should carry the server's detail, got: %v", err)
	}
}

func TestSignedURLResolvesRelativeAndAbsoluteForms(t *testing.T) {
	var gotBody string
	relative := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		io.WriteString(w, `{"signedURL":"/object/sign/bucket/p.jpg?token=abc"}`)
	}))
	defer relative.Close()

	c := New(relative.URL, "key")
	got, err := c.SignedURL(context.Background(), "bucket", "p.jpg", 90*time.Second)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	want := relative.URL + "/storage/v1/object/sign/bucket/p.jpg?token=abc"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if !strings.Contains(gotBody, `"expiresIn":90`) {
		t.Errorf("expiry not passed through: %s", gotBody)
	}

	absolute := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"signedURL":"https://cdn.example.com/x?token=abc"}`)
	}))
	defer absolute.Close()

	c = New(absolute.URL, "key")
	got, err = c.SignedURL(context.Background(), "bucket", "p.jpg", time.Minute)
	if err != nil {
		t.Fatalf("SignedURL: %v", err)
	}
	if got != "https://cdn.example.com/x?token=abc" {
		t.Errorf("absolute url should pass through, got %q", got)
	}
}

func TestSignedURLRejectsEmptyResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"signedURL":""}`)
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "key").SignedURL(context.Background(), "b", "p", time.Minute); err == nil {
		t.Error("an empty signed url must be an error, not a broken image")
	}
}

func TestDelete(t *testing.T) {
	var method string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := New(srv.URL, "key").Delete(context.Background(), "b", "p.jpg"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if method != http.MethodDelete {
		t.Errorf("method = %s", method)
	}
}

func TestObjectPath(t *testing.T) {
	if got := ObjectPath(42, "abc123", ".jpg"); got != "entries/42/abc123.jpg" {
		t.Errorf("ObjectPath = %q", got)
	}
}
