// Package version exposes build-time version information for the Toise server.
//
// The values are overridden at build time with -ldflags, for example:
//
//	go build -ldflags "-X github.com/toise-dev/toise/internal/version.Version=0.1.0 \
//	  -X github.com/toise-dev/toise/internal/version.Commit=$(git rev-parse HEAD) \
//	  -X github.com/toise-dev/toise/internal/version.Built=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
package version

// Build-time information. Defaults are used for development builds.
var (
	// Version is the semantic version of the build.
	Version = "0.0.0-unknown"
	// Commit is the full git commit hash of the build.
	Commit = "unknown"
	// Built is the UTC build timestamp in RFC 3339 format.
	Built = "unknown"
)

// Info is the version information served by GET /version.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
}

// Get returns the current build-time version information.
func Get() Info {
	return Info{Version: Version, Commit: Commit, Built: Built}
}

// String returns a short human-readable "<version> (<commit>)" string, with
// the commit truncated to 7 characters when it is long enough.
func String() string {
	commit := Commit
	if len(commit) >= 7 {
		commit = commit[:7]
	}
	return Version + " (" + commit + ")"
}

// Line returns the single-line output of `toise-server version`: version,
// full commit, and UTC build timestamp.
func Line() string {
	return Version + " " + Commit + " " + Built
}
