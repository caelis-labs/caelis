package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReleaseWaitsForExactSHAMainQualityBeforeArtifacts(t *testing.T) {
	t.Parallel()

	quality := readWorkflow(t, "../.github/workflows/quality.yml")
	release := readWorkflow(t, "../.github/workflows/release.yml")

	for _, want := range []string{
		"pull_request:",
		"push:",
		"make sdk-boundary-check",
		"make sdk-race",
		"make docs-links",
	} {
		if !strings.Contains(quality, want) {
			t.Errorf("quality workflow missing %q", want)
		}
	}
	for _, want := range []string{
		"wait-quality:",
		"actions: read",
		"checks: read",
		"-f branch=main",
		"-f event=push",
		"-f head_sha=\"${GITHUB_SHA}\"",
		"/actions/workflows/quality.yml/runs",
		"for attempt in {1..30}",
		"gh run watch \"${quality_run_id}\"",
		"--interval 10",
		"--exit-status",
		"release:",
		"needs: wait-quality",
		"goreleaser/goreleaser-action@v7",
		"Publish platform packages",
		"Publish main package",
	} {
		if !strings.Contains(release, want) {
			t.Errorf("release workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"release-validation:",
		"uses: ./.github/workflows/quality.yml",
		"-f status=success",
		"make ",
		"sdk-proxy-smoke",
		"workflow_dispatch:",
	} {
		if strings.Contains(release, forbidden) {
			t.Errorf("release workflow still contains non-artifact work %q", forbidden)
		}
	}
	if !strings.Contains(quality, "sdk-race:\n    if: github.event_name == 'pull_request'") {
		t.Error("focused SDK race is not scoped to pull requests")
	}
	for _, forbidden := range []string{"workflow_call:", "make regression", "make sdk-proxy-smoke"} {
		if strings.Contains(quality, forbidden) {
			t.Errorf("quality workflow still contains release-only behavior %q", forbidden)
		}
	}
}

func TestReleaseDryRunDoesNotRepeatOrdinaryQuality(t *testing.T) {
	t.Parallel()

	makefile := readWorkflow(t, "../Makefile")
	if !strings.Contains(makefile, "release-dry-run: cache-dirs") {
		t.Error("release dry run no longer initializes its local cache directories")
	}
	if strings.Contains(makefile, "release-dry-run: quality") {
		t.Error("release dry run repeats the ordinary quality gate")
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
		"git tag --merged HEAD --sort=-v:refname",
		"git show \"${VERSION}:scripts/testdata/sdk_consumer/quickstart_test.go\"",
		"git show \"${VERSION}:agent-sdk/supported-packages.txt\"",
		"go list -m",
		"replace directive",
		"with no replacement",
		"GOMODCACHE=\"${consumer_modcache}\"",
		"no direct/off/pipe fallback",
		"GOPRIVATE=",
		"GONOPROXY=none",
		"GOVCS='*:off'",
	} {
		if !strings.Contains(tagged, want) {
			t.Errorf("tagged consumer gate missing %q", want)
		}
	}
	if strings.Contains(tagged, "sdk_api_compat") {
		t.Error("tagged consumer gate still depends on the removed declaration-compatibility command")
	}
}

func TestSDKProxySmokeRejectsDisabledProxyEvenWithWarmSharedCache(t *testing.T) {
	t.Parallel()

	command := exec.Command("bash", "./sdk_proxy_smoke.sh")
	command.Env = append(os.Environ(),
		"SDK_PROXY_VERSION=v0.25.0",
		"SDK_PROXY_URL=off",
		"GOMODCACHE="+t.TempDir(),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("sdk proxy smoke succeeded with disabled proxy: %s", output)
	}
	if !strings.Contains(string(output), "no direct/off/pipe fallback") {
		t.Fatalf("unexpected failure: %s", output)
	}
}

func TestSDKProxySmokeRejectsPipeDirectFallback(t *testing.T) {
	t.Parallel()

	command := exec.Command("bash", "./sdk_proxy_smoke.sh")
	command.Env = append(os.Environ(),
		"SDK_PROXY_VERSION=v0.25.0",
		"SDK_PROXY_URL=https://127.0.0.1:1|direct",
		"GOMODCACHE="+t.TempDir(),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("sdk proxy smoke succeeded with pipe direct fallback: %s", output)
	}
	if !strings.Contains(string(output), "no direct/off/pipe fallback") {
		t.Fatalf("unexpected failure: %s", output)
	}
}

func TestSDKProxySmokeCannotUseAmbientPrivateModuleBypass(t *testing.T) {
	t.Parallel()
	command := exec.Command("bash", "./sdk_proxy_smoke.sh")
	command.Env = append(os.Environ(),
		"SDK_PROXY_VERSION=v0.25.0",
		"SDK_PROXY_URL=https://127.0.0.1:1",
		"GOPRIVATE=github.com/caelis-labs/*",
		"GONOPROXY=github.com/caelis-labs/*",
		"GOFLAGS=-x",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("sdk proxy smoke bypassed the dead evidence proxy: %s", output)
	}
	if strings.Contains(string(output), "git ls-remote") {
		t.Fatalf("sdk proxy smoke reached VCS through ambient private settings: %s", output)
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
