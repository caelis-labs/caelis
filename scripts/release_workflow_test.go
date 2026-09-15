package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

func TestReleasePleaseFeedsProtectedMainPublication(t *testing.T) {
	t.Parallel()

	workflow := readWorkflow(t, "../.github/workflows/release-please.yml")
	for _, want := range []string{
		"push:\n    branches: [main]",
		"if: github.ref == 'refs/heads/main'",
		"group: release-please-main\n  cancel-in-progress: false",
		"token: ${{ secrets.RELEASE_PLEASE_TOKEN }}",
		"target-branch: main",
		"config-file: release-please-config.json",
		"manifest-file: .release-please-manifest.json",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("release-please workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{"secrets.GITHUB_TOKEN", "github.token", "--auto", "skip-github-release: true"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("release-please must trigger downstream checks and leave merging to maintainers: %q", forbidden)
		}
	}

	var config struct {
		Packages     map[string]map[string]any `json:"packages"`
		AlwaysUpdate bool                      `json:"always-update"`
	}
	if err := json.Unmarshal([]byte(readWorkflow(t, "../release-please-config.json")), &config); err != nil {
		t.Fatal(err)
	}
	root := config.Packages["."]
	if len(config.Packages) != 1 || root["release-type"] != "go" ||
		root["include-component-in-tag"] != false || root["include-v-in-tag"] != true {
		t.Fatal("release-please must use one root Go version with vX.Y.Z tags")
	}
	if config.AlwaysUpdate {
		t.Fatal("release PR must not be refreshed solely to catch up with main")
	}
	if root["draft"] == true || root["skip-github-release"] == true {
		t.Fatal("release-please must create a published release and tag to trigger artifact publication")
	}
	var manifest map[string]string
	if err := json.Unmarshal([]byte(readWorkflow(t, "../.release-please-manifest.json")), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest) != 1 || !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(manifest["."]) {
		t.Fatal("release manifest must contain one root release version")
	}
	if !strings.Contains(readWorkflow(t, "../.goreleaser.yml"), "release:\n  mode: keep-existing") {
		t.Fatal("GoReleaser must preserve the release-please changelog")
	}
}

func TestReleasePublishesProtectedMainTagsWithoutRepeatingPRQuality(t *testing.T) {
	t.Parallel()

	quality := readWorkflow(t, "../.github/workflows/quality.yml")
	release := readWorkflow(t, "../.github/workflows/release.yml")

	for _, want := range []string{
		"pull_request:\n    branches: [main]",
		"if: needs.changes.outputs.full == 'true'",
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
		"schedule:",
		"workflow_dispatch:",
		"release-ci-approval",
		"environment: release-ci",
		"windows-host-open:",
		"go test -race",
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

func TestScopedQualityFailsClosed(t *testing.T) {
	t.Parallel()
	quality := readWorkflow(t, "../.github/workflows/quality.yml")
	for _, want := range []string{
		"fetch-depth: 2", "PR_BASE_SHA: ${{ github.event.pull_request.base.sha }}",
		"node --test scripts/ci_scope.test.mjs", "node scripts/ci_scope.mjs",
		"if: steps.scope.outputs.docs == 'true'", "go run ./scripts/markdown_links",
		"needs: [changes, govulncheck, go-quality]", "if: always()",
		"CHANGES_RESULT: ${{ needs.changes.result }}", "FULL: ${{ needs.changes.outputs.full }}",
		"VULN_RESULT: ${{ needs.govulncheck.result }}", "GO_RESULT: ${{ needs.go-quality.result }}",
		"bash scripts/ci_result.sh",
	} {
		if !strings.Contains(quality, want) {
			t.Errorf("scoped quality workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{"paths-ignore:", "paths:", "github.actor", "github.head_ref"} {
		if strings.Contains(quality, forbidden) {
			t.Errorf("quality must inspect the complete diff regardless of author: %q", forbidden)
		}
	}
	for _, full := range []string{"true", "false"} {
		expected := "success"
		if full == "false" {
			expected = "skipped"
		}
		baseline := map[string]string{"CHANGES_RESULT": "success", "FULL": full, "VULN_RESULT": expected, "GO_RESULT": expected}
		run := func(values map[string]string, want bool) {
			t.Helper()
			cmd := exec.Command(testBash(t), "./ci_result.sh")
			cmd.Env = os.Environ()
			for key, value := range values {
				cmd.Env = append(cmd.Env, key+"="+value)
			}
			output, err := cmd.CombinedOutput()
			if (err == nil) != want {
				t.Fatalf("guard %v: success=%v, want %v: %s", values, err == nil, want, output)
			}
		}
		run(baseline, true)
		for key, correct := range baseline {
			for _, bad := range []string{"", "unknown", "failure", "cancelled", "skipped", "success"} {
				if bad == correct {
					continue
				}
				values := make(map[string]string, len(baseline))
				for k, v := range baseline {
					values[k] = v
				}
				values[key] = bad
				run(values, false)
			}
		}
	}
}

func TestCommitCheckDoesNotRepeatFullPRQuality(t *testing.T) {
	t.Parallel()
	makefile := readWorkflow(t, "../Makefile")
	for _, want := range []string{"commit-check: fmt-check", "git diff --check", "git diff --cached --check", "quality: lint test build"} {
		if !strings.Contains(makefile, want) {
			t.Errorf("local checkpoint missing %q", want)
		}
	}
	for _, forbidden := range []string{"commit-check: quality", "commit-check: windows-check"} {
		if strings.Contains(makefile, forbidden) {
			t.Errorf("local checkpoint repeats full CI: %q", forbidden)
		}
	}
}

func TestWindowsQualityUsesFocusedNativeGate(t *testing.T) {
	t.Parallel()

	quality := readWorkflow(t, "../.github/workflows/platform-checks.yml")
	for _, want := range []string{"schedule:", "workflow_dispatch:", "go test -race", "govulncheck -mode=source -scan=symbol ./..."} {
		if !strings.Contains(quality, want) {
			t.Errorf("platform checks missing %q", want)
		}
	}
	for _, forbidden := range []string{"push:", "pull_request:"} {
		if strings.Contains(quality, forbidden) {
			t.Errorf("platform checks run on every change: %q", forbidden)
		}
	}
	_, windows, ok := strings.Cut(quality, "\n  windows-host-open:\n")
	if !ok {
		t.Fatal("required windows-host-open check missing")
	}
	windows = regexp.MustCompile(`(?m)^  \S`).Split(windows, 2)[0]
	for _, want := range []string{
		"runs-on: windows-2022",
		"GOWORK: 'off'",
		"GOFLAGS: -mod=readonly -p=2",
		"run: make windows-check",
	} {
		if !strings.Contains(windows, want) {
			t.Errorf("Windows quality check missing %q", want)
		}
	}
	for _, forbidden := range []string{"golangci-lint-action", "run: make test", "go test ./...", "GO_TEST_TIMEOUT=15m"} {
		if strings.Contains(windows, forbidden) {
			t.Errorf("Windows quality check repeats broad validation %q", forbidden)
		}
	}
	gate := readWorkflow(t, "./windows_check.sh")
	for _, want := range []string{
		`"$(go env GOHOSTOS)" != windows`,
		"CGO_ENABLED=0 go build ./...",
		"CGO_ENABLED=0 GO_TEST_TIMEOUT=10m bash ./scripts/go_test_nonempty.sh",
		"./app/gatewayapp/internal/memoryhost '^TestEmbeddedHostBindsSDKClient$' windows-memory-open -count=1",
	} {
		if !strings.Contains(gate, want) {
			t.Errorf("Windows release configuration check missing %q", want)
		}
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
