package config

import (
	"strings"
	"testing"
)

func TestLoadRequiresDatabaseURL(t *testing.T) {
	_, err := Load(func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error when DATABASE_URL is unset")
	}
}

func TestLoadReadsValues(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":              "postgres://localhost/test",
		"SUPABASE_URL":              "https://supabase.example.com/",
		"SUPABASE_ANON_KEY":         "anon",
		"SUPABASE_SERVICE_ROLE_KEY": "service",
		"SUPABASE_JWT_SECRET":       "secret",
		"BASE_URL":                  "https://family.example.com/",
	}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DatabaseURL != "postgres://localhost/test" {
		t.Errorf("DatabaseURL = %q", c.DatabaseURL)
	}
	// Trailing slashes are stripped so URLs can be concatenated safely.
	if c.SupabaseURL != "https://supabase.example.com" {
		t.Errorf("SupabaseURL = %q", c.SupabaseURL)
	}
	if c.BaseURL != "https://family.example.com" {
		t.Errorf("BaseURL = %q", c.BaseURL)
	}
	if c.Addr != ":8080" {
		t.Errorf("Addr = %q, want default :8080", c.Addr)
	}
}

func TestLoadNamesEveryMissingVariable(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL": "postgres://localhost/test",
	}
	_, err := Load(func(k string) string { return env[k] })
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"SUPABASE_URL", "SUPABASE_ANON_KEY", "BASE_URL"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s, got: %v", want, err)
		}
	}
}

// SUPABASE_SERVICE_ROLE_KEY is only needed for photo uploads in phase 3.
func TestLoadDoesNotRequireServiceKey(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/test",
		"SUPABASE_URL":        "https://supabase.example.com",
		"SUPABASE_ANON_KEY":   "anon",
		"SUPABASE_JWT_SECRET": "secret",
		"BASE_URL":            "https://family.example.com",
	}
	if _, err := Load(func(k string) string { return env[k] }); err != nil {
		t.Errorf("service role key should be optional in phase 1: %v", err)
	}
}

// The buckets already exist in Supabase, so sensible defaults avoid a deploy
// failing on a variable nobody remembered to set.
func TestBucketsDefaultToTheOnesThatExist(t *testing.T) {
	env := map[string]string{
		"DATABASE_URL":        "postgres://localhost/test",
		"SUPABASE_URL":        "https://supabase.example.com",
		"SUPABASE_ANON_KEY":   "anon",
		"SUPABASE_JWT_SECRET": "secret",
		"BASE_URL":            "https://family.example.com",
	}
	c, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.PhotoBucket != "family-questions-photos" {
		t.Errorf("PhotoBucket = %q", c.PhotoBucket)
	}
	if c.AudioBucket != "family-questions-audio" {
		t.Errorf("AudioBucket = %q", c.AudioBucket)
	}

	env["SUPABASE_PHOTO_BUCKET"] = "somewhere-else"
	c, _ = Load(func(k string) string { return env[k] })
	if c.PhotoBucket != "somewhere-else" {
		t.Errorf("an explicit bucket should win, got %q", c.PhotoBucket)
	}
}
