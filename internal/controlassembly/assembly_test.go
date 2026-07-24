package controlassembly

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	policyapi "github.com/caelis-labs/caelis/agent-sdk/policy"
)

func TestCloneResolvedAssemblyClonesNestedData(t *testing.T) {
	in := ResolvedAssembly{
		Agents: []AgentConfig{{
			Name:        "self",
			Description: "desc",
			Command:     "bash",
			Args:        []string{"-lc", "echo hi"},
			Env:         map[string]string{"A": "1"},
			WorkDir:     "/tmp/work",
		}},
		Skills: []SkillBundle{{
			Plugin:    "skills-demo",
			Namespace: "demo",
			Root:      "/tmp/skills",
			Disabled:  []string{"demo:b"},
		}},
		Modes: []ModeConfig{{
			ID:          "default",
			Name:        "Default",
			Description: "Default mode",
		}},
		Configs: []ConfigOption{{
			ID:           "effort",
			Name:         "Effort",
			DefaultValue: "balanced",
			Options: []ConfigSelectOption{{
				Value: "balanced",
				Name:  "Balanced",
			}},
		}},
	}

	out := CloneResolvedAssembly(in)
	out.Agents[0].Args[0] = "changed"
	out.Agents[0].Env["A"] = "2"
	out.Skills[0].Disabled[0] = "demo:c"
	out.Configs[0].Options[0].Name = "Changed"

	if got := in.Agents[0].Args[0]; got != "-lc" {
		t.Fatalf("agent args mutated original = %q", got)
	}
	if got := in.Agents[0].Env["A"]; got != "1" {
		t.Fatalf("agent env mutated original = %q", got)
	}
	if got := in.Skills[0].Disabled[0]; got != "demo:b" {
		t.Fatalf("skill disabled mutated original = %q", got)
	}
	if got := in.Configs[0].Options[0].Name; got != "Balanced" {
		t.Fatalf("config option mutated original = %q", got)
	}
}

func TestApplyRuntimeOverridesMergesRuntimeMetadata(t *testing.T) {
	metadata := map[string]any{
		policyapi.MetadataLegacyPolicyMode: "legacy",
		"policy_extra_read_roots":          []any{"/existing", "/shared"},
	}
	ApplyRuntimeOverrides(metadata, RuntimeOverrides{
		PolicyMode:   "workspace-write",
		SystemPrompt: " selected prompt ",
		Reasoning: model.ReasoningConfig{
			Effort:       "high",
			BudgetTokens: 4096,
		},
		ExtraReadRoots:  []string{"/shared", "/new-read"},
		ExtraWriteRoots: []string{"/new-write", "/new-write"},
	})

	if metadata[policyapi.MetadataPolicyProfile] != "workspace-write" {
		t.Fatalf("policy profile = %#v", metadata[policyapi.MetadataPolicyProfile])
	}
	if _, ok := metadata[policyapi.MetadataLegacyPolicyMode]; ok {
		t.Fatalf("legacy policy metadata retained = %#v", metadata)
	}
	if metadata["system_prompt"] != "selected prompt" ||
		metadata["reasoning_effort"] != "high" ||
		metadata["reasoning_budget_tokens"] != 4096 {
		t.Fatalf("scalar runtime metadata = %#v", metadata)
	}
	if got, want := metadata["policy_extra_read_roots"], []string{"/existing", "/shared", "/new-read"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("read roots = %#v, want %#v", got, want)
	}
	if got, want := metadata["policy_extra_write_roots"], []string{"/new-write"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("write roots = %#v, want %#v", got, want)
	}
}
