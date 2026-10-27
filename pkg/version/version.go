// Package version provides build-time version metadata for all Meridian binaries.
// This is critical for telemetry, observability, and debugging fleet control-plane rollouts.
package version

import (
	"fmt"
	"runtime"
)

// Info contains versioning information for Meridian components.
// It is populated at build time using ldflags or falls back to sane development defaults.
type Info struct {
	// Version is the semantic version string (e.g., "v0.1.0" or "dev").
	Version string `json:"version"`
	// GitCommit is the git SHA from which the binary was compiled.
	GitCommit string `json:"gitCommit"`
	// BuildDate is the ISO-8601 timestamp when the binary was built.
	BuildDate string `json:"buildDate"`
	// GoVersion is the Go compiler runtime version used for compilation.
	GoVersion string `json:"goVersion"`
	// Platform indicates the OS and architecture (e.g., "linux/amd64").
	Platform string `json:"platform"`
}

var (
	// version is populated at build time via -ldflags "-X github.com/tijani-web/meridian/pkg/version.version=...".
	version = "v0.1.0-dev"
	// gitCommit is populated at build time via -ldflags "-X github.com/tijani-web/meridian/pkg/version.gitCommit=...".
	gitCommit = "unknown"
	// buildDate is populated at build time via -ldflags "-X github.com/tijani-web/meridian/pkg/version.buildDate=...".
	buildDate = "unknown"
)

// Get returns the complete build and version metadata for the running binary.
// Callers use this to populate metrics (e.g. meridian_build_info) and CLI version subcommands.
func Get() Info {
	return Info{
		Version:   version,
		GitCommit: gitCommit,
		BuildDate: buildDate,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

// String formats the version info as a human-readable one-line string.
func (i Info) String() string {
	return fmt.Sprintf("Meridian %s (commit: %s, built: %s, %s, %s)",
		i.Version, i.GitCommit, i.BuildDate, i.GoVersion, i.Platform)
}
