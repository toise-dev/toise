package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"
)

func TestVerifySubject(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	future := time.Now().Add(time.Hour)
	cfg := Config{Issuer: testIssuer, Audience: testAudience, TenantClaim: "tenant"}
	vr := testVerifier(t, priv, cfg)

	// Valid token: subject from the standard "sub" claim.
	tok := mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"tenant": "tenant-a", "sub": "user-a"})
	if _, _, subj, ok := vr.Verify(ctx, tok); !ok || subj != "user-a" {
		t.Errorf("valid token = %q,%v; want user-a,true", subj, ok)
	}

	// No sub claim: still verifies, subject is empty.
	tok = mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"tenant": "tenant-a"})
	if _, _, subj, ok := vr.Verify(ctx, tok); !ok || subj != "" {
		t.Errorf("missing sub = %q,%v; want \"\",true", subj, ok)
	}

	// Non-string sub: the underlying verifier already rejects the token.
	tok = mintJWT(t, priv, testIssuer, testAudience, future, map[string]any{"tenant": "tenant-a", "sub": 42})
	if _, _, _, ok := vr.Verify(ctx, tok); ok {
		t.Error("non-string sub must be rejected")
	}
}
