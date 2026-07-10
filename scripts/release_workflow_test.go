package main

import (
	"os"
	"strings"
	"testing"
)

func TestReleaseWaitsForReusableQualityOnSameSHA(t *testing.T) {
	t.Parallel()

	quality := readWorkflow(t, "../.github/workflows/quality.yml")
	release := readWorkflow(t, "../.github/workflows/release.yml")

	for _, want := range []string{
		"workflow_call:",
		"make sdk-race",
		"make regression",
		"make docs-links",
		"make sdk-proxy-smoke",
	} {
		if !strings.Contains(quality, want) {
			t.Errorf("quality workflow missing %q", want)
		}
	}
	for _, want := range []string{
		"quality:",
		"uses: ./.github/workflows/quality.yml",
		"sdk_proxy_version: ${{ startsWith(github.ref, 'refs/tags/v') && github.ref_name || '' }}",
		"needs: quality",
	} {
		if !strings.Contains(release, want) {
			t.Errorf("release workflow missing %q", want)
		}
	}
}

func readWorkflow(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
