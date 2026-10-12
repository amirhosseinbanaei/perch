// Package version holds the build metadata set through -ldflags -X.
package version

// These are the only package-level variables in the module. scripts/release.sh sets them:
//
//	-X github.com/amirhosseinbanaei/tmux-manager-cli/internal/version.Version=0.1.0
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
