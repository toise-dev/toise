package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHardeningDefaults(t *testing.T) {
	d := Default()
	if !d.GraphQLIntrospection || !d.Playground || !d.DebugUI || d.Production {
		t.Errorf("dev defaults: introspection=%v playground=%v debugUI=%v production=%v, want true/true/true/false",
			d.GraphQLIntrospection, d.Playground, d.DebugUI, d.Production)
	}
}

// TestSnapshotOnByDefault pins #164: snapshots default to 5m so the liveness
// memento survives restarts out of the box; a 0 default left the sweeper
// backstop silently restart-amnesiac.
func TestSnapshotOnByDefault(t *testing.T) {
	if d := Default(); d.SnapshotInterval.D() != 5*time.Minute {
		t.Errorf("default snapshot_interval = %v, want 5m", d.SnapshotInterval.D())
	}
}

func TestProductionLockdownWinsOverToggles(t *testing.T) {
	// --production forces all three off, even if a toggle tries to re-enable.
	cfg, err := Load([]string{"--production", "--playground=true"}, env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.GraphQLIntrospection || cfg.Playground || cfg.DebugUI {
		t.Errorf("production lockdown: introspection=%v playground=%v debugUI=%v, want all false",
			cfg.GraphQLIntrospection, cfg.Playground, cfg.DebugUI)
	}
}

func TestIndividualToggleWithoutProduction(t *testing.T) {
	cfg, err := Load([]string{"--playground=false"}, env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Playground {
		t.Error("playground should be off")
	}
	if !cfg.GraphQLIntrospection || !cfg.DebugUI {
		t.Error("the other surfaces should stay on without --production")
	}
}

func TestAllowedOriginsParsing(t *testing.T) {
	cfg, err := Load(nil, env(map[string]string{"TOISE_ALLOWED_ORIGINS": "https://a.example, https://b.example , "}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AllowedOrigins) != 2 || cfg.AllowedOrigins[0] != "https://a.example" || cfg.AllowedOrigins[1] != "https://b.example" {
		t.Errorf("allowed origins = %v, want [https://a.example https://b.example]", cfg.AllowedOrigins)
	}
}

func TestAuthAndTLSConfig(t *testing.T) {
	cfg, err := Load([]string{"--tls-cert-file", "/c.pem", "--tls-key-file", "/k.pem"},
		env(map[string]string{"TOISE_AUTH_TOKENS": "tok1, tok2 , "}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AuthTokens) != 2 || cfg.AuthTokens[0] != "tok1" || cfg.AuthTokens[1] != "tok2" {
		t.Errorf("auth tokens = %v, want [tok1 tok2]", cfg.AuthTokens)
	}
	if !cfg.TLSEnabled() {
		t.Error("TLSEnabled should be true with both cert and key set")
	}
	if c2, _ := Load([]string{"--tls-cert-file", "/c.pem"}, env(nil)); c2.TLSEnabled() {
		t.Error("TLSEnabled needs both cert AND key")
	}
	if d := Default(); d.TLSEnabled() || len(d.AuthTokens) != 0 {
		t.Error("defaults: no TLS, no tokens")
	}
}

func TestLogConfig(t *testing.T) {
	d := Default()
	if d.LogFormat != "text" || d.LogLevel != "info" {
		t.Errorf("log defaults = %q / %q, want text / info", d.LogFormat, d.LogLevel)
	}
	if (Config{LogLevel: "debug"}).SlogLevel() != slog.LevelDebug {
		t.Error("debug should map to slog.LevelDebug")
	}
	if (Config{LogLevel: "nonsense"}).SlogLevel() != slog.LevelInfo {
		t.Error("unknown level should fall back to info")
	}
	var buf bytes.Buffer
	slog.New((Config{LogFormat: "json", LogLevel: "info"}).NewLogHandler(&buf)).Info("hi", "k", "v")
	if !strings.Contains(buf.String(), `"msg":"hi"`) {
		t.Errorf("json handler output = %q, want JSON", buf.String())
	}
	cfg, err := Load(nil, env(map[string]string{"TOISE_LOG_FORMAT": "json", "TOISE_LOG_LEVEL": "warn"}))
	if err != nil || cfg.LogFormat != "json" || cfg.LogLevel != "warn" {
		t.Errorf("env log config = %q/%q err=%v", cfg.LogFormat, cfg.LogLevel, err)
	}
}

// env builds a getenv function backed by a map, so tests resolve config without
// touching the real process environment.
func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "toise.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp config: %v", err)
	}
	return p
}

func TestDefault(t *testing.T) {
	d := Default()
	if d.Listen != "127.0.0.1:8080" || d.OTLPListen != "127.0.0.1:4317" {
		t.Errorf("default listeners = %q / %q", d.Listen, d.OTLPListen)
	}
	if d.CompactionInterval.D() != time.Hour || d.RetentionMaxAge.D() != 0 {
		t.Errorf("default retention = %v / %v", d.CompactionInterval.D(), d.RetentionMaxAge.D())
	}
}

// TestLoadPrecedence is the core contract: defaults < file < env < flags, each
// layer overriding only what it sets.
func TestLoadPrecedence(t *testing.T) {
	path := writeFile(t, `
listen: 10.0.0.1:9000
data_dir: /var/lib/toise
relation_buffer_ttl: 1m
`)
	getenv := env(map[string]string{
		"TOISE_DATA_DIR":            "/srv/toise", // env overrides the file
		"TOISE_RELATION_BUFFER_TTL": "2m",         // env overrides the file...
	})
	// ...and a flag overrides env for relation-buffer-ttl.
	args := []string{"--config", path, "--relation-buffer-ttl", "3m"}

	cfg, err := Load(args, getenv)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "10.0.0.1:9000" {
		t.Errorf("listen = %q, want the file value (no env/flag set it)", cfg.Listen)
	}
	if cfg.DataDir != "/srv/toise" {
		t.Errorf("data_dir = %q, want the env value (env overrides file)", cfg.DataDir)
	}
	if cfg.RelationBufferTTL.D() != 3*time.Minute {
		t.Errorf("relation_buffer_ttl = %v, want 3m (flag overrides env+file)", cfg.RelationBufferTTL.D())
	}
	// A field nobody set keeps the built-in default.
	if cfg.OTLPListen != "127.0.0.1:4317" {
		t.Errorf("otlp_listen = %q, want the default", cfg.OTLPListen)
	}
}

func TestLoadConfigPathFromEnv(t *testing.T) {
	path := writeFile(t, "listen: 192.168.0.1:7000\n")
	cfg, err := Load(nil, env(map[string]string{"TOISE_CONFIG": path}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Listen != "192.168.0.1:7000" {
		t.Errorf("listen = %q, want the file (path from TOISE_CONFIG)", cfg.Listen)
	}
}

func TestLoadRejectsUnknownKey(t *testing.T) {
	path := writeFile(t, "listne: typo\n") // misspelled key
	if _, err := Load([]string{"--config", path}, env(nil)); err == nil {
		t.Error("want an error for an unknown config key, got nil")
	}
}

func TestLoadRejectsBadEnvDuration(t *testing.T) {
	_, err := Load(nil, env(map[string]string{"TOISE_RETENTION_MAX_AGE": "forever"}))
	if err == nil {
		t.Error("want an error for a malformed duration env var, got nil")
	}
}

func TestLoadParsesFileDurations(t *testing.T) {
	path := writeFile(t, "retention_compaction_interval: 1h30m\n")
	cfg, err := Load([]string{"--config", path}, env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.CompactionInterval.D() != 90*time.Minute {
		t.Errorf("compaction interval = %v, want 1h30m", cfg.CompactionInterval.D())
	}
}

func TestLoadMissingFileIsError(t *testing.T) {
	if _, err := Load([]string{"--config", "/no/such/file.yaml"}, env(nil)); err == nil {
		t.Error("want an error for a missing --config file, got nil")
	}
}

// TestValidateRejectsSilentMisconfigurations pins #115: configurations that
// would start but not do what they say must fail at load.
func TestValidateRejectsSilentMisconfigurations(t *testing.T) {
	base := Default()
	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"half-set TLS (cert only)", func(c *Config) { c.TLSCertFile = "/x/cert.pem" }},
		{"half-set TLS (key only)", func(c *Config) { c.TLSKeyFile = "/x/key.pem" }},
		{"retention without compaction", func(c *Config) { c.RetentionMaxAge = Duration(time.Hour); c.CompactionInterval = 0 }},
		{"unknown log level", func(c *Config) { c.LogLevel = "verbose" }},
		{"unknown log format", func(c *Config) { c.LogFormat = "logfmt" }},
		{"negative max_tenants", func(c *Config) { c.MaxTenants = -1 }},
		{"empty allowlist entry", func(c *Config) { c.TenantAllowlist = []string{""} }},
		{"whitespace allowlist entry", func(c *Config) { c.TenantAllowlist = []string{"ten ant"} }},
		{"unknown tenant_trust_mode", func(c *Config) { c.TenantTrustMode = "wishful" }},
		{"mTLS client CA without server TLS", func(c *Config) { c.TLSClientCAFile = "/x/ca.pem" }},
		{"bad tenant_read_tokens pair", func(c *Config) { c.TenantReadTokens = []string{"no-colon"} }},
	}
	for _, tc := range cases {
		cfg := base
		tc.mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: Validate accepted it", tc.name)
		}
	}
	if err := base.Validate(); err != nil {
		t.Errorf("default config must validate: %v", err)
	}
	// "warning" is a valid level (alias for warn): SlogLevel maps it, so Validate
	// must not reject it.
	warn := base
	warn.LogLevel = "warning"
	if err := warn.Validate(); err != nil {
		t.Errorf("log_level \"warning\" must validate (SlogLevel maps it): %v", err)
	}
}

// TestTenantTokensMap pins the "tenant:token" parsing: canonical tenant ids
// only, non-empty tokens, malformed pairs are hard errors.
func TestTenantTokensMap(t *testing.T) {
	cfg := Default()
	cfg.TenantTokens = []string{"acme:s3cret", "acme:other", "globex:tok"}
	m, err := cfg.TenantTokensMap()
	if err != nil {
		t.Fatalf("valid pairs: %v", err)
	}
	if len(m["acme"]) != 2 || len(m["globex"]) != 1 {
		t.Errorf("parsed map = %v", m)
	}
	for _, bad := range []string{"notoken", ":empty-tenant", "acme:", "../esc:tok", "a/b:tok"} {
		cfg.TenantTokens = []string{bad}
		if _, err := cfg.TenantTokensMap(); err == nil {
			t.Errorf("pair %q accepted, want error", bad)
		}
		if err := cfg.Validate(); err == nil {
			t.Errorf("Validate accepted pair %q", bad)
		}
	}
}
