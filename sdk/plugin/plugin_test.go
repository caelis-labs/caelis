package plugin

import "testing"

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
