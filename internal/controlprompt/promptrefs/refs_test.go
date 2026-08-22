package promptrefs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScanSubmissionReferencesAllowsNamespacedSkills(t *testing.T) {
	t.Parallel()

	tokens := ScanSubmissionReferences("/figma:figma-use build @app.go")
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

func TestScanSubmissionReferencesFindsMultipleInlineSlashSkills(t *testing.T) {
	t.Parallel()

	tokens := ScanSubmissionReferences("use /lint and /brainstorm on @app.go")
	if len(tokens) != 3 {
		t.Fatalf("ScanSubmissionReferences() returned %d tokens, want 3: %#v", len(tokens), tokens)
	}
	if tokens[0].Kind != KindSkill || tokens[0].Value != "lint" {
		t.Fatalf("first skill token = %#v, want lint", tokens[0])
	}
	if tokens[1].Kind != KindSkill || tokens[1].Value != "brainstorm" {
		t.Fatalf("second skill token = %#v, want brainstorm", tokens[1])
	}
	if tokens[2].Kind != KindFile || tokens[2].Value != "app.go" {
		t.Fatalf("file token = %#v, want app.go", tokens[2])
	}
}

func TestScanSubmissionReferencesIgnoresURLAndPathSlashTokens(t *testing.T) {
	t.Parallel()

	for _, input := range []string{
		"see https://example.com/lint and /usr/bin/lint plus path/to/lint",
		"compare //lint and use/lint",
		"open C:/Users/docs",
		"file://tmp/notes.md",
		"./src/lint",
		"../pkg/app",
		"/lint这个命令怎么用?",
	} {
		tokens := ScanSubmissionReferences(input)
		if len(tokens) != 0 {
			t.Fatalf("ScanSubmissionReferences(%q) = %#v, want URL/path slash tokens ignored", input, tokens)
		}
	}
}

func TestProjectSubmissionReferences(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "dict.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	projected := ProjectSubmissionReferences("/CMPCTL inspect @dict.go", ProjectionOptions{
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
	if strings.Contains(projected.Text, "/CMPCTL") || strings.Contains(projected.Text, "@dict.go") {
		t.Fatalf("projected input leaked raw references:\n%s", projected.Text)
	}
}

func TestProjectSubmissionReferencesLoadsMultipleInlineSlashSkills(t *testing.T) {
	t.Parallel()

	projected := ProjectSubmissionReferences("use /lint and /brainstorm together", ProjectionOptions{
		SkillNames: map[string]string{
			"lint":       "lint",
			"brainstorm": "superpowers:brainstorm",
		},
	})
	if !projected.Changed {
		t.Fatal("ProjectSubmissionReferences() changed = false, want multiple slash skills")
	}
	for _, want := range []string{
		"Load skill `lint` before taking task actions, then follow its instructions.",
		"Load skill `superpowers:brainstorm` before taking task actions, then follow its instructions.",
		"User request:\nuse and together",
	} {
		if !strings.Contains(projected.Text, want) {
			t.Fatalf("projected input missing %q:\n%s", want, projected.Text)
		}
	}
	if strings.Contains(projected.Text, "/lint") || strings.Contains(projected.Text, "/brainstorm") {
		t.Fatalf("projected input leaked raw slash skill tokens:\n%s", projected.Text)
	}
}

func TestProjectSubmissionReferencesLoadsLeadingInternalSkillAndInlineSlashSkill(t *testing.T) {
	t.Parallel()

	projected := ProjectSubmissionReferences("$lint inspect /brainstorm", ProjectionOptions{
		SkillNames: map[string]string{
			"lint":       "lint",
			"brainstorm": "superpowers:brainstorm",
		},
	})
	if !projected.Changed {
		t.Fatal("ProjectSubmissionReferences() changed = false, want leading internal skill plus inline slash skill")
	}
	for _, want := range []string{
		"Load skill `lint` before taking task actions, then follow its instructions.",
		"Load skill `superpowers:brainstorm` before taking task actions, then follow its instructions.",
		"User request:\ninspect",
	} {
		if !strings.Contains(projected.Text, want) {
			t.Fatalf("projected input missing %q:\n%s", want, projected.Text)
		}
	}
	if strings.Contains(projected.Text, "$lint") || strings.Contains(projected.Text, "/brainstorm") {
		t.Fatalf("projected input leaked raw skill tokens:\n%s", projected.Text)
	}
}

func TestProjectSubmissionReferencesLeavesUnresolvedSlashTokensUntouched(t *testing.T) {
	t.Parallel()

	projected := ProjectSubmissionReferences("please use /notaskill and /lint", ProjectionOptions{
		SkillNames: map[string]string{"lint": "lint"},
	})
	if !projected.Changed {
		t.Fatal("ProjectSubmissionReferences() changed = false, want resolved /lint")
	}
	if !strings.Contains(projected.Text, "Load skill `lint`") {
		t.Fatalf("projected input missing resolved skill:\n%s", projected.Text)
	}
	if !strings.Contains(projected.Text, "/notaskill") {
		t.Fatalf("projected input dropped unresolved slash token:\n%s", projected.Text)
	}
}

func TestProjectSubmissionReferencesIgnoresShellVariablesAndMissingFiles(t *testing.T) {
	t.Parallel()

	projected := ProjectSubmissionReferences("echo $HOME @missing.go", ProjectionOptions{
		WorkspaceDir: t.TempDir(),
		SkillNames:   map[string]string{"cmpctl": "cmpctl"},
	})
	if projected.Changed {
		t.Fatalf("ProjectSubmissionReferences() changed shell/missing refs: %q", projected.Text)
	}
}

func TestScanSubmissionReferencesUsesAtOnlyForFiles(t *testing.T) {
	t.Parallel()

	tokens := ScanSubmissionReferences("email user@example.com and legacy #dict.go")
	if len(tokens) != 0 {
		t.Fatalf("ScanSubmissionReferences() = %#v, want email and legacy # syntax ignored", tokens)
	}

	tokens = ScanSubmissionReferences("read @dict.go")
	if len(tokens) != 1 || tokens[0].Kind != KindFile || tokens[0].Value != "dict.go" {
		t.Fatalf("ScanSubmissionReferences(@dict.go) = %#v, want one file token", tokens)
	}
}

func TestProjectSubmissionReferencesAllowsNamespacedSkills(t *testing.T) {
	t.Parallel()

	projected := ProjectSubmissionReferences("/figma:figma-use sync screen", ProjectionOptions{
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
	if strings.Contains(projected.Text, "/figma:figma-use") {
		t.Fatalf("projected namespaced skill input leaked raw skill token:\n%s", projected.Text)
	}
}

func TestProjectSubmissionReferencesStillProjectsInternalCanonicalSkillTokens(t *testing.T) {
	t.Parallel()

	projected := ProjectSubmissionReferences("$cmpctl inspect", ProjectionOptions{
		SkillNames: map[string]string{"cmpctl": "cmpctl"},
	})
	if !projected.Changed {
		t.Fatal("ProjectSubmissionReferences() changed = false, want internal $canonical projection")
	}
	if !strings.Contains(projected.Text, "Load skill `cmpctl` before taking task actions, then follow its instructions.") {
		t.Fatalf("projected internal skill input missing load instruction:\n%s", projected.Text)
	}
	if strings.Contains(projected.Text, "$cmpctl") {
		t.Fatalf("projected internal skill input leaked raw $ token:\n%s", projected.Text)
	}
}
