package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestR2V0791RecoveryGuard(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong-tag", "unfinished", "notarization-failed", "publication-failed", "wrong-branch"} {
		t.Run(scenario, func(t *testing.T) {
			bin := t.TempDir()
			for name, script := range map[string]string{
				"git": `#!/usr/bin/env bash
set -eu
if [[ "$1" == rev-parse ]]; then
 if [[ "$SCENARIO" == wrong-tag ]]; then echo wrong; else echo 048c0d479f663caa31880fdb3c6d5e33734de8be; fi
elif [[ "$1" != merge-base ]]; then exit 1; fi
`,
				"gh": `#!/usr/bin/env bash
set -eu
if [[ "$2" == */jobs* ]]; then
 notary=success; publication=success
 [[ "$SCENARIO" != notarization-failed ]] || notary=failure
 [[ "$SCENARIO" != publication-failed ]] || publication=failure
 printf '{"jobs":[{"name":"build","conclusion":"success"},{"name":"notarize-darwin","conclusion":"%s"},{"name":"release","conclusion":"%s"},{"name":"publish-r2","conclusion":"failure"}]}\n' "$notary" "$publication"
else
 status=completed
 [[ "$SCENARIO" != unfinished ]] || status=in_progress
 printf '{"head_sha":"048c0d479f663caa31880fdb3c6d5e33734de8be","head_branch":"v0.79.1","event":"push","status":"%s","conclusion":"failure","path":".github/workflows/release.yml"}\n' "$status"
fi
`,
			} {
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			ref := "refs/heads/main"
			if scenario == "wrong-branch" {
				ref = "refs/heads/feature"
			}
			cmd := exec.Command(testBash(t), "./verify_r2_v0791_recovery.sh")
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SCENARIO="+scenario, "GITHUB_EVENT_NAME=push", "GITHUB_REF="+ref, "GITHUB_REPOSITORY=caelis-labs/caelis")
			out, err := cmd.CombinedOutput()
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("%v: %s", err, out)
			}
		})
	}
}

func TestR2V0791RecoveryDoesNotRepeatOtherPublication(t *testing.T) {
	workflow := readWorkflow(t, "../.github/workflows/recover-r2-v0791.yml")
	for _, forbidden := range []string{"workflow_dispatch", "sign_notarize_cli.sh", "publish_release_assets.sh", "npm publish", "publish-release.mjs"} {
		if strings.Contains(workflow, forbidden) {
			t.Errorf("R2-only recovery includes %q", forbidden)
		}
	}
	guard := strings.Index(workflow, "bash scripts/verify_r2_v0791_recovery.sh")
	publish := strings.Index(workflow, "bash scripts/publish_latest_to_r2.sh v0.79.1")
	if guard < 0 || publish < guard || !strings.Contains(workflow, "branches: [main]") || !strings.Contains(workflow, "paths: [.github/workflows/recover-r2-v0791.yml]") {
		t.Fatal("recovery must be scoped to its reviewed main change and guarded before publishing")
	}
}
