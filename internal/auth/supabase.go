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
	if resp.StatusCode == http.StatusTooManyRequests {
		return ErrRateLimited
	}
	return fmt.Errorf("supabase returned %s: %s", resp.Status, bytes.TrimSpace(detail))
}

// ErrRateLimited means Supabase declined to send another email to this address so
// soon. It is reported separately because it almost always means one was just
// sent: the right response is to point at the inbox, not to show a failure.
var ErrRateLimited = errors.New("an email was sent to this address very recently")

// ErrBadCode means the six-digit code was wrong, already used, or has expired.
// Supabase does not distinguish between those, and neither should the message
// shown: all three are fixed by asking for another one.
var ErrBadCode = errors.New("that code is not valid")

// VerifyEmailOTP exchanges the code from the sign-in email for an access token.
//
// This is the same email the link is in -- Supabase puts both in it -- and the
// same one-time token behind both, so using either consumes the other. It exists
// as a second route because the link depends on the redirect URL being in
// Supabase's allow list, and the code depends on nothing: it is typed into this
// site and exchanged server-side, so it works from any device, on any host, with
// no JavaScript.
func (s *Supabase) VerifyEmailOTP(ctx context.Context, email, code string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"type":  "email",
		"email": email,
		"token": code,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+"/auth/v1/verify",
		bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", s.AnonKey)

	resp, err := s.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("reach supabase: %w", err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}

	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusBadRequest ||
		resp.StatusCode == http.StatusUnauthorized {
		return "", ErrBadCode
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("supabase returned %s: %s", resp.Status, bytes.TrimSpace(payload))
	}

	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(payload, &out); err != nil {
		return "", fmt.Errorf("decode supabase response: %w", err)
	}
	if out.AccessToken == "" {
		return "", ErrBadCode
	}
	return out.AccessToken, nil
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

// EnsureAccount creates the Supabase account for an address, already confirmed.
//
// Nothing is emailed until this exists: the site asks for magic links with
// should_create_user false, so an address Supabase has never seen gets no mail at
// all and the person waits for a link that was never sent. Confirming normally
// needs an email they would have to receive first, which is the chicken and egg
// email_confirm avoids.
//
// Idempotent: an address Supabase already knows is reported as success, because
// the caller wants the account to exist, not to have created it.
func (s *Supabase) EnsureAccount(ctx context.Context, email, serviceKey string) error {
	if serviceKey == "" {
		return errors.New("SUPABASE_SERVICE_ROLE_KEY is unset, so accounts cannot be created")
	}

	body, err := json.Marshal(map[string]any{"email": email, "email_confirm": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		s.BaseURL+"/auth/v1/admin/users", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("apikey", serviceKey)
	req.Header.Set("Authorization", "Bearer "+serviceKey)

	resp, err := s.Client.Do(req)
	if err != nil {
		return fmt.Errorf("reach supabase: %w", err)
	}
	defer resp.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnprocessableEntity &&
		bytes.Contains(bytes.ToLower(detail), []byte("already")):
		return nil
	default:
		return fmt.Errorf("supabase returned %s: %s", resp.Status, bytes.TrimSpace(detail))
	}
}
