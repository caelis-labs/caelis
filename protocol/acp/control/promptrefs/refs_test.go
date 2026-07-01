package promptrefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanSubmissionReferencesAllowsNamespacedSkills(t *testing.T) {
	t.Parallel()

	tokens := ScanSubmissionReferences("$figma:figma-use build #app.go")
	if len(tokens) != 2 {
		t.Fatalf("ScanSubmissionReferences() returned %d tokens, want 2: %#v", len(tokens), tokens)
	}
	if tokens[0].Kind != KindSkill || tokens[0].Value != "figma:figma-use" {
		t.Fatalf("skill token = %#v, want namespaced plugin skill", tokens[0])
	}
	if tokens[1].Kind != KindFile || tokens[1].Value != "app.go" {
		t.Fatalf("file token = %#v, want app.go", tokens[1])
	}
}

func TestSkillQueryAtCursorAllowsNamespacedSkills(t *testing.T) {
	t.Parallel()

	input := []rune("use $figma:figma-use")
	start, end, query, ok := SkillQueryAtCursor(input, len(input))
	if !ok {
		t.Fatal("SkillQueryAtCursor() ok = false, want true")
	}
	if query != "figma:figma-use" {
		t.Fatalf("query = %q, want namespaced skill", query)
	}
	if got := string(input[start:end]); got != "$figma:figma-use" {
		t.Fatalf("span = %q, want full namespaced skill token", got)
	}
}

func TestProjectSubmissionReferences(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "dict.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	projected := ProjectSubmissionReferences("$CMPCTL inspect #dict.go", ProjectionOptions{
		WorkspaceDir: workspace,
		SkillNames:   map[string]string{"cmpctl": "cmpctl"},
	})
	if !projected.Changed {
		t.Fatal("ProjectSubmissionReferences() changed = false, want true")
	}
	for _, want := range []string{
		"Load skill `cmpctl` before taking task actions, then follow its instructions.",
		"Read `dict.go` before answering or editing.",
		"User request:\ninspect `dict.go`",
	} {
		if !strings.Contains(projected.Text, want) {
			t.Fatalf("projected input missing %q:\n%s", want, projected.Text)
		}
	}
	if strings.Contains(projected.Text, "$CMPCTL") || strings.Contains(projected.Text, "#dict.go") {
		t.Fatalf("projected input leaked raw references:\n%s", projected.Text)
	}
}

func TestProjectSubmissionReferencesIgnoresShellVariablesAndMissingFiles(t *testing.T) {
	t.Parallel()

	projected := ProjectSubmissionReferences("echo $HOME #missing.go", ProjectionOptions{
		WorkspaceDir: t.TempDir(),
		SkillNames:   map[string]string{"cmpctl": "cmpctl"},
	})
	if projected.Changed {
		t.Fatalf("ProjectSubmissionReferences() changed shell/missing refs: %q", projected.Text)
	}
}

func TestProjectSubmissionReferencesAllowsNamespacedSkills(t *testing.T) {
	t.Parallel()

	projected := ProjectSubmissionReferences("$figma:figma-use sync screen", ProjectionOptions{
		SkillNames: map[string]string{"figma:figma-use": "figma:figma-use"},
	})
	if !projected.Changed {
		t.Fatal("ProjectSubmissionReferences() changed = false, want namespaced skill projection")
	}
	if !strings.Contains(projected.Text, "Load skill `figma:figma-use` before taking task actions, then follow its instructions.") {
		t.Fatalf("projected namespaced skill input missing load instruction:\n%s", projected.Text)
	}
	if strings.Contains(projected.Text, "`skill` tool") {
		t.Fatalf("projected namespaced skill input should stay tool-agnostic:\n%s", projected.Text)
	}
	if strings.Contains(projected.Text, "$figma:figma-use") {
		t.Fatalf("projected namespaced skill input leaked raw skill token:\n%s", projected.Text)
	}
}
