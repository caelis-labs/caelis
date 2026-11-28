package main

import (
	"regexp"
	"strings"
	"testing"
)

func TestBuiltinDTWWorkflowIsolatesWriteCredential(t *testing.T) {
	t.Parallel()

	workflow := readWorkflow(t, "../.github/workflows/builtin-dtw-update.yml")
	prepare, publish, found := strings.Cut(workflow, "\n  publish:\n")
	if !found || strings.Contains(prepare, "secrets.") {
		t.Fatal("DTW release discovery and package verification require a separate read-only job")
	}
	for _, required := range []string{
		"github.repository == 'caelis-labs/caelis' && github.ref == 'refs/heads/main'",
		"GH_TOKEN: ${{ github.token }}",
		"python3 scripts/update_builtin_dtw.py",
	} {
		if !strings.Contains(prepare, required) {
			t.Errorf("DTW discovery job is missing %q", required)
		}
	}
	restore, write, found := strings.Cut(publish, "\n      - name: Open the dependency PR\n")
	if !found || strings.Contains(restore, "secrets.") {
		t.Fatal("DTW publish job must restore the candidate before exposing a write credential")
	}
	if !regexp.MustCompile(`actions/checkout@[0-9a-f]{40}`).MatchString(restore) ||
		!strings.Contains(restore, "ref: ${{ github.sha }}") ||
		!strings.Contains(restore, "base64 --decode > control/plugin/builtin/desktop-world.json") {
		t.Fatal("DTW publish job must check out the fixed Core commit and restore only pin data")
	}
	if strings.Count(write, "secrets.") != 2 ||
		!strings.Contains(write, "GH_TOKEN: ${{ secrets.DTW_UPDATE_TOKEN || secrets.RELEASE_PLEASE_TOKEN }}") ||
		!strings.Contains(write, `run: bash scripts/open_builtin_dtw_update_pr.sh "$DTW_TAG"`) {
		t.Fatal("DTW write step must prefer the dedicated token and invoke the trusted Core PR script")
	}
}
