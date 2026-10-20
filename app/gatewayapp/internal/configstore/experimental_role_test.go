package configstore

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/experimental"
)

func TestExperimentalLegacyRoleSurvivesConfigurationSave(t *testing.T) {
	doc := currentValidationFixture()
	role := agentbinding.Role{Handle: "experimental", Description: "existing role identity"}
	doc.AgentBindings.Roles = append(doc.AgentBindings.Roles, role)
	binding := doc.AgentBindings.Bindings[0]
	binding.Handle = role.Handle
	doc.AgentBindings.Bindings = append(doc.AgentBindings.Bindings, binding)
	doc.AgentBindings.Sets = append(doc.AgentBindings.Sets, agentbinding.BindingSet{Name: "legacy-experiment", Bindings: []agentbinding.Binding{binding}})
	root := t.TempDir()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	store := New(root)
	loaded, err := store.LoadContext(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	disk, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, disk) {
		t.Fatal("load rewrote old role identity")
	}
	loaded.Experimental = experimental.Values{experimental.AsyncCompact: true}
	saved, err := store.CompareAndSave(t.Context(), loaded.ConfigurationRevision, loaded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.AgentBindings, loaded.AgentBindings) {
		t.Fatal("save changed existing role or references")
	}
	if _, err := agentbinding.CreateRole(agentbinding.Configuration{}, role, agentbinding.Binding{}, doc.ModelProfiles); err == nil {
		t.Fatal("new role collided with slash command")
	}
}
