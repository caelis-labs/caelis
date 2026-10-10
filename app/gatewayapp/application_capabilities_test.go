package gatewayapp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
	skillfs "github.com/caelis-labs/caelis/agent-sdk/skill/fs"
	"github.com/caelis-labs/caelis/control/application"
)

func TestApplicationMCPGrantOwnerUsesAuthenticatedConnectionScope(t *testing.T) {
	owner := application.Scope{PrincipalID: "principal", ApplicationID: "application-a", ConnectionID: "connection-a"}
	if applicationMCPGrantOwner(owner) == "" {
		t.Fatal("authenticated connection has no MCP grant owner")
	}
	other := owner
	other.ConnectionID = "connection-b"
	if applicationMCPGrantOwner(owner) == applicationMCPGrantOwner(other) {
		t.Fatal("independent connection shared MCP grant owner")
	}
	other = owner
	other.ApplicationID = "application-b"
	if applicationMCPGrantOwner(owner) == applicationMCPGrantOwner(other) {
		t.Fatal("independent Application shared MCP grant owner")
	}
}

func TestApplicationSkillRootLoadsOnlySelectedMetadataThenBody(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "selected")
	ambient := filepath.Join(root, "ambient")
	for _, path := range []string{selected, ambient} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(selected, "SKILL.md"), []byte("---\nname: selected\ndescription: Selected synthetic skill.\n---\nSELECTED_BODY_MARKER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ambient, "SKILL.md"), []byte("---\nname: ambient\ndescription: Ambient synthetic skill.\n---\nAMBIENT_BODY_MARKER\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := applicationSkillCatalog(application.Profile{SkillRoots: []string{selected}})
	if err != nil {
		t.Fatal(err)
	}
	metadata := applicationSkillMetadata(catalog)
	if !strings.Contains(metadata, "selected") || strings.Contains(metadata, "SELECTED_BODY_MARKER") || strings.Contains(metadata, "ambient") {
		t.Fatalf("root metadata includes unselected Skill or body: %s", metadata)
	}
	meta, matched := catalog.ResolveBool("selected")
	if !matched {
		t.Fatal("selected root was not discovered")
	}
	loaded, err := (skillfs.Loader{}).Load(context.Background(), skill.RefFromMeta(meta))
	if err != nil || !strings.Contains(loaded.Content, "SELECTED_BODY_MARKER") {
		t.Fatalf("on-demand body = %+v, %v", loaded, err)
	}
}

func TestApplicationSkillCatalogDoesNotSilentlyDropMissingSelectedRoot(t *testing.T) {
	root := t.TempDir()
	healthy := filepath.Join(root, "healthy")
	if err := os.Mkdir(healthy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(healthy, "SKILL.md"), []byte("---\nname: healthy\ndescription: Healthy synthetic skill.\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "selected-but-missing")
	if _, err := applicationSkillCatalog(application.Profile{SkillRoots: []string{healthy, missing}}); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("missing selected Skill was silently omitted: %v", err)
	}
}
