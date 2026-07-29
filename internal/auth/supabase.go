package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
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

// UserFromToken asks Supabase to verify an access token and describe its owner.
//
// This is the alternative to holding Supabase's signing secret. It costs one
// HTTP call, but only at sign-in — this site then mints its own 90-day session —
// and it has two advantages worth the round trip: the signing secret never needs
// to be copied out of the Supabase instance, and it keeps working unchanged if
// Supabase later moves to asymmetric keys, which local HS256 verification would
// not.
func (s *Supabase) UserFromToken(ctx context.Context, accessToken string) (Claims, error) {
	if accessToken == "" {
		return Claims{}, errors.New("no access token")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BaseURL+"/auth/v1/user", nil)
	if err != nil {
		return Claims{}, err
	}
	req.Header.Set("apikey", s.AnonKey)
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := s.Client.Do(req)
	if err != nil {
		return Claims{}, fmt.Errorf("reach supabase to verify token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return Claims{}, fmt.Errorf("supabase rejected the token with %s: %s",
			resp.Status, bytes.TrimSpace(detail))
	}

	var body struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Claims{}, fmt.Errorf("decode supabase user: %w", err)
	}

	// Same requirements as local verification. The anon and service_role keys
	// have no user attached, so they cannot reach this point with an id, but the
	// checks are stated rather than assumed.
	if body.Role != "authenticated" {
		return Claims{}, fmt.Errorf("supabase reports role %q, want authenticated", body.Role)
	}
	if !isUUID(body.ID) {
		return Claims{}, fmt.Errorf("supabase returned a non-UUID id %q", body.ID)
	}
	if body.Email == "" {
		return Claims{}, errors.New("supabase returned no email; the allowlist needs one")
	}

	return Claims{
		Subject: body.ID,
		Email:   strings.ToLower(strings.TrimSpace(body.Email)),
	}, nil
}
