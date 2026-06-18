package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTestCA writes a self-signed CA certificate PEM to a temp file and returns
// its path.
func writeTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Unix(1_700_000_000, 0),
		NotAfter:              time.Unix(1_900_000_000, 0),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIngestTLSConfig(t *testing.T) {
	base := &tls.Config{MinVersion: tls.VersionTLS12}

	// No client CA: a clone with no client-certificate requirement.
	conf, err := ingestTLSConfig(base, "")
	if err != nil {
		t.Fatal(err)
	}
	if conf.ClientAuth != tls.NoClientCert || conf.ClientCAs != nil {
		t.Errorf("no client CA: ClientAuth=%v, ClientCAs set=%v; want neither", conf.ClientAuth, conf.ClientCAs != nil)
	}

	// A valid CA bundle: require and verify client certs against it.
	conf, err = ingestTLSConfig(base, writeTestCA(t))
	if err != nil {
		t.Fatal(err)
	}
	if conf.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("with client CA: ClientAuth=%v, want RequireAndVerifyClientCert", conf.ClientAuth)
	}
	if conf.ClientCAs == nil {
		t.Error("with client CA: ClientCAs pool must be set")
	}
	if base.ClientAuth != tls.NoClientCert {
		t.Error("base config must not be mutated (we clone)")
	}

	// A missing file and a non-PEM file are hard errors, never silent.
	if _, err := ingestTLSConfig(base, filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Error("a missing client CA file must error")
	}
	bad := filepath.Join(t.TempDir(), "bad.pem")
	if werr := os.WriteFile(bad, []byte("not a pem"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if _, err := ingestTLSConfig(base, bad); err == nil {
		t.Error("a file with no usable certificates must error")
	}
}
