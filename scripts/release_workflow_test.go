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
		"pull_request:",
		"push:",
		"workflow_call:",
		"make sdk-boundary-check",
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

func TestSDKConsumerGatesUseCurrentAndTaggedFixturesSeparately(t *testing.T) {
	t.Parallel()

	current := readWorkflow(t, "./sdk_boundary_check.sh")
	if !strings.Contains(current, "scripts/testdata/sdk_consumer/quickstart_test.go") || !strings.Contains(current, "go mod edit -replace") {
		t.Fatalf("current-source consumer gate no longer compiles the worktree quickstart with a local module replacement")
	}
	tagged := readWorkflow(t, "./sdk_proxy_smoke.sh")
	for _, want := range []string{
		"git show \"${VERSION}:scripts/testdata/sdk_consumer/quickstart_test.go\"",
		"git show \"${VERSION}:agent-sdk/supported-packages.txt\"",
		"go list -m",
		"replace directive",
		"with no replacement",
	} {
		if !strings.Contains(tagged, want) {
			t.Errorf("tagged consumer gate missing %q", want)
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
