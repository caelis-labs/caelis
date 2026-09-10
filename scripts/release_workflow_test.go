package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestReleasePublishesProtectedMainTagsWithoutRepeatingPRQuality(t *testing.T) {
	t.Parallel()

	quality := readWorkflow(t, "../.github/workflows/quality.yml")
	release := readWorkflow(t, "../.github/workflows/release.yml")

	for _, want := range []string{
		"pull_request:\n    branches: [main]",
		"schedule:",
		"if: github.event_name != 'schedule'",
		"contents: read",
		"name: Lint",
		"run: make test",
		"run: make build",
		"govulncheck -mode=source -scan=symbol ./...",
	} {
		if !strings.Contains(quality, want) {
			t.Errorf("quality workflow missing %q", want)
		}
	}
	for _, want := range []string{
		"push:\n    tags:\n      - \"v*\"",
		"group: release\n  cancel-in-progress: false",
		"release:",
		"fetch-depth: 0",
		"persist-credentials: false",
		"name: Verify tag belongs to main",
		`git merge-base --is-ancestor "${GITHUB_SHA}" origin/main`,
		"goreleaser/goreleaser-action@v7",
		"Publish platform packages",
		"Publish main package",
		"publish-r2:",
		"needs: release",
		"R2_ACCESS_KEY_ID",
		"R2_SECRET_ACCESS_KEY",
		"R2_ENDPOINT",
		"./scripts/publish_latest_to_r2.sh",
	} {
		if !strings.Contains(release, want) {
			t.Errorf("release workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"wait-quality:",
		"gh run watch",
		"/actions/workflows/quality.yml/runs",
		"release-validation:",
		"uses: ./.github/workflows/quality.yml",
		"-f status=success",
		"make ",
		"sdk-proxy-smoke",
		"workflow_dispatch:",
		"actions/download-artifact",
		"gh release download",
		"GITHUB_STEP_SUMMARY",
	} {
		if strings.Contains(release, forbidden) {
			t.Errorf("release workflow still contains non-artifact work %q", forbidden)
		}
	}
	for _, forbidden := range []string{
		"push:",
		"workflow_call:",
		"make fmt-check",
		"make vet",
		"make arch-lint",
		"make sdk-boundary-check",
		"make client-protocol-check",
		"make docs-links",
		"make sdk-race",
		"control-race:",
		"product-acceptance:",
		"windows-persistence:",
		"make regression",
		"make sdk-proxy-smoke",
	} {
		if strings.Contains(quality, forbidden) {
			t.Errorf("quality workflow still contains non-core behavior %q", forbidden)
		}
	}
	guard := strings.Index(release, "name: Verify tag belongs to main")
	publish := strings.Index(release, "name: GoReleaser")
	if guard < 0 || publish < 0 || guard >= publish {
		t.Error("release must validate tag ancestry before publishing artifacts")
	}
}

func TestReleaseTagAncestryGuard(t *testing.T) {
	t.Parallel()

	release := readWorkflow(t, "../.github/workflows/release.yml")
	_, step, ok := strings.Cut(release, "      - name: Verify tag belongs to main\n")
	if !ok {
		t.Fatal("release tag guard step missing")
	}
	_, script, ok := strings.Cut(step, "        run: |\n")
	if !ok {
		t.Fatal("release tag guard script missing")
	}
	script, _, _ = strings.Cut(script, "\n      - ")

	dir := t.TempDir()
	hooks := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + hooks}, args...)...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Release Test")
	git("config", "user.email", "release-test@example.invalid")
	git("commit", "--allow-empty", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-b", "unmerged")
	git("commit", "--allow-empty", "-m", "unmerged change")
	unmerged := git("rev-parse", "HEAD")
	git("checkout", "main")
	git("commit", "--allow-empty", "-m", "main advance")
	main := git("rev-parse", "HEAD")
	git("update-ref", "refs/remotes/origin/main", main)

	for _, tc := range []struct {
		name string
		sha  string
		want bool
	}{
		{name: "main tip", sha: main, want: true},
		{name: "main ancestor", sha: base, want: true},
		{name: "unmerged branch", sha: unmerged},
		{name: "missing commit", sha: strings.Repeat("0", 40)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(testBash(t), "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GITHUB_SHA="+tc.sha)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.want {
				t.Fatalf("guard success=%v, want %v: %v\n%s", err == nil, tc.want, err, output)
			}
			if !tc.want && !strings.Contains(string(output), "release tag must point to a commit on main") {
				t.Fatalf("guard did not report rejected tag: %s", output)
			}
		})
	}
}

func TestR2PublisherUpdatesLatestAfterVerifiedAssets(t *testing.T) {
	t.Parallel()

	publisher := readWorkflow(t, "./publish_latest_to_r2.sh")
	for _, want := range []string{
		`/releases/latest`,
		`gh release download "${tag}"`,
		`sha256sum -c checksums.txt`,
		`versioned_prefix="releases/${tag}"`,
		`head-object`,
		`printf '%s\n' "${tag}"`,
		`max-age=60, must-revalidate`,
		`list-objects-v2`,
		`refusing to delete unexpected R2 object`,
	} {
		if !strings.Contains(publisher, want) {
			t.Errorf("R2 publisher missing %q", want)
		}
	}

	latestUpload := strings.Index(publisher, `"s3://${R2_BUCKET}/latest.txt"`)
	assetVerification := strings.Index(publisher, `head-object`)
	cleanup := strings.Index(publisher, `list-objects-v2`)
	if assetVerification < 0 || latestUpload < 0 || cleanup < 0 || assetVerification >= latestUpload || latestUpload >= cleanup {
		t.Error("R2 publisher no longer verifies versioned assets, updates latest.txt, then cleans up old versions")
	}
}

func TestSDKProxySmokeRejectsDisabledProxyEvenWithWarmSharedCache(t *testing.T) {
	t.Parallel()

	command := exec.Command(testBash(t), "./sdk_proxy_smoke.sh")
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

	command := exec.Command(testBash(t), "./sdk_proxy_smoke.sh")
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
	command := exec.Command(testBash(t), "./sdk_proxy_smoke.sh")
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
