package tuiapp

import (
	"strings"
	"testing"
)

func TestRenderSlashArgListUsesWizardHintInsteadOfInternalConnectPayload(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	def := model.findWizard("connect")
	if def == nil {
		t.Fatalf("connect wizard not found")
	}
	model.wizard = &wizardRuntime{
		def:       def,
		stepIndex: 5,
		state:     map[string]string{},
	}
	model.slashArgActive = true
	model.slashArgCommand = "connect-context:minimax|https%3A%2F%2Fapi.minimaxi.com%2Fanthropic|60|sk-secret|MiniMax-M2.7-highspeed"
	model.slashArgCandidates = []SlashArgCandidate{{
		Value:   "204800",
		Display: "204800",
		Detail:  "context window tokens",
	}}

	rendered := model.renderSlashArgList()
	if strings.Contains(rendered, "sk-secret") {
		t.Fatalf("rendered slash arg list leaked api key: %q", rendered)
	}
	if !strings.Contains(rendered, "/connect context_window_tokens") {
		t.Fatalf("rendered slash arg list = %q, want wizard step label", rendered)
	}
}

func TestRenderInputBarMasksConnectAPIKeyWithoutDuplicatePrompt(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	def := model.findWizard("connect")
	if def == nil {
		t.Fatalf("connect wizard not found")
	}
	model.wizard = &wizardRuntime{
		def:       def,
		stepIndex: 3,
		state:     map[string]string{"provider": "minimax"},
	}
	model.slashArgActive = true
	model.setInputText("sk-secret")
	model.syncTextareaFromInput()

	rendered := model.renderInputBar()
	if strings.Contains(rendered, "sk-secret") {
		t.Fatalf("rendered input bar leaked api key: %q", rendered)
	}
	if strings.Contains(rendered, "> >") {
		t.Fatalf("rendered input bar duplicated prompt: %q", rendered)
	}
	if strings.Contains(rendered, "/connect") {
		t.Fatalf("rendered input bar leaked /connect prefix: %q", rendered)
	}
}

func TestRenderInputBarHidesConnectPrefixForProviderStep(t *testing.T) {
	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
	})
	def := model.findWizard("connect")
	if def == nil {
		t.Fatalf("connect wizard not found")
	}
	model.wizard = &wizardRuntime{
		def:       def,
		stepIndex: 0,
		state:     map[string]string{},
	}
	model.slashArgActive = true
	model.setInputText("deepseek")
	model.syncTextareaFromInput()

	rendered := model.renderInputBar()
	if strings.Contains(rendered, "/connect") {
		t.Fatalf("rendered provider step leaked /connect prefix: %q", rendered)
	}
	if !strings.Contains(rendered, "deepseek") {
		t.Fatalf("rendered provider step missing visible query: %q", rendered)
	}
}
