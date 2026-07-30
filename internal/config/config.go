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

	// Who to ask when something is wrong, shown in the footer to people who are
	// signed in. Out of the code because it is a personal address and a personal
	// telephone number, and this repository is public.
	SupportEmail string
	SupportPhone string

	// DevLogin turns on a route that signs in as any contributor without a magic
	// link, so the site can be checked as Mom or Dad. It is an authentication
	// bypass and exists only because it is off unless DEV_LOGIN is set to 1 in the
	// environment, which production never does. The server logs a warning on every
	// start while it is on.
	DevLogin bool
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
		SupportEmail:       get("SUPPORT_EMAIL"),
		SupportPhone:       get("SUPPORT_PHONE"),
		DevLogin:           get("DEV_LOGIN") == "1",
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

	// Only these are genuinely required. SUPABASE_JWT_SECRET is optional: without
	// it, tokens are verified by asking Supabase instead of checking the
	// signature locally. SUPABASE_SERVICE_ROLE_KEY is needed only for photos, and
	// SUPABASE_JWT_ISSUER only once the deployment's real issuer is known.
	required := map[string]string{
		"DATABASE_URL":      c.DatabaseURL,
		"SUPABASE_URL":      c.SupabaseURL,
		"SUPABASE_ANON_KEY": c.SupabaseAnonKey,
		"BASE_URL":          c.BaseURL,
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
