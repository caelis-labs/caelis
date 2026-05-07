package caelis_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestReleaseVersionReferencesStayAligned(t *testing.T) {
	versionBytes, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("ReadFile(VERSION) error = %v", err)
	}
	releaseTag := strings.TrimSpace(string(versionBytes))
	if !strings.HasPrefix(releaseTag, "v") {
		t.Fatalf("VERSION = %q, want leading v", releaseTag)
	}
	npmVersion := strings.TrimPrefix(releaseTag, "v")

	packageBytes, err := os.ReadFile("npm/package.json")
	if err != nil {
		t.Fatalf("ReadFile(npm/package.json) error = %v", err)
	}
	var pkg struct {
		Version              string            `json:"version"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(packageBytes, &pkg); err != nil {
		t.Fatalf("json.Unmarshal(npm/package.json) error = %v", err)
	}
	if pkg.Version != npmVersion {
		t.Fatalf("npm/package.json version = %q, want %q", pkg.Version, npmVersion)
	}
	for name, got := range pkg.OptionalDependencies {
		if got != npmVersion {
			t.Fatalf("optional dependency %s version = %q, want %q", name, got, npmVersion)
		}
	}

	for _, path := range []string{"README.md", "npm/README.md"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", path, err)
		}
		text := string(data)
		want := "@onslaughtsnail/caelis@" + npmVersion
		if !strings.Contains(text, want) {
			t.Fatalf("%s missing install example %q", path, want)
		}
		stale := "@onslaughtsnail/caelis@0.1.3"
		if strings.Contains(text, stale) {
			t.Fatalf("%s contains stale install example %q", path, stale)
		}
	}
}
