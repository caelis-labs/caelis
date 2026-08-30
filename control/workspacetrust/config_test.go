package workspacetrust

import (
	"path/filepath"
	"testing"
)

func TestConfigurationUsesExactTriStateWorkspaceDecision(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "workspace", "project")
	trusted, err := Set(nil, root, Trusted)
	if err != nil {
		t.Fatalf("Set trusted: %v", err)
	}
	if got := Lookup(trusted, root); got != Trusted {
		t.Fatalf("Lookup(root) = %q, want %q", got, Trusted)
	}
	if got := Lookup(trusted, root+"-other"); got != Unknown {
		t.Fatalf("Lookup(sibling) = %q, want %q", got, Unknown)
	}
	untrusted, err := Set(trusted, root, Untrusted)
	if err != nil {
		t.Fatalf("Set untrusted: %v", err)
	}
	if got := Lookup(untrusted, root); got != Untrusted {
		t.Fatalf("Lookup(root) = %q, want %q", got, Untrusted)
	}
	if got := Lookup(trusted, root); got != Trusted {
		t.Fatalf("Set mutated input: Lookup(root) = %q", got)
	}
}

func TestConfigurationRejectsInvalidPersistedDecisions(t *testing.T) {
	if _, err := Set(nil, "relative", Trusted); err == nil {
		t.Fatal("Set accepted a relative workspace")
	}
	root := filepath.Join(string(filepath.Separator), "workspace", "project")
	if _, err := Set(nil, root, Unknown); err == nil {
		t.Fatal("Set accepted an unknown decision")
	}
	if err := Validate(Configuration{root: Unknown}); err == nil {
		t.Fatal("Validate accepted an unknown persisted decision")
	}
}

func TestValidateIdentitiesRejectsNormalizedCollision(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "workspace", "project")
	if err := ValidateIdentities(Configuration{
		root:                                    Trusted,
		root + string(filepath.Separator) + ".": Untrusted,
	}); err == nil {
		t.Fatal("ValidateIdentities accepted colliding workspace paths")
	}
}

func TestConfigurationKeepsCaseDistinctWorkspaceIdentities(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "workspace", "Repo")
	trusted, err := Set(nil, root, Trusted)
	if err != nil {
		t.Fatalf("Set trusted: %v", err)
	}
	other := filepath.Join(string(filepath.Separator), "workspace", "repo")
	if got := Lookup(trusted, other); got != Unknown {
		t.Fatalf("Lookup(case-distinct workspace) = %q, want %q", got, Unknown)
	}
}
