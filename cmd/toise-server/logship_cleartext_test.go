package main

import (
	"strings"
	"testing"

	"github.com/toise-dev/toise/internal/config"
	"github.com/toise-dev/toise/internal/metrics"
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

// TestInsecureConditions pins the one list that feeds both the startup warnings
// and the gauge. Three of these conditions were true in production for months,
// correctly logged and unread, which is why they now have to be alertable.
func TestInsecureConditions(t *testing.T) {
	label := func(cs []metrics.Condition) string {
		parts := make([]string, 0, len(cs))
		for _, c := range cs {
			parts = append(parts, c.Surface+"/"+c.Addr+"/"+c.Reason)
		}
		return strings.Join(parts, " ")
	}
	exposed := config.Config{Listen: "0.0.0.0:18080", OTLPListen: "0.0.0.0:24317"}
	withTLS := func(c config.Config) config.Config {
		c.TLSCertFile, c.TLSKeyFile = "/etc/toise/tls.crt", "/etc/toise/tls.key"
		return c
	}
	shipping := func(c config.Config, endpoint string, ssl bool) config.Config {
		c.LogShipS3Endpoint, c.LogShipS3Bucket, c.LogShipS3UseSSL = endpoint, "toise-segments", ssl
		return c
	}
	upstreamTLS := func(c config.Config) config.Config {
		c.TLSTerminatedUpstream = true
		return c
	}

	cases := []struct {
		name string
		cfg  config.Config
		auth bool
		want string
	}{
		// The production shape found on 2026-10-12: both listeners open with TLS
		// on and no auth, and shipping in the clear to a public address.
		{
			"both listeners unauthenticated plus cleartext shipping",
			shipping(withTLS(exposed), "217.182.253.243:9000", false), false,
			"listener/0.0.0.0:18080/no_auth listener/0.0.0.0:24317/no_auth log_shipping/217.182.253.243:9000/cleartext_public",
		},
		{"no auth and no tls names both", exposed, false, "listener/0.0.0.0:18080/no_auth_no_tls listener/0.0.0.0:24317/no_auth_no_tls"},
		{"auth on, tls off", exposed, true, "listener/0.0.0.0:18080/no_tls listener/0.0.0.0:24317/no_tls"},
		{"auth on and tls on is clean", withTLS(exposed), true, ""},
		{"loopback listeners are not exposed", config.Config{Listen: "127.0.0.1:8080", OTLPListen: "127.0.0.1:4317"}, false, ""},
		{"cleartext shipping on a private link is still reported", shipping(withTLS(exposed), "10.90.0.5:9000", false), true, "log_shipping/10.90.0.5:9000/cleartext"},
		{"shipping with tls is clean", shipping(withTLS(exposed), "10.90.0.5:9000", true), true, ""},

		// The container shape, measured on a customer install on 2026-10-13: the
		// image's own default Cmd binds 0.0.0.0, the ports are published on
		// loopback, TLS ends at nginx and auth is on. The check reads the bound
		// address and nothing else, so before the declaration existed it reported
		// no_tls on both listeners of every containerised deployment behind a
		// terminating proxy — the deployment we recommend — and the documented
		// alert would have fired permanently.
		{"tls declared terminated upstream, auth on, is clean", upstreamTLS(exposed), true, ""},

		// The control that stops the declaration from being a blanket silencer, and
		// the one that would be missing if the case above were the only test: a
		// claim about the perimeter cannot make an unauthenticated read surface
		// acceptable, because whether a bearer is required is a fact this process
		// holds rather than something the proxy decides.
		{"tls declared terminated upstream does not excuse no auth", upstreamTLS(exposed), false, "listener/0.0.0.0:18080/no_auth listener/0.0.0.0:24317/no_auth"},

		// And it is a declaration about TLS alone: unencrypted shipping is a fact
		// about an outbound connection this process makes, which no inbound proxy
		// touches.
		{"tls declared terminated upstream does not excuse cleartext shipping", shipping(upstreamTLS(exposed), "217.182.253.243:9000", false), true, "log_shipping/217.182.253.243:9000/cleartext_public"},
	}
	for _, c := range cases {
		if got := label(insecureConditions(c.cfg, c.auth)); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

// TestExposureReportsZero guards the point of the unlabeled total: a clean
// deployment must publish a zero, because an absent series reads exactly like a
// scrape that failed.
func TestExposureReportsZero(t *testing.T) {
	e := metrics.NewExposure()
	e.Report(nil, nil)
	if n := len(e.Collectors()); n != 4 {
		t.Fatalf("collectors = %d, want 4 (the labeled family, the declarations, the total, the last-check timestamp)", n)
	}
}

// TestDeclarationsArePublishedEitherWay guards what makes a suppressing setting
// auditable. A declaration quiets a control, so a reader has to be able to tell
// an instance that reports nothing because nothing is exposed from one that
// reports nothing because someone said so. If the series existed only when the
// declaration were on, its absence would mean both "not declared" and "not
// scraped" — the very ambiguity the unlabeled total exists to remove.
func TestDeclarationsArePublishedEitherWay(t *testing.T) {
	for _, on := range []bool{true, false} {
		cfg := config.Config{Listen: "0.0.0.0:8080", TLSTerminatedUpstream: on}
		ds := configDeclarations(cfg)
		if len(ds) != 1 || ds[0].Name != "tls_terminated_upstream" || ds[0].On != on {
			t.Fatalf("declarations with tls_terminated_upstream=%v = %+v", on, ds)
		}
	}
}

// TestListenerRemedyAdvisesOnlyWhatIsMissing pins the wording, because the
// previous line is how a warning teaches its reader to skip it: it said "auth
// and/or TLS are off" and recommended setting both, while reporting auth:true
// in its own fields. An operator who had done half the work was told to do it
// again, by a line that was never wrong and never useful.
func TestListenerRemedyAdvisesOnlyWhatIsMissing(t *testing.T) {
	if got := listenerRemedy("no_tls"); strings.Contains(got, "TOISE_AUTH_TOKENS") {
		t.Errorf("the no_tls remedy asks for auth tokens: %q", got)
	}
	if got := listenerRemedy("no_tls"); !strings.Contains(got, "TOISE_TLS_TERMINATED_UPSTREAM") {
		t.Errorf("the no_tls remedy does not mention the declaration, which is the way out behind a proxy: %q", got)
	}
	if got := listenerRemedy("no_auth"); !strings.Contains(got, "TOISE_AUTH_TOKENS") {
		t.Errorf("the no_auth remedy does not name what is missing: %q", got)
	}
	// And the declaration is never offered as a way out of missing auth.
	if got := listenerRemedy("no_auth"); strings.Contains(got, "TOISE_TLS_TERMINATED_UPSTREAM") {
		t.Errorf("the no_auth remedy offers the TLS declaration as a remedy: %q", got)
	}
	if got := listenerRemedy("no_auth_no_tls"); strings.Contains(got, "TOISE_TLS_TERMINATED_UPSTREAM") {
		t.Errorf("the no_auth_no_tls remedy offers the TLS declaration as a remedy: %q", got)
	}
}

// TestVersionRequested pins which invocations print the version instead of
// serving. The negative cases matter more than the positive ones: a server that
// prints a version and exits where it was asked to serve looks exactly like a
// crash loop, and a unit file with a stray flag must not produce that.
func TestVersionRequested(t *testing.T) {
	for _, args := range [][]string{
		{"--version"}, {"-version"}, {"version"}, {"-v"},
	} {
		if !versionRequested(args) {
			t.Errorf("%v must print the version", args)
		}
	}
	for _, args := range [][]string{
		nil,
		{},
		{"--listen", "0.0.0.0:8080"},
		{"--listen", "0.0.0.0:8080", "--version"},
		{"checkpoint", "--version"},
		{"--versions"},
		{"restore-log"},
	} {
		if versionRequested(args) {
			t.Errorf("%v must serve (or run its subcommand), not print the version", args)
		}
	}
}
