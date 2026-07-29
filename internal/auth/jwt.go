// Package auth verifies Supabase access tokens and gates access on this site's
// own allowlist.
//
// HS256 verification is written out rather than pulled from a JWT library: it is
// about sixty lines of standard library, and it keeps every security-critical
// check visible in one place.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Claims struct {
	Subject string // Supabase user UUID
	Email   string // lowercased, since the allowlist is matched on it
}

// VerifySupabaseJWT validates a Supabase access token.
//
// expectedIssuer is enforced only when non-empty: self-hosted GoTrue defaults to
// "supabase" while hosted Supabase uses a URL, and the right value for a given
// deployment has to be read off a real token.
func VerifySupabaseJWT(token, secret, expectedIssuer string, now time.Time) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Claims{}, errors.New("malformed token: want three segments")
	}

	var header struct {
		Alg string `json:"alg"`
	}
	if err := decodeSegment(parts[0], &header); err != nil {
		return Claims{}, fmt.Errorf("malformed header: %w", err)
	}
	// Pin the algorithm first, so "alg":"none" and key-confusion attacks cannot
	// get any further.
	if header.Alg != "HS256" {
		return Claims{}, fmt.Errorf("unsupported algorithm %q", header.Alg)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	expected := mac.Sum(nil)

	actual, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, errors.New("malformed signature")
	}
	if !hmac.Equal(actual, expected) {
		return Claims{}, errors.New("signature mismatch")
	}

	var payload struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Role  string `json:"role"`
		Iss   string `json:"iss"`
		Exp   int64  `json:"exp"`
	}
	if err := decodeSegment(parts[1], &payload); err != nil {
		return Claims{}, fmt.Errorf("malformed payload: %w", err)
	}

	// The published anon key is a valid signature over "role":"anon" using this
	// same secret. Without this check, anyone holding the public key could
	// present it as proof of login.
	if payload.Role != "authenticated" {
		return Claims{}, fmt.Errorf("token role is %q, want authenticated", payload.Role)
	}
	if expectedIssuer != "" && payload.Iss != expectedIssuer {
		return Claims{}, fmt.Errorf("unexpected issuer %q", payload.Iss)
	}
	if payload.Exp == 0 {
		return Claims{}, errors.New("token has no expiry")
	}
	if now.After(time.Unix(payload.Exp, 0)) {
		return Claims{}, errors.New("token expired")
	}
	if payload.Sub == "" {
		return Claims{}, errors.New("token has no subject")
	}
	if payload.Email == "" {
		return Claims{}, errors.New("token has no email")
	}

	return Claims{
		Subject: payload.Sub,
		Email:   strings.ToLower(strings.TrimSpace(payload.Email)),
	}, nil
}

func decodeSegment(seg string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}
