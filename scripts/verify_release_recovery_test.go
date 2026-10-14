package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The recovery guard checks GitHub's source run and release before the new
// workflow can sign or publish. All network responses here are synthetic.
func TestReleaseRecoveryGuard(t *testing.T) {
	for _, tc := range []struct {
		name, want string
	}{
		{"valid", "Verified v0.71.1"},
		{"changed-digest", "artifact is missing, expired or changed"},
		{"existing-assets", "release already has assets"},
		{"prior-recovery", "a recovery run already exists"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			fake := map[string]string{
				"git": `#!/usr/bin/env bash
case "$1" in
  rev-parse) echo 5e232e817cdd62186a19dd1d3a43aa5476326a74 ;;
  merge-base|diff) exit 0 ;;
  *) exit 2 ;;
esac
`,
				"gh": `#!/usr/bin/env bash
case "$2" in
  */runs/38420720149)
    echo '{"head_sha":"5e232e817cdd62186a19dd1d3a43aa5476326a74","head_branch":"v0.71.1","event":"push","status":"completed","conclusion":"failure","path":".github/workflows/release.yml"}' ;;
  */runs/38420720149/jobs*)
    echo '{"jobs":[{"name":"build","conclusion":"success"},{"name":"notarize-darwin","conclusion":"failure"},{"name":"release","conclusion":"skipped"},{"name":"publish-r2","conclusion":"skipped"}]}' ;;
  */runs/38420720149/artifacts*)
    digest=sha256:82c56458a1d95e591b288d69b1860f2cc777031d873d217e6b5fe7e9fe3dab6e
    [[ "$FAKE_CASE" != changed-digest ]] || digest=sha256:changed
    printf '{"total_count":1,"artifacts":[{"name":"unpublished-archives","digest":"%s","size_in_bytes":184477179,"expired":false}]}\n' "$digest" ;;
  */releases/tags/v0.71.1)
    assets='[]'
    [[ "$FAKE_CASE" != existing-assets ]] || assets='[{"name":"checksums.txt"}]'
    printf '{"tag_name":"v0.71.1","draft":false,"prerelease":false,"assets":%s}\n' "$assets" ;;
  */releases/latest)
    echo '{"tag_name":"v0.71.1"}' ;;
  */workflows/release.yml/runs*)
    if [[ "$FAKE_CASE" == prior-recovery ]]; then
      echo '{"total_count":2,"workflow_runs":[{"id":123},{"id":122}]}'
    else
      echo '{"total_count":1,"workflow_runs":[{"id":123}]}'
    fi ;;
  *) echo "unexpected API: $2" >&2; exit 2 ;;
esac
`,
			}
			for name, content := range fake {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(testBash(t), "./verify_release_recovery.sh")
			cmd.Dir = "../scripts"
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"),
				"FAKE_CASE="+tc.name, "GITHUB_EVENT_NAME=workflow_dispatch",
				"GITHUB_REF=refs/heads/main", "GITHUB_REPOSITORY=caelis-labs/caelis",
				"GITHUB_RUN_ATTEMPT=1", "GITHUB_RUN_ID=123",
				"RELEASE_TAG=v0.71.1", "GH_TOKEN=synthetic")
			output, err := cmd.CombinedOutput()
			if (err == nil) != (tc.name == "valid") || !strings.Contains(string(output), tc.want) {
				t.Fatalf("recovery %s: %v: %s", tc.name, err, output)
			}
		})
	}
}
