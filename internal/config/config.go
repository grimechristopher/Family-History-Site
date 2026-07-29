// Package config loads runtime configuration from the environment.
package config

import (
	"fmt"
	"sort"
	"strings"
)

type Config struct {
	DatabaseURL        string
	SupabaseURL        string
	SupabaseAnonKey    string
	SupabaseServiceKey string
	SupabaseJWTSecret  string
	SupabaseJWTIssuer  string
	BaseURL            string
	Addr               string

	// Storage buckets, created by hand in Supabase.
	PhotoBucket string
	AudioBucket string
}

// Load reads configuration using the supplied lookup function, which is
// os.Getenv in production and a map in tests.
func Load(get func(string) string) (Config, error) {
	c := Config{
		DatabaseURL:        get("DATABASE_URL"),
		SupabaseURL:        strings.TrimSuffix(get("SUPABASE_URL"), "/"),
		SupabaseAnonKey:    get("SUPABASE_ANON_KEY"),
		SupabaseServiceKey: get("SUPABASE_SERVICE_ROLE_KEY"),
		SupabaseJWTSecret:  get("SUPABASE_JWT_SECRET"),
		SupabaseJWTIssuer:  get("SUPABASE_JWT_ISSUER"),
		BaseURL:            strings.TrimSuffix(get("BASE_URL"), "/"),
		Addr:               get("ADDR"),
		PhotoBucket:        get("SUPABASE_PHOTO_BUCKET"),
		AudioBucket:        get("SUPABASE_AUDIO_BUCKET"),
	}
	if c.PhotoBucket == "" {
		c.PhotoBucket = "family-questions-photos"
	}
	if c.AudioBucket == "" {
		c.AudioBucket = "family-questions-audio"
	}
	if c.Addr == "" {
		c.Addr = ":8080"
	}

	// SUPABASE_SERVICE_ROLE_KEY is needed only for photo uploads (phase 3) and
	// SUPABASE_JWT_ISSUER only once the deployment's real issuer is known, so
	// neither is required here.
	required := map[string]string{
		"DATABASE_URL":        c.DatabaseURL,
		"SUPABASE_URL":        c.SupabaseURL,
		"SUPABASE_ANON_KEY":   c.SupabaseAnonKey,
		"SUPABASE_JWT_SECRET": c.SupabaseJWTSecret,
		"BASE_URL":            c.BaseURL,
	}
	var missing []string
	for name, value := range required {
		if value == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return Config{}, fmt.Errorf("missing required environment variables: %s",
			strings.Join(missing, ", "))
	}
	return c, nil
}
