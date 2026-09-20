package tuiapp

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestJudgmentWizardRendersMaskedCredentialsAndSubmitsWithoutChatControls(t *testing.T) {
	var submitted []Submission
	m := NewModel(Config{Commands: DefaultCommands(), Wizards: DefaultWizards(), NoColor: true, NoAnimation: true,
		ExecuteLine: func(s Submission) TaskResultMsg { submitted = append(submitted, s); return TaskResultMsg{} },
		SlashArgComplete: func(_ context.Context, command, query string, _ int) ([]SlashArgCandidate, error) {
			var candidates []SlashArgCandidate
			switch {
			case command == "connect":
				candidates = []SlashArgCandidate{{Value: "judgment", Display: "Connect a judgment model"}}
			case command == "connect-provider-judgment":
				candidates = []SlashArgCandidate{{Value: "typesafe", Display: "TypeSafe · Jev"}}
			case strings.HasPrefix(command, "connect-baseurl:"):
				candidates = []SlashArgCandidate{{Value: "https://api.typesafe.ai/v1"}}
			case strings.HasPrefix(command, "connect-model:"):
				candidates = []SlashArgCandidate{{Value: "jev-1.13.0", ModelMetadataComplete: true, ModelImageInputKnown: true}}
			}
			return filterSlashArgCandidates(query, candidates), nil
		},
	})
	m.width, m.height, m.ready = 100, 30, true
	runConnectTestCmd(m, m.openSlashArgPicker("connect"))
	connectPress(m, "enter")
	connectPress(m, "enter")
	connectPaste(m, "fixture-private-key")
	frame := ansi.Strip(m.renderWizardOverlay())
	if strings.Contains(frame, "fixture-private-key") || !strings.Contains(frame, "API key") {
		t.Fatalf("credential masking failed:\n%s", frame)
	}
	connectPress(m, "enter")
	modelFrame := ansi.Strip(m.renderWizardOverlay())
	if !strings.Contains(modelFrame, "jev-1.13.0") {
		t.Fatalf("model picker omitted Jev:\n%s", modelFrame)
	}
	connectPress(m, "enter")
	if len(submitted) != 1 || !strings.Contains(submitted[0].Text, "jev-1.13.0") || m.wizardOverlay != nil {
		t.Fatalf("judgment wizard did not submit: %v", submitted)
	}
	for _, key := range []string{"image_input", "context_window_tokens", "max_output_tokens", "reasoning_levels"} {
		if strings.Contains(submitted[0].Text, key) {
			t.Fatalf("judgment flow submitted chat control %s", key)
		}
	}
	if path := os.Getenv("CAELIS_JEV_UI_REPORT"); path != "" {
		if err := os.WriteFile(path, []byte(frame+"\n\n"+modelFrame), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTeamOffersJudgmentOnlyForEvaluationScenes(t *testing.T) {
	m, _ := newSubagentOverlayTestModel(t)
	profile := modelprofile.ModelProfile{ID: "provider:jev", DisplayName: "Jev", Judgment: true, Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "jev"}}, Effort: modelprofile.EffortCapability{DefaultEffort: "none", Choices: []modelprofile.EffortChoice{{Canonical: "none"}}}}
	m.subagentOverlay.status.Targets = append(m.subagentOverlay.status.Targets, profile)
	for _, handle := range []agentbinding.Handle{agentbinding.HandleToolSearch, agentbinding.HandleGuardian, agentbinding.HandleGuardianScreen, agentbinding.HandleMemoryVerifier, agentbinding.HandleSteward, agentbinding.HandleReviewer, agentbinding.HandleOrbit} {
		m.subagentOverlay.bindingHandle = handle
		found := false
		for _, row := range m.subagentBindingRows() {
			if row.binding.ProfileID == profile.ID {
				found = true
			}
		}
		want := handle == agentbinding.HandleToolSearch || handle == agentbinding.HandleGuardianScreen || handle == agentbinding.HandleMemoryVerifier
		if found != want {
			t.Fatalf("Jev choice for %s = %v", handle, found)
		}
	}
}
