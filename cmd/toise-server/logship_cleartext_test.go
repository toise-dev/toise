package main

import (
	"testing"

	"github.com/toise-dev/toise/internal/config"
)

// TestCleartextShipping pins when the server must say that the event log leaves
// readable. The default is TLS on, so every true here is a deliberate operator
// choice — and the one misconfiguration that never shows up as a failure,
// because the segments arrive exactly as asked.
func TestCleartextShipping(t *testing.T) {
	ship := func(endpoint string, ssl bool) config.Config {
		return config.Config{
			LogShipS3Endpoint: endpoint,
			LogShipS3Bucket:   "toise-logs",
			LogShipS3UseSSL:   ssl,
		}
	}
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		// The shape found in production on 2026-10-12: TLS off, public IP of
		// another machine, routed off the trusted mesh.
		{"public ip, no tls", ship("217.182.253.243:9000", false), true},
		{"private ip, no tls", ship("10.90.0.5:9000", false), true},
		{"hostname, no tls", ship("minio.internal:9000", false), true},
		{"public ip with tls", ship("217.182.253.243:9000", true), false},
		{"loopback, no tls", ship("127.0.0.1:9000", false), false},
		{"localhost, no tls", ship("localhost:9000", false), false},
		{"no s3 target at all", config.Config{LogShipDir: "/var/lib/toise/ship"}, false},
		// A bucket with no endpoint is not an S3 target: nothing is shipped
		// anywhere, so there is nothing to warn about.
		{"bucket without endpoint", config.Config{LogShipS3Bucket: "toise-logs"}, false},
	}
	for _, c := range cases {
		if got := cleartextShipping(c.cfg); got != c.want {
			t.Errorf("%s: cleartextShipping = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestPublicIPEndpoint separates "unencrypted inside a perimeter" from
// "unencrypted across the internet": the warning carries the difference because
// it is what decides how fast an operator has to act.
func TestPublicIPEndpoint(t *testing.T) {
	for addr, want := range map[string]bool{
		"217.182.253.243:9000": true,
		"217.182.253.243":      true,
		"10.90.0.5:9000":       false,
		"192.168.1.10:9000":    false,
		"127.0.0.1:9000":       false,
		"169.254.1.1:9000":     false,
		"minio.internal:9000":  false,
		"":                     false,
	} {
		if got := publicIPEndpoint(addr); got != want {
			t.Errorf("publicIPEndpoint(%q) = %v, want %v", addr, got, want)
		}
	}
}
