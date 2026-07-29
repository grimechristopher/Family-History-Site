package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const testSecret = "super-secret-jwt-value-for-tests"

func mint(t *testing.T, secret string, claims map[string]any, alg string) string {
	t.Helper()
	enc := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := enc(map[string]string{"alg": alg, "typ": "JWT"})
	body := enc(claims)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(head + "." + body))
	return head + "." + body + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func validClaims(exp time.Time) map[string]any {
	return map[string]any{
		"sub":   "3f1c9a44-6b2e-4f7a-9c11-0d8e5b7a2c33",
		"email": "Dad@Example.com",
		"role":  "authenticated",
		"iss":   "supabase",
		"exp":   exp.Unix(),
	}
}

func TestVerifyAcceptsValidToken(t *testing.T) {
	now := time.Now()
	tok := mint(t, testSecret, validClaims(now.Add(time.Hour)), "HS256")

	got, err := VerifySupabaseJWT(tok, testSecret, "", now)
	if err != nil {
		t.Fatalf("VerifySupabaseJWT: %v", err)
	}
	if got.Subject != "3f1c9a44-6b2e-4f7a-9c11-0d8e5b7a2c33" {
		t.Errorf("Subject = %q", got.Subject)
	}
	if got.Email != "dad@example.com" {
		t.Errorf("Email = %q, want lowercased", got.Email)
	}
}

// The published anon key is signed with the same secret and carries
// "role":"anon". Accepting it would let anyone log in.
func TestVerifyRejectsAnonRole(t *testing.T) {
	now := time.Now()
	c := validClaims(now.Add(time.Hour))
	c["role"] = "anon"
	delete(c, "email")
	tok := mint(t, testSecret, c, "HS256")

	_, err := VerifySupabaseJWT(tok, testSecret, "", now)
	if err == nil {
		t.Fatal("anon-role token must be rejected")
	}
	if !strings.Contains(err.Error(), "role") {
		t.Errorf("error should name the role, got: %v", err)
	}
}

func TestVerifyRejectsServiceRole(t *testing.T) {
	now := time.Now()
	c := validClaims(now.Add(time.Hour))
	c["role"] = "service_role"
	tok := mint(t, testSecret, c, "HS256")

	if _, err := VerifySupabaseJWT(tok, testSecret, "", now); err == nil {
		t.Fatal("service_role token must not authenticate a person")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	now := time.Now()
	tok := mint(t, "a-different-secret", validClaims(now.Add(time.Hour)), "HS256")

	if _, err := VerifySupabaseJWT(tok, testSecret, "", now); err == nil {
		t.Fatal("token signed with the wrong secret must be rejected")
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	now := time.Now()
	tok := mint(t, testSecret, validClaims(now.Add(-time.Minute)), "HS256")

	_, err := VerifySupabaseJWT(tok, testSecret, "", now)
	if err == nil {
		t.Fatal("expired token must be rejected")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention expiry, got: %v", err)
	}
}

func TestVerifyRejectsMissingExpiry(t *testing.T) {
	now := time.Now()
	c := validClaims(now.Add(time.Hour))
	delete(c, "exp")
	tok := mint(t, testSecret, c, "HS256")

	if _, err := VerifySupabaseJWT(tok, testSecret, "", now); err == nil {
		t.Fatal("a token with no expiry must be rejected")
	}
}

// "alg":"none" is the classic JWT bypass.
func TestVerifyRejectsNonHS256(t *testing.T) {
	now := time.Now()
	for _, alg := range []string{"none", "None", "RS256", "HS512"} {
		tok := mint(t, testSecret, validClaims(now.Add(time.Hour)), alg)
		if _, err := VerifySupabaseJWT(tok, testSecret, "", now); err == nil {
			t.Errorf("alg %q must be rejected", alg)
		}
	}
}

func TestVerifyEnforcesIssuerOnlyWhenConfigured(t *testing.T) {
	now := time.Now()
	c := validClaims(now.Add(time.Hour))
	c["iss"] = "https://supabase.example.com/auth/v1"
	tok := mint(t, testSecret, c, "HS256")

	if _, err := VerifySupabaseJWT(tok, testSecret, "", now); err != nil {
		t.Errorf("unset issuer should not be enforced: %v", err)
	}
	if _, err := VerifySupabaseJWT(tok, testSecret, "https://supabase.example.com/auth/v1", now); err != nil {
		t.Errorf("matching issuer should pass: %v", err)
	}
	if _, err := VerifySupabaseJWT(tok, testSecret, "supabase", now); err == nil {
		t.Error("mismatched issuer must be rejected")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	now := time.Now()
	for _, tok := range []string{"", "a.b", "a.b.c.d", "not-base64.not-base64.not-base64", "..."} {
		if _, err := VerifySupabaseJWT(tok, testSecret, "", now); err == nil {
			t.Errorf("token %q must be rejected", tok)
		}
	}
}

func TestVerifyRequiresEmailAndSubject(t *testing.T) {
	now := time.Now()

	noEmail := validClaims(now.Add(time.Hour))
	delete(noEmail, "email")
	if _, err := VerifySupabaseJWT(mint(t, testSecret, noEmail, "HS256"), testSecret, "", now); err == nil {
		t.Error("a token without an email must be rejected: the allowlist needs it")
	}

	noSub := validClaims(now.Add(time.Hour))
	delete(noSub, "sub")
	if _, err := VerifySupabaseJWT(mint(t, testSecret, noSub, "HS256"), testSecret, "", now); err == nil {
		t.Error("a token without a subject must be rejected")
	}
}

// A tampered payload must fail even though its own signature segment is intact.
func TestVerifyDetectsTamperedPayload(t *testing.T) {
	now := time.Now()
	tok := mint(t, testSecret, validClaims(now.Add(time.Hour)), "HS256")
	parts := strings.Split(tok, ".")

	forged, err := json.Marshal(map[string]any{
		"sub": "attacker", "email": "attacker@example.com",
		"role": "authenticated", "iss": "supabase", "exp": now.Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(forged)

	if _, err := VerifySupabaseJWT(strings.Join(parts, "."), testSecret, "", now); err == nil {
		t.Fatal("a swapped payload must fail signature verification")
	}
}
