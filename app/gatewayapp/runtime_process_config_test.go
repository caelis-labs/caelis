package gatewayapp

import (
	"testing"

	"github.com/caelis-labs/caelis/control/memorybinding"
)

func TestRuntimeProcessConfigSourceSnapshotsAndClonesMutableState(t *testing.T) {
	t.Parallel()

	networkEnabled := true
	source := newRuntimeProcessConfigSource(sessionRuntimeProcessSnapshot{
		runtime: stackRuntimeConfig{
			SystemPrompt: "first",
			SkillDirs:    []string{"skills-a"},
			Plugins:      []PluginConfig{{ID: "plugin-a"}},
		},
		sandboxOverride: SandboxConfig{
			WritableRoots:  []string{"workspace-a"},
			NetworkEnabled: &networkEnabled,
		},
		childControlURL:       " http://127.0.0.1:1234 ",
		childControlTokenFile: " token-a ",
		memorySelection: memorybinding.RuntimeSelection{
			BindingRef: " primary ",
		},
		memoryDisabled: true,
	})

	first := source.snapshot()
	first.runtime.SkillDirs[0] = "mutated"
	first.sandboxOverride.WritableRoots[0] = "mutated"
	*first.sandboxOverride.NetworkEnabled = false

	second := source.snapshot()
	if second.runtime.SkillDirs[0] != "skills-a" {
		t.Fatalf("Runtime snapshot aliases caller mutation: %#v", second.runtime)
	}
	if len(second.runtime.Plugins) != 0 {
		t.Fatalf("Runtime snapshot duplicated AppConfig-owned Plugins: %#v", second.runtime.Plugins)
	}
	if second.sandboxOverride.WritableRoots[0] != "workspace-a" || !*second.sandboxOverride.NetworkEnabled {
		t.Fatalf("sandbox snapshot aliases caller mutation: %#v", second.sandboxOverride)
	}
	if second.childControlURL != "http://127.0.0.1:1234" || second.childControlTokenFile != "token-a" {
		t.Fatalf("child control snapshot = %#v", second)
	}
	if second.memorySelection.BindingRef != "primary" || !second.memoryDisabled {
		t.Fatalf("Memory process snapshot = %#v", second)
	}
}

func TestRuntimeProcessConfigSourcePublishesLaterActivationState(t *testing.T) {
	t.Parallel()

	source := newRuntimeProcessConfigSource(sessionRuntimeProcessSnapshot{})
	source.setRuntime(stackRuntimeConfig{SystemPrompt: "updated", SkillDirs: []string{"skills-b"}})
	source.setChildControl(" http://127.0.0.1:4321 ", " token-b ")

	snapshot := source.snapshot()
	if snapshot.runtime.SystemPrompt != "updated" || len(snapshot.runtime.SkillDirs) != 1 || snapshot.runtime.SkillDirs[0] != "skills-b" {
		t.Fatalf("Runtime snapshot = %#v", snapshot.runtime)
	}
	if snapshot.childControlURL != "http://127.0.0.1:4321" || snapshot.childControlTokenFile != "token-b" {
		t.Fatalf("child control snapshot = %#v", snapshot)
	}
}
