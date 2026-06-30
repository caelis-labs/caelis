package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	kernelimpl "github.com/OnslaughtSnail/caelis/internal/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
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
