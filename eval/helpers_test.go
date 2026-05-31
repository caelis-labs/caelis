//go:build e2e

package eval

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/gatewaydriver"
)

func repoRootForEval(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd() error = %v", err)
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repo root")
		}
		dir = parent
	}
}

func repoRootForRunnerTest(t *testing.T) string { return repoRootForEval(t) }

func slashCandidatesHaveValue(candidates []gatewaydriver.SlashArgCandidate, value string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate.Value), strings.TrimSpace(value)) {
			return true
		}
	}
	return false
}
