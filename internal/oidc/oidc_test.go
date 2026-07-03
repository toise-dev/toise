package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const (
	testIssuer   = "https://issuer.test"
	testAudience = "toise"
)

func testVerifier(t *testing.T, priv *rsa.PrivateKey, cfg Config) *Verifier {
	t.Helper()
	ks := &gooidc.StaticKeySet{PublicKeys: []crypto.PublicKey{priv.Public()}}
	v := gooidc.NewVerifier(testIssuer, ks, &gooidc.Config{ClientID: testAudience})
	return newFromVerifier(v, cfg)
}

func mintJWT(t *testing.T, priv *rsa.PrivateKey, iss, aud string, exp time.Time, extra map[string]any) string {
	t.Helper()
	sig, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: priv}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		t.Fatal(err)
	}
	public := jwt.Claims{
		Issuer:   iss,
		Audience: jwt.Audience{aud},
		Expiry:   jwt.NewNumericDate(exp),
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}
	raw, err := jwt.Signed(sig).Claims(public).Claims(extra).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestVerify(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	future := time.Now().Add(time.Hour)
	cfg := Config{Issuer: testIssuer, Audience: testAudience, TenantClaim: "tenant", RoleClaim: "role"}
	vr := testVerifier(t, priv, cfg)

	// A nil verifier (OIDC disabled) rejects everything.
	var off *Verifier
	if _, _, ok := off.Verify(ctx, "anything"); ok {
		t.Error("nil verifier must reject")
	}

	// Valid token: tenant + role from the configured claims.
	tok := mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"tenant": "acme", "role": "read"})
	if id, role, ok := vr.Verify(ctx, tok); !ok || id != "acme" || role != string(RoleRead) {
		t.Errorf("valid token = %q,%v,%v; want acme,read,true", id, role, ok)
	}

	// No role claim configured => full role.
	vrNoRole := testVerifier(t, priv, Config{Issuer: testIssuer, Audience: testAudience})
	tok = mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"tenant": "acme"})
	if id, role, ok := vrNoRole.Verify(ctx, tok); !ok || id != "acme" || role != string(RoleFull) {
		t.Errorf("no role claim => full; got %q,%v,%v", id, role, ok)
	}

	// Every verification failure path must reject.
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	bad := []struct {
		name string
		tok  string
	}{
		{"expired", mintJWT(t, priv, testIssuer, testAudience, time.Now().Add(-time.Hour), map[string]any{"tenant": "acme"})},
		{"wrong issuer", mintJWT(t, priv, "https://evil.test", testAudience, future, map[string]any{"tenant": "acme"})},
		{"wrong audience", mintJWT(t, priv, testIssuer, "other", future, map[string]any{"tenant": "acme"})},
		{"unknown signing key", mintJWT(t, other, testIssuer, testAudience, future, map[string]any{"tenant": "acme"})},
		{"missing tenant claim", mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"role": "read"})},
		{"unknown role", mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"tenant": "acme", "role": "superuser"})},
		{"role claim configured but absent", mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"tenant": "acme"})},
		{"role claim configured but empty", mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"tenant": "acme", "role": ""})},
		{"non-canonical tenant", mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"tenant": "../etc"})},
		{"garbage", "not.a.jwt"},
	}
	for _, c := range bad {
		if _, _, ok := vr.Verify(ctx, c.tok); ok {
			t.Errorf("%s: must reject, but verified", c.name)
		}
	}
}
