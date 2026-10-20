package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	if !strings.Contains(readWorkflow(t, "../.github/workflows/release.yml"), "bash scripts/publish_release_assets.sh") {
		t.Fatal("release assets must attach to the existing release-please release")
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
		"args: release --clean --skip=publish",
		"name: unpublished-archives",
		"  verify-recovery:\n    if: github.event_name == 'workflow_dispatch'",
		"  notarize-darwin:\n    needs: [build, verify-recovery]",
		"run-id: 38420720149",
		"run-id: 38505541188",
		"python3 scripts/verify_release_publication_recovery.py recovery dist",
		"if: ${{ always() && !cancelled()",
		"github.event_name == 'push' && needs.build.result == 'success'",
		"name: macos-signing-checkpoint",
		"bash scripts/sign_notarize_cli.sh",
		"name: verified-release-archives",
		"  release:\n    needs: [notarize-darwin, verify-recovery]",
		"github.event_name == 'workflow_dispatch' && needs.verify-recovery.result == 'success'",
		"bash scripts/verify_release_archives.sh",
		"bash scripts/publish_release_assets.sh",
		"Publish platform packages",
		"Publish main package",
		"publish-r2:",
		"needs: release",
		"!cancelled() && needs.release.result == 'success'",
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
	build := strings.Index(release, "name: Build archives without publishing")
	notarize := strings.Index(release, "name: Sign and notarize both macOS CLIs")
	publish := strings.Index(release, "name: Publish verified GitHub assets")
	if guard < 0 || build < 0 || notarize < 0 || publish < 0 ||
		guard >= build || build >= notarize || notarize >= publish {
		t.Error("release must validate ancestry, build, then notarize before publishing assets")
	}
	for _, want := range []string{
		"APPLE_DEVELOPER_ID_P12_BASE64: ${{ secrets.APPLE_DEVELOPER_ID_P12_BASE64 }}",
		"APPLE_DEVELOPER_ID_P12_PASSWORD: ${{ secrets.APPLE_DEVELOPER_ID_P12_PASSWORD }}",
		"APPLE_NOTARY_APPLE_ID: ${{ secrets.APPLE_NOTARY_APPLE_ID }}",
		"APPLE_NOTARY_PASSWORD: ${{ secrets.APPLE_NOTARY_PASSWORD }}",
		"APPLE_TEAM_ID: ${{ vars.APPLE_TEAM_ID }}",
	} {
		if !strings.Contains(release, want) {
			t.Errorf("macOS notarization workflow missing %q", want)
		}
	}
	for _, forbidden := range []string{"args: release --clean\n", "--clobber"} {
		if strings.Contains(release, forbidden) {
			t.Errorf("tag workflow could bypass notarized archive gate: %q", forbidden)
		}
	}
}

func TestNpmPlatformsCompleteBeforeMainPublication(t *testing.T) {
	t.Parallel()
	quality := readWorkflow(t, "../.github/workflows/quality.yml")
	if !strings.Contains(quality, "name: Test npm distribution\n        if: steps.scope.outputs.full == 'true'\n        run: npm --prefix npm test") {
		t.Fatal("PR checks must exercise the npm publisher before release")
	}
	release := readWorkflow(t, "../.github/workflows/release.yml")
	_, job, ok := strings.Cut(release, "\n  release:\n")
	if !ok {
		t.Fatal("release job missing")
	}
	job, _, _ = strings.Cut(job, "\n  publish-r2:")
	_, platforms, ok := strings.Cut(job, "      - name: Publish platform packages\n")
	if !ok {
		t.Fatal("platform publish step missing")
	}
	platforms, main, ok := strings.Cut(platforms, "      - name: Publish main package\n")
	if !ok {
		t.Fatal("main must be a later step gated by platform success")
	}
	if !strings.Contains(platforms, "publish-release.mjs --concurrency 6") {
		t.Fatal("six platform publications must run concurrently")
	}
	for _, platform := range []string{"darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "windows-arm64", "windows-x64"} {
		if strings.Count(platforms, "./npm/packages/caelis-"+platform) != 1 {
			t.Errorf("platform %s must occur exactly once", platform)
		}
	}
	if !strings.Contains(main, "run: node ./npm/scripts/publish-release.mjs ./npm") {
		t.Fatal("main publication must use the same integrity verifier")
	}
	for _, forbidden := range []string{"continue-on-error", "if:", "|| true", "&"} {
		if strings.Contains(platforms+main, forbidden) {
			t.Errorf("publication must not bypass the platform success gate: %s", forbidden)
		}
	}
	if !strings.Contains(job, "timeout-minutes: 65") {
		t.Fatal("job must budget both platform and main verification deadlines")
	}
}

func TestReleaseArchiveVerificationRejectsMissingOrTamperedAsset(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	version := "1.2.3"
	for _, target := range []string{
		"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64", "windows_arm64",
	} {
		name := "caelis_" + version + "_" + target + ".tar.gz"
		if err := os.WriteFile(dir+"/"+name, []byte(target), 0600); err != nil {
			t.Fatal(err)
		}
	}
	verify := func(extra ...string) error {
		t.Helper()
		args := append([]string{"./verify_release_archives.sh", version, dir}, extra...)
		cmd := exec.Command(testBash(t), args...)
		cmd.Dir = "../scripts"
		return cmd.Run()
	}
	if err := verify("--names-only"); err != nil {
		t.Fatalf("six archive names: %v", err)
	}
	checksum := exec.Command(testBash(t), "-c", "shasum -a 256 caelis_*.tar.gz > checksums.txt")
	checksum.Dir = dir
	if output, err := checksum.CombinedOutput(); err != nil {
		t.Fatalf("prepare checksums: %v: %s", err, output)
	}
	if err := verify(); err != nil {
		t.Fatalf("valid final archives: %v", err)
	}
	if err := os.WriteFile(dir+"/caelis_"+version+"_darwin_arm64.tar.gz", []byte("changed after signing"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verify(); err == nil {
		t.Fatal("tampered macOS archive passed final checksum gate")
	}
}

func TestReleaseAssetRetryVerifiesBytesBeforeSkippingOrUploading(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dist := filepath.Join(root, "dist")
	state := filepath.Join(root, "state")
	bin := filepath.Join(root, "bin")
	for _, dir := range []string{dist, state, bin, filepath.Join(state, "assets")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, target := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64", "windows_arm64"} {
		name := "caelis_1.2.3_" + target + ".tar.gz"
		if err := os.WriteFile(filepath.Join(dist, name), []byte(target), 0600); err != nil {
			t.Fatal(err)
		}
	}
	checksum := exec.Command(testBash(t), "-c", "shasum -a 256 caelis_*.tar.gz > checksums.txt")
	checksum.Dir = dist
	if output, err := checksum.CombinedOutput(); err != nil {
		t.Fatalf("prepare checksums: %v: %s", err, output)
	}
	fakeGH := `#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == api ]]; then
  for file in "$FAKE_GH_STATE"/assets/*; do
    [[ -f "$file" ]] || continue
    printf '%s\tsha256:%s\tuploaded\n' "${file##*/}" "$(shasum -a 256 "$file" | awk '{print $1}')"
  done
elif [[ "$1" == release && "$2" == upload ]]; then
  file=$4
  name=${file##*/}
  [[ ! -e "$FAKE_GH_STATE/assets/$name" ]] || exit 1
  count=$(wc -l < "$FAKE_GH_STATE/uploads" | tr -d '[:space:]')
  [[ "${FAKE_GH_FAIL_AT:-}" != "$((count + 1))" ]] || exit 1
  cp "$file" "$FAKE_GH_STATE/assets/$name"
  printf '%s\n' "$name" >> "$FAKE_GH_STATE/uploads"
else
  exit 2
fi
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "uploads"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(failAt string) (string, error) {
		t.Helper()
		cmd := exec.Command(testBash(t), "./publish_release_assets.sh", "v1.2.3", dist)
		cmd.Dir = "../scripts"
		cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE_GH_STATE="+state, "FAKE_GH_FAIL_AT="+failAt, "GITHUB_REPOSITORY=caelis-labs/caelis")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if _, err := run("4"); err == nil {
		t.Fatal("expected interruption after three uploaded assets")
	}
	if output, err := run(""); err != nil {
		t.Fatalf("retry must fill missing assets: %v: %s", err, output)
	}
	if output, err := run(""); err != nil {
		t.Fatalf("identical retry must skip assets: %v: %s", err, output)
	}
	uploads, err := os.ReadFile(filepath.Join(state, "uploads"))
	if err != nil || len(strings.Split(strings.TrimSpace(string(uploads)), "\n")) != 7 {
		t.Fatalf("wanted seven distinct uploads, got %q: %v", uploads, err)
	}
	changed := filepath.Join(dist, "caelis_1.2.3_darwin_arm64.tar.gz")
	if err := os.WriteFile(changed, []byte("new timestamped signature"), 0600); err != nil {
		t.Fatal(err)
	}
	checksum = exec.Command(testBash(t), "-c", "shasum -a 256 caelis_*.tar.gz > checksums.txt")
	checksum.Dir = dist
	if output, err := checksum.CombinedOutput(); err != nil {
		t.Fatalf("recompute checksums: %v: %s", err, output)
	}
	if output, err := run(""); err == nil || !strings.Contains(output, "differs from the verified local bytes") {
		t.Fatalf("different signed asset must fail without overwrite: %v: %s", err, output)
	}
	uploadsAfter, err := os.ReadFile(filepath.Join(state, "uploads"))
	if err != nil || string(uploadsAfter) != string(uploads) {
		t.Fatalf("mismatched retry uploaded files: %q: %v", uploadsAfter, err)
	}
}

func TestMacOSCLIArchivesFollowAcceptedNotarization(t *testing.T) {
	t.Parallel()
	script := readWorkflow(t, "./sign_notarize_cli.sh")
	for _, want := range []string{
		"for arch in amd64 arm64; do",
		"--options runtime --timestamp",
		"TeamIdentifier=$APPLE_TEAM_ID",
		"^CodeDirectory .*flags=.*runtime",
		"^Timestamp=",
		"ditto -c -k --keepParent",
		"submission.zip",
		"notarytool submit",
		"notarytool wait",
		"--timeout 60m --output-format json",
		"notarytool log",
		"log.get('sha256')",
		"tar -czf",
		"verify_release_archives.sh",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("macOS release gate missing %q", want)
		}
	}
	if strings.Contains(script, "stapler staple") {
		t.Error("bare CLI and ZIP do not support stapling")
	}
	sign := strings.Index(script, "--options runtime --timestamp")
	checkpoint := strings.Index(script, "cp \"$temp/notary.zip\" \"$dist/notarization/submission.zip\"")
	submit := strings.Index(script, "notarytool submit")
	accepted := strings.Index(script, "log.get('sha256')")
	repackage := strings.Index(script, "tar -czf")
	if sign < 0 || checkpoint < 0 || submit < 0 || accepted < 0 || repackage < 0 ||
		sign >= checkpoint || checkpoint >= submit || submit >= accepted || accepted >= repackage {
		t.Error("macOS archives must contain signed bytes only after Apple accepts the matching submission")
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
		"sudo apt-get install -y bubblewrap", "CAELIS_LINUX_SANDBOX_SMOKE_E2E: '1'",
		"go test -count=1 -timeout=3m ./agent-sdk/sandbox/bwrap",
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

func TestCommitCheckIncludesLintWithoutFullPRQuality(t *testing.T) {
	t.Parallel()
	makefile := readWorkflow(t, "../Makefile")
	for _, want := range []string{"commit-check: fmt-check lint\n", "git diff --check", "git diff --cached --check", "quality: lint test build"} {
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
		"uses: actions/setup-python@v7", "python-version: '3.13'",
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
		"CAELIS_WINDOWS_SANDBOX_E2E=1 bash ./scripts/go_test_nonempty.sh",
		"./agent-sdk/sandbox/windows '^TestRestrictedToken(ExecutionConfig|PythonEnvironment)E2E$' windows-execution-config -count=1",
		"CGO_ENABLED=0 go build ./...",
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
