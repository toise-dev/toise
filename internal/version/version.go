// Package version exposes build-time version information for the Toise server.
//
// The values are overridden at build time with -ldflags, for example:
//
//	go build -ldflags "-X github.com/toise-dev/toise/internal/version.Version=0.1.0 \
//	  -X github.com/toise-dev/toise/internal/version.Commit=$(git rev-parse --short HEAD)"
package version

// Build-time information. Defaults are used for development builds.
var (
	// Version is the semantic version of the build.
	Version = "0.0.0-dev"
	// Commit is the short git commit hash of the build.
	Commit = "unknown"
)

// String returns a human-readable "<version> (<commit>)" string.
func String() string {
	return Version + " (" + Commit + ")"
}
