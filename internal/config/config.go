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
	LogFormat             string   `yaml:"log_format"` // "text" or "json"
	LogLevel              string   `yaml:"log_level"`  // debug | info | warn | error
}

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
		LogFormat:             "text",
		LogLevel:              "info",
	}
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
	} {
		if v := getenv(e.key); v != "" {
			parsed, err := time.ParseDuration(v)
			if err != nil {
				return fmt.Errorf("%s: invalid duration %q: %w", e.key, v, err)
			}
			*e.dst = Duration(parsed)
		}
	}
	return nil
}

// Load resolves the configuration from all layers in precedence order. getenv is
// injected (pass os.Getenv) so the resolution is testable without touching the
// process environment.
func Load(args []string, getenv func(string) string) (Config, error) {
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
	logFormat := fs.String("log-format", cfg.LogFormat, "log output format: text or json")
	logLevel := fs.String("log-level", cfg.LogLevel, "log level: debug, info, warn, or error")
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
	cfg.LogFormat = *logFormat
	cfg.LogLevel = *logLevel
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
