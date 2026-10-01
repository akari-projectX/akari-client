// Package version holds build metadata injected via -ldflags.
package version

// Set at build time:
//
//	-X github.com/akari-projectX/akari-client/internal/version.Version=v0.1.0
//	-X github.com/akari-projectX/akari-client/internal/version.Commit=abc123
var (
	Version = "dev"
	Commit  = "unknown"
)

// UserAgent is sent on every subscription fetch. The panel selects the
// Clash format when the UA contains "mihomo" (or "clash").
func UserAgent() string { return "akari-client/" + Version + " mihomo" }
