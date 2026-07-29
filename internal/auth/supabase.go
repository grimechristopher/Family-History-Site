package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Supabase sends magic-link emails through the existing self-hosted GoTrue.
type Supabase struct {
	BaseURL string // https://supabase.example.com
	AnonKey string
	Client  *http.Client
}

func NewSupabase(baseURL, anonKey string) *Supabase {
	return &Supabase{
		BaseURL: baseURL,
		AnonKey: anonKey,
		Client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// SendMagicLink asks Supabase to email a sign-in link.
//
// shouldCreateUser is false: accounts are seeded by hand, and letting this
// endpoint create them would mean an unknown address could provoke an email from
// a family-only site. An address with no allowlist row is rejected before this is
// ever called.
func (s *Supabase) SendMagicLink(ctx context.Context, email, redirectTo string) error {
	body, err := json.Marshal(map[string]any{
		"email":              email,
		"should_create_user": false,
	})
	if err != nil {
		return err
	}

	endpoint := s.BaseURL + "/auth/v1/otp?redirect_to=" + url.QueryEscape(redirectTo)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.AnonKey)
	req.Header.Set("Authorization", "Bearer "+s.AnonKey)

	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("reach supabase: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("supabase returned %s: %s", resp.Status, bytes.TrimSpace(detail))
}
