// Package config resolves the toise-server configuration from layered sources.
//
// Precedence, lowest to highest: built-in defaults < YAML file < environment
// variables (TOISE_*) < command-line flags. The file path itself comes from
// --config or TOISE_CONFIG. Secrets are only ever sourced from the environment,
// never required on the command line. See ADR 0023.
package config

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/toise-dev/toise/internal/tenant"
)

// ErrHelp is returned by Load when -h/--help was requested (the flag set has
// already printed the usage). Callers should treat it as a clean exit, not a
// failure.
var ErrHelp = flag.ErrHelp

// Duration wraps time.Duration so it (un)marshals as a Go-duration string ("30s",
// "1h") in YAML, which the standard time.Duration does not.
type Duration time.Duration

// D returns the wrapped time.Duration.
func (d Duration) D() time.Duration { return time.Duration(d) }

// UnmarshalYAML parses a Go-duration string into the Duration.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(parsed)
	return nil
}

// MarshalYAML renders the Duration as a Go-duration string.
func (d Duration) MarshalYAML() (any, error) { return time.Duration(d).String(), nil }

// Config is the full toise-server configuration. Field tags name the YAML keys;
// the matching environment variable is TOISE_<UPPER_SNAKE> and the flag is the
// kebab-case form (e.g. RelationBufferTTL -> relation_buffer_ttl ->
// TOISE_RELATION_BUFFER_TTL -> --relation-buffer-ttl).
type Config struct {
	Listen                string   `yaml:"listen"`
	OTLPListen            string   `yaml:"otlp_listen"`
	DataDir               string   `yaml:"data_dir"`
	MCPStdio              bool     `yaml:"mcp_stdio"`
	RelationBufferTTL     Duration `yaml:"relation_buffer_ttl"`
	LivenessSweepInterval Duration `yaml:"liveness_sweep_interval"`
	RetentionMaxAge       Duration `yaml:"retention_max_age"`
	CompactionInterval    Duration `yaml:"retention_compaction_interval"`
	SnapshotInterval      Duration `yaml:"snapshot_interval"` // 0 = disabled (replay full log on start)
	LogFormat             string   `yaml:"log_format"`        // "text" or "json"
	LogLevel              string   `yaml:"log_level"`         // debug | info | warn | error

	// Production is a hardening profile: when true it forces GraphQLIntrospection,
	// Playground, and DebugUI off regardless of their individual values.
	Production           bool `yaml:"production"`
	GraphQLIntrospection bool `yaml:"graphql_introspection"`
	Playground           bool `yaml:"playground"`
	DebugUI              bool `yaml:"debug_ui"`
	// AllowedOrigins is the browser Origin allowlist for WebSocket subscriptions
	// (and CORS). Empty means same-origin only.
	AllowedOrigins []string `yaml:"allowed_origins"`

	// AcceptUnknownTypes opens the producer vocabulary: entity/relation types
	// outside the built-in registry are accepted when their shape is sound
	// (identity present, well-formed key-values), instead of rejected per
	// record. Identity hashing is type-prefixed, so unknown types are
	// first-class identities with no merge ambiguity. Off by default. (#141)
	AcceptUnknownTypes bool `yaml:"accept_unknown_types"`

	// TenantAutoCreate allows a first write to a new tenant id to create its
	// isolated stack (the open multi-tenant posture). Off, only pre-existing
	// tenants and the default are served. TenantAllowlist, when non-empty,
	// restricts which NEW tenant ids may be created; MaxTenants (>0) caps the
	// number of open tenants. See #115.
	TenantAutoCreate bool     `yaml:"tenant_auto_create"`
	TenantAllowlist  []string `yaml:"tenant_allowlist"`
	MaxTenants       int      `yaml:"max_tenants"`

	// AuthTokens are accepted bearer tokens for the data surfaces (GraphQL, MCP,
	// debug UI, OTLP ingest). Empty disables auth (trusted-network default). These
	// are secrets: source them from TOISE_AUTH_TOKENS (env), never a flag.
	AuthTokens []string `yaml:"auth_tokens"`
	// ReadTokens are bearer tokens valid only on the read surfaces (GraphQL, MCP,
	// debug UI) — a dashboard or assistant that must never ingest. IngestTokens
	// are valid only on OTLP ingest — a producer that must never read. Same secret
	// rules as AuthTokens: TOISE_READ_TOKENS / TOISE_INGEST_TOKENS (env). Tokens in
	// AuthTokens remain full (both surfaces). (0.7.0 token roles.)
	ReadTokens   []string `yaml:"read_tokens"`
	IngestTokens []string `yaml:"ingest_tokens"`
	// TenantTokens are tenant-scoped bearer tokens as "tenant:token" pairs: the
	// token authenticates like any other but is authorized only for its tenant
	// (#104). Same secret rules: TOISE_TENANT_TOKENS (env), never a flag.
	TenantTokens []string `yaml:"tenant_tokens"`
	// TenantReadTokens / TenantIngestTokens are tenant-scoped bearer tokens with a
	// role: read-only or ingest-only, authorized only for their tenant (per-tenant
	// RBAC, ADR 0028). Same "tenant:token" format and secret rules as TenantTokens
	// (TOISE_TENANT_READ_TOKENS / TOISE_TENANT_INGEST_TOKENS, env only). Tokens in
	// TenantTokens stay full (both surfaces) for their tenant.
	TenantReadTokens   []string `yaml:"tenant_read_tokens"`
	TenantIngestTokens []string `yaml:"tenant_ingest_tokens"`
	// TenantTrustMode selects how a request's tenant is decided (ADR 0028, tier-2,
	// off by default):
	//   "trust-header" (default) — the tenant comes from the X-Scope-OrgID header /
	//     tenant.id resource attribute; the network/edge is trusted. Unchanged
	//     behavior, so the zero-config and self-hosted postures are untouched.
	//   "derive-only" — for a tenant-scoped token the tenant is derived from the
	//     token's binding and any client-supplied X-Scope-OrgID / tenant.id is
	//     ignored (anti-spoofing for SaaS). Global (operator) tokens keep
	//     header-based, cross-tenant selection.
	TenantTrustMode string `yaml:"tenant_trust_mode"`
	// TLSCertFile/TLSKeyFile enable native TLS on the HTTP and OTLP listeners when
	// both are set.
	TLSCertFile string `yaml:"tls_cert_file"`
	TLSKeyFile  string `yaml:"tls_key_file"`
	// TLSClientCAFile, when set, requires and verifies a client certificate on the
	// OTLP ingest listener against this PEM CA bundle — optional mTLS for ingest
	// (ADR 0028), on top of bearer auth. Requires TLS to be enabled. Not a secret.
	// The HTTP read surfaces are unaffected (they use bearer/OIDC).
	TLSClientCAFile string `yaml:"tls_client_ca_file"`
	// IngestMTLSOnly decouples ingest auth from read auth (#262): when true, the
	// OTLP ingest surface relies on mutual TLS alone and requires no bearer token,
	// while the read surfaces (GraphQL, MCP) keep requiring their scoped tokens /
	// OIDC. Requires TLSClientCAFile (there must be a verified client cert to rely
	// on). Default false = unchanged (a bearer is required on ingest when tokens
	// are configured). Not a secret: TOISE_INGEST_MTLS_ONLY (env) or yaml.
	IngestMTLSOnly bool `yaml:"ingest_mtls_only"`
	// AuditLog is a file path that, when set, receives an append-only JSON-line
	// audit record for every operator write (annotate_entity) — distinct from the
	// event log (ADR 0028). Empty = auditing off (the default). Not a secret.
	AuditLog string `yaml:"audit_log"`
	// BackupDir, with BackupInterval > 0, enables periodic online backups: every
	// interval each tenant's event log is checkpointed (Pebble's lock-free
	// checkpoint) into <BackupDir>/<timestamp>/<tenant>, for an operator to sync
	// off-node (ADR 0029). Empty = no scheduled backups (the default).
	BackupDir      string   `yaml:"backup_dir"`
	BackupInterval Duration `yaml:"backup_interval"`
	// LogShipDir, with LogShipInterval > 0, enables continuous log shipping: every
	// interval each tenant's new event-log tail is exported as an immutable segment
	// under <LogShipDir>/<tenant>/, the finer-RPO complement to scheduled backups
	// (ADR 0029). The directory may be a mounted object-store bucket / NFS / rsync
	// target. Empty = off (the default).
	LogShipDir      string   `yaml:"log_shipping_dir"`
	LogShipInterval Duration `yaml:"log_shipping_interval"`
	// LogShipS3* ship segments to an S3-compatible object store instead of a
	// directory (same LogShipInterval). Setting LogShipS3Bucket selects S3. Works
	// against AWS S3 and any compatible store (MinIO/Ceph/R2/…) — one config shape.
	// The access/secret keys are secrets: env-only (TOISE_LOG_SHIPPING_S3_ACCESS_KEY
	// / _SECRET_KEY), never a flag or the config file.
	LogShipS3Endpoint  string `yaml:"log_shipping_s3_endpoint"`
	LogShipS3Bucket    string `yaml:"log_shipping_s3_bucket"`
	LogShipS3Region    string `yaml:"log_shipping_s3_region"`
	LogShipS3Prefix    string `yaml:"log_shipping_s3_prefix"`
	LogShipS3UseSSL    bool   `yaml:"log_shipping_s3_use_ssl"`
	LogShipS3AccessKey string `yaml:"-"`
	LogShipS3SecretKey string `yaml:"-"`
	// OIDC* enable verifying OIDC/JWT bearers on the read surfaces (ADR 0028).
	// OIDCIssuer empty = OIDC off (the default). Not secrets — the issuer/audience
	// and claim names are configuration. OIDCTenantClaim defaults to "tenant";
	// OIDCRoleClaim empty means every valid token is full role.
	OIDCIssuer      string `yaml:"oidc_issuer"`
	OIDCAudience    string `yaml:"oidc_audience"`
	OIDCTenantClaim string `yaml:"oidc_tenant_claim"`
	OIDCRoleClaim   string `yaml:"oidc_role_claim"`
	// IdentityThreshold is the same_as confidence (0–1] at or above which an alias
	// belief joins an entity's canonical group in the read-time identity overlay
	// (ADR 0020). Default 0.9 (the high band). Never merges storage.
	IdentityThreshold float64 `yaml:"identity_confidence_threshold"`
}

// TLSEnabled reports whether both a certificate and key are configured.
func (c Config) TLSEnabled() bool { return c.TLSCertFile != "" && c.TLSKeyFile != "" }

// Validate rejects configurations that would start but silently not do what
// they say (#115): half-set TLS serving plaintext, a retention age nothing
// ever prunes, a log level that falls back to info unnoticed.
func (c Config) Validate() error {
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("tls_cert_file and tls_key_file must be set together: half-set TLS would silently serve plaintext")
	}
	if c.TLSClientCAFile != "" && !c.TLSEnabled() {
		return fmt.Errorf("tls_client_ca_file (ingest mTLS) requires tls_cert_file and tls_key_file: there is no TLS handshake to verify a client cert on")
	}
	if c.IngestMTLSOnly && c.TLSClientCAFile == "" {
		return fmt.Errorf("ingest_mtls_only requires tls_client_ca_file: with no verified client certificate, ingest would be left open")
	}
	if c.IngestMTLSOnly {
		// ingest_mtls_only decouples ingest auth (mTLS) from read auth, so the
		// read surfaces MUST have their own authenticator — otherwise mTLS protects
		// ingest while GraphQL/MCP/debug UI are wide open, the opposite of #262.
		readAuth := len(c.AuthTokens) > 0 || len(c.ReadTokens) > 0 ||
			len(c.TenantTokens) > 0 || len(c.TenantReadTokens) > 0 || c.OIDCIssuer != ""
		if !readAuth {
			return fmt.Errorf("ingest_mtls_only leaves the read surfaces open: configure a read authenticator (read_tokens / tenant_read_tokens / auth_tokens / oidc_issuer) or drop ingest_mtls_only")
		}
		// A client certificate carries no tenant, so mTLS-only ingest takes the
		// tenant from the request header/resource attribute — which derive-only
		// exists precisely to distrust. The combination silently defeats the
		// anti-spoofing guarantee; refuse it rather than pretend to enforce it.
		if c.TenantTrustMode == "derive-only" {
			return fmt.Errorf("ingest_mtls_only is incompatible with tenant_trust_mode: derive-only: a client cert carries no tenant, so ingest cannot enforce per-tenant isolation — bind certs to tenants at the gateway with trust-header, or drop ingest_mtls_only")
		}
	}
	if c.RetentionMaxAge.D() > 0 && c.CompactionInterval.D() <= 0 {
		return fmt.Errorf("retention_max_age is set but retention_compaction_interval is 0: nothing would ever prune")
	}
	if c.BackupDir != "" && c.BackupInterval.D() <= 0 {
		return fmt.Errorf("backup_dir is set but backup_interval is 0: no backup would ever be written")
	}
	if c.LogShipDir != "" && c.LogShipInterval.D() <= 0 {
		return fmt.Errorf("log_shipping_dir is set but log_shipping_interval is 0: no segment would ever be shipped")
	}
	if c.LogShipS3Enabled() {
		if c.LogShipDir != "" {
			return fmt.Errorf("log_shipping_dir and log_shipping_s3_bucket are both set: pick one shipping target")
		}
		if c.LogShipInterval.D() <= 0 {
			return fmt.Errorf("log_shipping_s3_bucket is set but log_shipping_interval is 0: no segment would ever be shipped")
		}
		if c.LogShipS3Endpoint == "" {
			return fmt.Errorf("log_shipping_s3_bucket is set but log_shipping_s3_endpoint is empty")
		}
	}
	if c.IdentityThreshold <= 0 || c.IdentityThreshold > 1 {
		return fmt.Errorf("identity_confidence_threshold must be in (0,1], got %v", c.IdentityThreshold)
	}
	switch strings.ToLower(c.LogLevel) {
	// "warning" is accepted as an alias for "warn" to match SlogLevel, which
	// already maps it — Validate must not reject a level the handler honors.
	case "debug", "info", "warn", "warning", "error":
	default:
		return fmt.Errorf("unknown log_level %q: use debug, info, warn, or error", c.LogLevel)
	}
	switch strings.ToLower(c.LogFormat) {
	case "text", "json":
	default:
		return fmt.Errorf("unknown log_format %q: use text or json", c.LogFormat)
	}
	if _, err := c.TenantTokensMap(); err != nil {
		return err
	}
	if _, err := c.TenantReadTokensMap(); err != nil {
		return err
	}
	if _, err := c.TenantIngestTokensMap(); err != nil {
		return err
	}
	if c.MaxTenants < 0 {
		return fmt.Errorf("max_tenants must be >= 0 (0 = unbounded), got %d", c.MaxTenants)
	}
	switch c.TenantTrustMode {
	case "", "trust-header", "derive-only":
	default:
		return fmt.Errorf("unknown tenant_trust_mode %q: use trust-header or derive-only", c.TenantTrustMode)
	}
	// Tenant ids become X-Scope-OrgID header values and on-disk stack directory
	// names; an empty or whitespace-bearing allowlist entry can never match a
	// real tenant and signals a malformed config rather than a deliberate rule.
	for _, t := range c.TenantAllowlist {
		if t == "" || strings.ContainsAny(t, " \t\r\n") {
			return fmt.Errorf("tenant_allowlist entry %q is invalid: tenant ids must not be empty or contain whitespace", t)
		}
	}
	return nil
}

// TenantTokensMap parses the full-role "tenant:token" pairs into tenant -> tokens.
func (c Config) TenantTokensMap() (map[string][]string, error) {
	return parseTenantTokens(c.TenantTokens, "tenant_tokens")
}

// TenantReadTokensMap / TenantIngestTokensMap parse the role-scoped tenant tokens
// (per-tenant RBAC, ADR 0028): tenant -> read-only / ingest-only tokens.
func (c Config) TenantReadTokensMap() (map[string][]string, error) {
	return parseTenantTokens(c.TenantReadTokens, "tenant_read_tokens")
}
func (c Config) TenantIngestTokensMap() (map[string][]string, error) {
	return parseTenantTokens(c.TenantIngestTokens, "tenant_ingest_tokens")
}

// parseTenantTokens parses "tenant:token" pairs into tenant -> tokens. The tenant
// part must be a canonical tenant id; a malformed pair is a hard error (a typo
// here must not silently widen or narrow access). label names the source field
// in the error.
func parseTenantTokens(pairs []string, label string) (map[string][]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	out := make(map[string][]string, len(pairs))
	for _, pair := range pairs {
		id, token, found := strings.Cut(pair, ":")
		san, ok := tenant.Sanitize(id)
		if !found || !ok || san != id || strings.TrimSpace(token) == "" {
			return nil, fmt.Errorf("invalid %s entry: want \"<tenant>:<token>\" with a canonical tenant id and a non-empty token", label)
		}
		out[id] = append(out[id], token)
	}
	return out, nil
}

// DeriveOnlyTenancy reports whether the derive-only tenant trust mode is in
// effect (ADR 0028): a scoped token's tenant is derived from its binding and a
// client-supplied X-Scope-OrgID / tenant.id is ignored. The empty value is
// trust-header (the default).
func (c Config) DeriveOnlyTenancy() bool { return c.TenantTrustMode == "derive-only" }

// OIDCEnabled reports whether OIDC/JWT verification is configured (an issuer is
// set). Off by default (ADR 0028 / 0030).
func (c Config) OIDCEnabled() bool { return c.OIDCIssuer != "" }

// Default returns the built-in configuration (the lowest-precedence layer). These
// mirror the historical flag defaults: loopback listeners, no retention cap.
func Default() Config {
	return Config{
		Listen:                "127.0.0.1:8080",
		OTLPListen:            "127.0.0.1:4317",
		DataDir:               "toise-data",
		MCPStdio:              false,
		RelationBufferTTL:     Duration(30 * time.Second),
		LivenessSweepInterval: Duration(30 * time.Second),
		RetentionMaxAge:       0,
		CompactionInterval:    Duration(time.Hour),
		SnapshotInterval:      Duration(5 * time.Minute),
		LogFormat:             "text",
		LogLevel:              "info",
		TenantTrustMode:       "trust-header",
		Production:            false,
		GraphQLIntrospection:  true,
		Playground:            true,
		DebugUI:               true,
		TenantAutoCreate:      true,
		IdentityThreshold:     0.9,
		LogShipS3UseSSL:       true,
	}
}

// LogShipS3Enabled reports whether log shipping targets an S3-compatible store
// (selected by setting the bucket) rather than a directory.
func (c Config) LogShipS3Enabled() bool { return c.LogShipS3Bucket != "" }

// LogShipEnabled reports whether continuous log shipping is on (a directory or an
// S3 bucket, with an interval).
func (c Config) LogShipEnabled() bool {
	return (c.LogShipDir != "" || c.LogShipS3Enabled()) && c.LogShipInterval.D() > 0
}

// applyFile overlays the YAML at path onto c, touching only the keys the file
// sets. Unknown keys are rejected so a typo fails loudly rather than being
// silently ignored.
func (c *Config) applyFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading config file %s: %w", path, err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		return fmt.Errorf("parsing config file %s: %w", path, err)
	}
	return nil
}

// applyEnv overlays TOISE_* environment variables onto c. An empty/unset variable
// leaves the current value untouched; a malformed value is a hard error.
func (c *Config) applyEnv(getenv func(string) string) error {
	if v := getenv("TOISE_LISTEN"); v != "" {
		c.Listen = v
	}
	if v := getenv("TOISE_OTLP_LISTEN"); v != "" {
		c.OTLPListen = v
	}
	if v := getenv("TOISE_DATA_DIR"); v != "" {
		c.DataDir = v
	}
	if v := getenv("TOISE_LOG_FORMAT"); v != "" {
		c.LogFormat = v
	}
	if v := getenv("TOISE_LOG_LEVEL"); v != "" {
		c.LogLevel = v
	}
	if v := getenv("TOISE_TENANT_TOKENS"); v != "" {
		c.TenantTokens = splitOrigins(v)
	}
	if v := getenv("TOISE_TENANT_READ_TOKENS"); v != "" {
		c.TenantReadTokens = splitOrigins(v)
	}
	if v := getenv("TOISE_TENANT_INGEST_TOKENS"); v != "" {
		c.TenantIngestTokens = splitOrigins(v)
	}
	if v := getenv("TOISE_ACCEPT_UNKNOWN_TYPES"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("invalid TOISE_ACCEPT_UNKNOWN_TYPES %q: %w", v, err)
		}
		c.AcceptUnknownTypes = b
	}
	if v := getenv("TOISE_TENANT_AUTO_CREATE"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("invalid TOISE_TENANT_AUTO_CREATE %q: %w", v, err)
		}
		c.TenantAutoCreate = b
	}
	if v := getenv("TOISE_TENANT_ALLOWLIST"); v != "" {
		c.TenantAllowlist = splitOrigins(v)
	}
	if v := getenv("TOISE_TENANT_TRUST_MODE"); v != "" {
		c.TenantTrustMode = v
	}
	if v := getenv("TOISE_MAX_TENANTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid TOISE_MAX_TENANTS %q: %w", v, err)
		}
		c.MaxTenants = n
	}
	if v := getenv("TOISE_MCP_STDIO"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("TOISE_MCP_STDIO: invalid bool %q: %w", v, err)
		}
		c.MCPStdio = b
	}
	for _, e := range []struct {
		key string
		dst *Duration
	}{
		{"TOISE_RELATION_BUFFER_TTL", &c.RelationBufferTTL},
		{"TOISE_LIVENESS_SWEEP_INTERVAL", &c.LivenessSweepInterval},
		{"TOISE_RETENTION_MAX_AGE", &c.RetentionMaxAge},
		{"TOISE_RETENTION_COMPACTION_INTERVAL", &c.CompactionInterval},
		{"TOISE_SNAPSHOT_INTERVAL", &c.SnapshotInterval},
		{"TOISE_BACKUP_INTERVAL", &c.BackupInterval},
		{"TOISE_LOG_SHIPPING_INTERVAL", &c.LogShipInterval},
	} {
		if v := getenv(e.key); v != "" {
			parsed, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: invalid duration %q: %w", e.key, v, err)
			}
			*e.dst = Duration(parsed)
		}
	}
	for _, e := range []struct {
		key string
		dst *bool
	}{
		{"TOISE_PRODUCTION", &c.Production},
		{"TOISE_GRAPHQL_INTROSPECTION", &c.GraphQLIntrospection},
		{"TOISE_PLAYGROUND", &c.Playground},
		{"TOISE_DEBUG_UI", &c.DebugUI},
		{"TOISE_INGEST_MTLS_ONLY", &c.IngestMTLSOnly},
	} {
		if v := getenv(e.key); v != "" {
			b, err := strconv.ParseBool(v)
			if err != nil {
				return fmt.Errorf("%s: invalid bool %q: %w", e.key, v, err)
			}
			*e.dst = b
		}
	}
	if v := getenv("TOISE_ALLOWED_ORIGINS"); v != "" {
		c.AllowedOrigins = splitOrigins(v)
	}
	if v := getenv("TOISE_AUTH_TOKENS"); v != "" {
		c.AuthTokens = splitOrigins(v)
	}
	if v := getenv("TOISE_READ_TOKENS"); v != "" {
		c.ReadTokens = splitOrigins(v)
	}
	if v := getenv("TOISE_INGEST_TOKENS"); v != "" {
		c.IngestTokens = splitOrigins(v)
	}
	if v := getenv("TOISE_OIDC_ISSUER"); v != "" {
		c.OIDCIssuer = v
	}
	if v := getenv("TOISE_OIDC_AUDIENCE"); v != "" {
		c.OIDCAudience = v
	}
	if v := getenv("TOISE_OIDC_TENANT_CLAIM"); v != "" {
		c.OIDCTenantClaim = v
	}
	if v := getenv("TOISE_OIDC_ROLE_CLAIM"); v != "" {
		c.OIDCRoleClaim = v
	}
	if v := getenv("TOISE_BACKUP_DIR"); v != "" {
		c.BackupDir = v
	}
	if v := getenv("TOISE_LOG_SHIPPING_DIR"); v != "" {
		c.LogShipDir = v
	}
	if v := getenv("TOISE_LOG_SHIPPING_S3_ENDPOINT"); v != "" {
		c.LogShipS3Endpoint = v
	}
	if v := getenv("TOISE_LOG_SHIPPING_S3_BUCKET"); v != "" {
		c.LogShipS3Bucket = v
	}
	if v := getenv("TOISE_LOG_SHIPPING_S3_REGION"); v != "" {
		c.LogShipS3Region = v
	}
	if v := getenv("TOISE_LOG_SHIPPING_S3_PREFIX"); v != "" {
		c.LogShipS3Prefix = v
	}
	if v := getenv("TOISE_LOG_SHIPPING_S3_USE_SSL"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("TOISE_LOG_SHIPPING_S3_USE_SSL: invalid bool %q: %w", v, err)
		}
		c.LogShipS3UseSSL = b
	}
	if v := getenv("TOISE_LOG_SHIPPING_S3_ACCESS_KEY"); v != "" {
		c.LogShipS3AccessKey = v
	}
	if v := getenv("TOISE_LOG_SHIPPING_S3_SECRET_KEY"); v != "" {
		c.LogShipS3SecretKey = v
	}
	if v := getenv("TOISE_IDENTITY_CONFIDENCE_THRESHOLD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("TOISE_IDENTITY_CONFIDENCE_THRESHOLD: invalid float %q: %w", v, err)
		}
		c.IdentityThreshold = f
	}
	if v := getenv("TOISE_AUDIT_LOG"); v != "" {
		c.AuditLog = v
	}
	if v := getenv("TOISE_TLS_CERT_FILE"); v != "" {
		c.TLSCertFile = v
	}
	if v := getenv("TOISE_TLS_CLIENT_CA_FILE"); v != "" {
		c.TLSClientCAFile = v
	}
	if v := getenv("TOISE_TLS_KEY_FILE"); v != "" {
		c.TLSKeyFile = v
	}
	return nil
}

// splitOrigins parses a comma-separated origin list, trimming spaces and dropping
// empty entries.
func splitOrigins(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Load resolves the configuration from all layers (defaults < file < env < flags)
// and validates it — the server entry point.
func Load(args []string, getenv func(string) string) (Config, error) {
	cfg, err := resolve(args, getenv)
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// LoadCold resolves the configuration without the server-runtime Validate. The
// cold subcommands (checkpoint, drop-snapshot, delete-tenant, restore-log) use it:
// they need only the resolved data dir / shipping target, and must not be blocked
// by run()-time invariants (e.g. a shipping interval) that do not apply to them.
func LoadCold(args []string, getenv func(string) string) (Config, error) {
	return resolve(args, getenv)
}

// resolve applies every layer in precedence order (defaults < file < env < flags)
// and the production lockdown, but does not Validate. getenv is injected (pass
// os.Getenv) so resolution is testable without touching the process environment.
func resolve(args []string, getenv func(string) string) (Config, error) {
	cfg := Default()

	// The config-file path underlays env and flags, so resolve it first — from
	// TOISE_CONFIG, then overridden by an explicit --config on the command line.
	path := getenv("TOISE_CONFIG")
	if p, ok := configPathFromArgs(args); ok {
		path = p
	}
	if path != "" {
		if err := cfg.applyFile(path); err != nil {
			return Config{}, err
		}
	}
	if err := cfg.applyEnv(getenv); err != nil {
		return Config{}, err
	}

	// Flags have the highest precedence. Seeding each flag's default with the
	// file+env-resolved value makes an unset flag keep that value and a set flag
	// win — so precedence falls out of flag.Parse with no per-flag bookkeeping.
	fs := flag.NewFlagSet("toise-server", flag.ContinueOnError)
	configPath := fs.String("config", path, "path to a YAML config file (env: TOISE_CONFIG)")
	listen := fs.String("listen", cfg.Listen, "address for the GraphQL/HTTP server (loopback by default; phase 1 has no auth)")
	otlpListen := fs.String("otlp-listen", cfg.OTLPListen, "address for the OTLP/gRPC ingestion server")
	dataDir := fs.String("data-dir", cfg.DataDir, "directory for the Pebble event log")
	mcpStdio := fs.Bool("mcp-stdio", cfg.MCPStdio, "serve only the MCP server over stdio (for Claude Desktop); no HTTP or OTLP servers")
	relationBufferTTL := fs.Duration("relation-buffer-ttl", cfg.RelationBufferTTL.D(),
		"how long to hold an out-of-order edge waiting for its endpoints before dropping it (0 = disabled)")
	livenessSweepInterval := fs.Duration("liveness-sweep-interval", cfg.LivenessSweepInterval.D(),
		"how often to expire entities past their heartbeat interval (0 = disabled)")
	retentionMaxAge := fs.Duration("retention-max-age", cfg.RetentionMaxAge.D(),
		"maximum age of retained events (0 = unlimited)")
	compactionInterval := fs.Duration("retention-compaction-interval", cfg.CompactionInterval.D(),
		"interval between heartbeat-coalescing compactions")
	snapshotInterval := fs.Duration("snapshot-interval", cfg.SnapshotInterval.D(),
		"how often to snapshot the projection for fast restart (0 = disabled)")
	logFormat := fs.String("log-format", cfg.LogFormat, "log output format: text or json")
	logLevel := fs.String("log-level", cfg.LogLevel, "log level: debug, info, warn, or error")
	production := fs.Bool("production", cfg.Production, "hardening profile: disable introspection, playground, and the debug UI")
	introspection := fs.Bool("graphql-introspection", cfg.GraphQLIntrospection, "expose GraphQL introspection (off under --production)")
	playground := fs.Bool("playground", cfg.Playground, "serve the GraphQL playground at /playground (off under --production)")
	debugUI := fs.Bool("debug-ui", cfg.DebugUI, "serve the debug UI at / (off under --production)")
	allowedOrigins := fs.String("allowed-origins", strings.Join(cfg.AllowedOrigins, ","),
		"comma-separated browser Origin allowlist for WebSocket/CORS (empty = same-origin only)")
	acceptUnknownTypes := fs.Bool("accept-unknown-types", cfg.AcceptUnknownTypes, "accept entity/relation types outside the built-in registry (shape still validated)")
	tenantAutoCreate := fs.Bool("tenant-auto-create", cfg.TenantAutoCreate, "allow a first write to a new tenant id to create its stack")
	tenantAllowlist := fs.String("tenant-allowlist", strings.Join(cfg.TenantAllowlist, ","), "comma-separated tenant ids allowed to be created (empty: any)")
	maxTenants := fs.Int("max-tenants", cfg.MaxTenants, "cap on open tenants, 0 = unbounded")
	tenantTrustMode := fs.String("tenant-trust-mode", cfg.TenantTrustMode, "how a request's tenant is decided: trust-header (default) or derive-only (derive a scoped token's tenant, ignore the client header)")
	oidcIssuer := fs.String("oidc-issuer", cfg.OIDCIssuer, "OIDC issuer URL to verify JWT bearers on the read surfaces; empty = OIDC off")
	oidcAudience := fs.String("oidc-audience", cfg.OIDCAudience, "expected OIDC audience (aud) for JWT verification")
	oidcTenantClaim := fs.String("oidc-tenant-claim", cfg.OIDCTenantClaim, "JWT claim carrying the tenant id (default: tenant)")
	oidcRoleClaim := fs.String("oidc-role-claim", cfg.OIDCRoleClaim, "JWT claim carrying the role read/ingest/full (empty = full)")
	backupDir := fs.String("backup-dir", cfg.BackupDir, "directory for periodic online backups (with --backup-interval); empty = off")
	backupInterval := fs.Duration("backup-interval", cfg.BackupInterval.D(), "interval between online backups of every tenant's event log (0 = off)")
	logShipDir := fs.String("log-shipping-dir", cfg.LogShipDir, "directory to ship event-log segments to (with --log-shipping-interval); may be a mounted bucket/NFS; empty = off")
	logShipInterval := fs.Duration("log-shipping-interval", cfg.LogShipInterval.D(), "interval between event-log segment ships of every tenant (0 = off)")
	identityThreshold := fs.Float64("identity-confidence-threshold", cfg.IdentityThreshold, "same_as confidence (0,1] at/above which an alias joins the canonical view (ADR 0020)")
	auditLog := fs.String("audit-log", cfg.AuditLog, "append-only JSON-line audit file for operator writes (annotate_entity); empty = off")
	tlsCertFile := fs.String("tls-cert-file", cfg.TLSCertFile, "PEM certificate file; with --tls-key-file, serves HTTP and OTLP over TLS")
	tlsKeyFile := fs.String("tls-key-file", cfg.TLSKeyFile, "PEM private key file (pairs with --tls-cert-file)")
	tlsClientCAFile := fs.String("tls-client-ca-file", cfg.TLSClientCAFile, "PEM CA bundle; when set, requires+verifies a client certificate on OTLP ingest (mTLS, needs TLS)")
	ingestMTLSOnly := fs.Bool("ingest-mtls-only", cfg.IngestMTLSOnly, "authenticate OTLP ingest by mTLS alone (no bearer); reads still need their scoped tokens (needs --tls-client-ca-file)")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	_ = *configPath // already consumed via configPathFromArgs; defined for --help and validation
	cfg.Listen = *listen
	cfg.OTLPListen = *otlpListen
	cfg.DataDir = *dataDir
	cfg.MCPStdio = *mcpStdio
	cfg.RelationBufferTTL = Duration(*relationBufferTTL)
	cfg.LivenessSweepInterval = Duration(*livenessSweepInterval)
	cfg.RetentionMaxAge = Duration(*retentionMaxAge)
	cfg.CompactionInterval = Duration(*compactionInterval)
	cfg.SnapshotInterval = Duration(*snapshotInterval)
	cfg.Production = *production
	cfg.GraphQLIntrospection = *introspection
	cfg.Playground = *playground
	cfg.DebugUI = *debugUI
	cfg.AllowedOrigins = splitOrigins(*allowedOrigins)
	cfg.AcceptUnknownTypes = *acceptUnknownTypes
	cfg.TenantAutoCreate = *tenantAutoCreate
	cfg.TenantAllowlist = splitOrigins(*tenantAllowlist)
	cfg.MaxTenants = *maxTenants
	cfg.TenantTrustMode = *tenantTrustMode
	cfg.OIDCIssuer = *oidcIssuer
	cfg.OIDCAudience = *oidcAudience
	cfg.OIDCTenantClaim = *oidcTenantClaim
	cfg.OIDCRoleClaim = *oidcRoleClaim
	cfg.BackupDir = *backupDir
	cfg.BackupInterval = Duration(*backupInterval)
	cfg.LogShipDir = *logShipDir
	cfg.LogShipInterval = Duration(*logShipInterval)
	cfg.IdentityThreshold = *identityThreshold
	cfg.AuditLog = *auditLog
	cfg.TLSCertFile = *tlsCertFile
	cfg.TLSKeyFile = *tlsKeyFile
	cfg.TLSClientCAFile = *tlsClientCAFile
	cfg.IngestMTLSOnly = *ingestMTLSOnly
	cfg.LogFormat = *logFormat
	cfg.LogLevel = *logLevel

	// The production profile is a lockdown: it wins over the individual toggles so
	// "be safe" can never be silently re-opened. For fine-grained control, set the
	// toggles without --production.
	if cfg.Production {
		cfg.GraphQLIntrospection = false
		cfg.Playground = false
		cfg.DebugUI = false
	}
	return cfg, nil
}

// SlogLevel maps the configured log level to a slog.Level. Unknown values fall
// back to Info.
func (c Config) SlogLevel() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// NewLogHandler builds the slog handler for the configured format and level,
// writing to w. An unknown format falls back to text.
func (c Config) NewLogHandler(w io.Writer) slog.Handler {
	opts := &slog.HandlerOptions{Level: c.SlogLevel()}
	if strings.EqualFold(c.LogFormat, "json") {
		return slog.NewJSONHandler(w, opts)
	}
	return slog.NewTextHandler(w, opts)
}

// configPathFromArgs pre-scans args for the --config flag, so the file can be
// loaded before the full flag set is parsed (the file underlays the flags). It
// accepts -config/--config with the value attached (=) or as the next argument.
func configPathFromArgs(args []string) (string, bool) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-config" || a == "--config":
			if i+1 < len(args) {
				return args[i+1], true
			}
		case strings.HasPrefix(a, "-config="):
			return strings.TrimPrefix(a, "-config="), true
		case strings.HasPrefix(a, "--config="):
			return strings.TrimPrefix(a, "--config="), true
		}
	}
	return "", false
}
