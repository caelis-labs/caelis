package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/skill"
)

func TestProjectSubmissionReferencesDoesNotBlockOnSkillDiscoveryError(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	stack := &Stack{
		Workspace: session.WorkspaceRef{CWD: workspace},
		runtime:   stackRuntimeConfig{SkillDirs: []string{"\x00"}},
	}

	projected, err := stack.projectSubmissionReferences(context.Background(), kernelimpl.SubmissionReferenceProjectionRequest{
		Session: session.Session{CWD: workspace},
		Input:   "$cmpctl inspect",
	})
	if err != nil {
		t.Fatalf("projectSubmissionReferences() error = %v, want nil for skill discovery error", err)
	}
	if projected.Changed || projected.Input != "$cmpctl inspect" {
		t.Fatalf("projected = %#v, want raw skill reference pass-through", projected)
	}
}

func TestProjectSubmissionReferencesKeepsShellVariableWhenSkillDiscoveryFails(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	stack := &Stack{
		Workspace: session.WorkspaceRef{CWD: workspace},
		runtime:   stackRuntimeConfig{SkillDirs: []string{"\x00"}},
	}

	projected, err := stack.projectSubmissionReferences(context.Background(), kernelimpl.SubmissionReferenceProjectionRequest{
		Session: session.Session{CWD: workspace},
		Input:   "echo $HOME",
	})
	if err != nil {
		t.Fatalf("projectSubmissionReferences() error = %v, want nil for shell variable", err)
	}
	if projected.Changed || projected.Input != "echo $HOME" {
		t.Fatalf("projected = %#v, want shell variable pass-through", projected)
	}
}

func TestProjectSubmissionReferencesProjectsFilesWhenSkillDiscoveryFails(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "dict.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	stack := &Stack{
		Workspace: session.WorkspaceRef{CWD: workspace},
		runtime:   stackRuntimeConfig{SkillDirs: []string{"\x00"}},
	}

	projected, err := stack.projectSubmissionReferences(context.Background(), kernelimpl.SubmissionReferenceProjectionRequest{
		Session: session.Session{CWD: workspace},
		Input:   "$cmpctl inspect #dict.go",
	})
	if err != nil {
		t.Fatalf("projectSubmissionReferences() error = %v, want nil for skill discovery error", err)
	}
	if !projected.Changed || !strings.Contains(projected.Input, "Read `dict.go` before answering or editing.") {
		t.Fatalf("projected = %#v, want file projection", projected)
	}
	if !strings.Contains(projected.Input, "$cmpctl inspect `dict.go`") {
		t.Fatalf("projected.Input = %q, want unresolved skill left in user request", projected.Input)
	}
}

func TestProjectSubmissionReferencesDoesNotDiscoverSkillsForFileOnlyReferences(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "dict.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	stack := &Stack{
		Workspace: session.WorkspaceRef{CWD: workspace},
		runtime:   stackRuntimeConfig{SkillDirs: []string{"\x00"}},
	}

	projected, err := stack.projectSubmissionReferences(context.Background(), kernelimpl.SubmissionReferenceProjectionRequest{
		Session: session.Session{CWD: workspace},
		Input:   "read #dict.go",
	})
	if err != nil {
		t.Fatalf("projectSubmissionReferences() error = %v, want nil for file-only reference", err)
	}
	if !projected.Changed || !strings.Contains(projected.Input, "Read `dict.go` before answering or editing.") {
		t.Fatalf("projected = %#v, want file projection", projected)
	}
}

func TestProjectSubmissionReferencesUsesRuntimeSkillSnapshot(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, ".agents", "skills", "late"), 0o700); err != nil {
		t.Fatalf("MkdirAll(late skill) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".agents", "skills", "late", "SKILL.md"), []byte("---\nname: late\ndescription: Late skill.\n---\n# Late\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(late SKILL.md) error = %v", err)
	}
	stack := &Stack{
		Workspace: session.WorkspaceRef{CWD: workspace},
		runtime: stackRuntimeConfig{
			SkillCatalog: skill.NewCatalog([]skill.Meta{{
				Name:      "known",
				LocalName: "known",
				Path:      filepath.Join(workspace, ".agents", "skills", "known", "SKILL.md"),
			}}),
		},
	}

	projected, err := stack.projectSubmissionReferences(context.Background(), kernelimpl.SubmissionReferenceProjectionRequest{
		Session: session.Session{CWD: workspace},
		Input:   "$known and $late",
	})
	if err != nil {
		t.Fatalf("projectSubmissionReferences() error = %v", err)
	}
	if !strings.Contains(projected.Input, "Load skill `known`") {
		t.Fatalf("projected.Input = %q, want known skill from snapshot projected", projected.Input)
	}
	if strings.Contains(projected.Input, "Load skill `late`") {
		t.Fatalf("projected.Input = %q, should not project skill added outside runtime snapshot", projected.Input)
	}
	if !strings.Contains(projected.Input, "$late") {
		t.Fatalf("projected.Input = %q, want late skill shorthand left unresolved", projected.Input)
	}
}
