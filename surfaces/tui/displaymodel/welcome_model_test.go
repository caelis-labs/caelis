package displaymodel

import "testing"

func TestBuildWelcomeViewModelNormalizesLabels(t *testing.T) {
	t.Parallel()

	got := BuildWelcomeViewModel("1.2.3", "", "")
	if got.VersionLabel != "v1.2.3" {
		t.Fatalf("VersionLabel = %q, want v1.2.3", got.VersionLabel)
	}
	if got.Workspace != "." {
		t.Fatalf("Workspace = %q, want .", got.Workspace)
	}
	if got.ModelAlias != "not configured (/connect)" {
		t.Fatalf("ModelAlias = %q, want default not configured label", got.ModelAlias)
	}
}

func TestBuildWelcomeViewModelPreservesVersionPrefix(t *testing.T) {
	t.Parallel()

	got := BuildWelcomeViewModel("v2.0.0", "/tmp/project", "gpt-4o")
	if got.VersionLabel != "v2.0.0" {
		t.Fatalf("VersionLabel = %q, want v2.0.0", got.VersionLabel)
	}
	if got.ModelAlias != "gpt-4o" {
		t.Fatalf("ModelAlias = %q, want gpt-4o", got.ModelAlias)
	}
}
