package version_test

import (
	"strings"
	"testing"

	"github.com/tijani-web/meridian/pkg/version"
)

// TestGet verifies that Get returns non-empty runtime metadata and handles defaults gracefully.
func TestGet(t *testing.T) {
	info := version.Get()

	if info.Version == "" {
		t.Errorf("expected non-empty Version")
	}
	if info.GoVersion == "" {
		t.Errorf("expected non-empty GoVersion")
	}
	if !strings.HasPrefix(info.GoVersion, "go") {
		t.Errorf("expected GoVersion to start with 'go', got %s", info.GoVersion)
	}
	if info.Platform == "" {
		t.Errorf("expected non-empty Platform")
	}

	str := info.String()
	if !strings.Contains(str, "Meridian") {
		t.Errorf("expected string representation to include 'Meridian', got: %s", str)
	}
}
