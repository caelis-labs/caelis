package tuiapp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
	acpcontrol "github.com/caelis-labs/caelis/protocol/acp/control"
)

type subagentDelegationStub struct {
	status      agentbinding.Status
	bindRequest agentbinding.Binding
	reset       agentbinding.Handle
}

func (s *subagentDelegationStub) AgentBindingStatus(context.Context) (agentbinding.Status, error) {
	return s.status, nil
}

func (s *subagentDelegationStub) BindAgentBinding(_ context.Context, req agentbinding.Binding) (agentbinding.Status, error) {
	s.bindRequest = req
	return s.status, nil
}

func (s *subagentDelegationStub) ResetAgentBinding(_ context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	s.reset = handle
	return s.status, nil
}

func TestSubagentCompletionUsesFixedProfilesAndRosterTargets(t *testing.T) {
	service := &subagentDelegationStub{status: agentbinding.Status{Targets: []modelprofile.ModelProfile{
		testProviderModelProfile(),
		testACPModelProfile(),
	}}}

	actions, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-action", "", 10)
	if err != nil || !handled || len(actions) != 2 || !subagentCandidateHasValue(actions, "list") || !subagentCandidateHasValue(actions, "bind") {
		t.Fatalf("action candidates = %#v, handled=%v err=%v", actions, handled, err)
	}
	profiles, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-bindable", "", 10)
	if err != nil || !handled || len(profiles) != 5 {
		t.Fatalf("bindable candidates = %#v, handled=%v err=%v", profiles, handled, err)
	}
	for _, want := range []string{"breeze", "orbit", "zenith", "guardian", "reviewer"} {
		if !subagentCandidateHasValue(profiles, want) {
			t.Fatalf("profile candidates = %#v, want %q", profiles, want)
		}
	}
	systemTargets, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-target:guardian", "", 10)
	if err != nil || !handled || len(systemTargets) != 2 || !subagentCandidateHasValue(systemTargets, "default") || !subagentCandidateHasValue(systemTargets, "provider:sol") {
		t.Fatalf("system target candidates = %#v, handled=%v err=%v", systemTargets, handled, err)
	}

	targets, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-target:orbit", "", 10)
	if err != nil || !handled || len(targets) != 3 {
		t.Fatalf("target candidates = %#v, handled=%v err=%v", targets, handled, err)
	}
	self, ok := subagentCandidate(targets, "self")
	if !ok {
		t.Fatalf("self target = %#v", self)
	}
	model, ok := subagentCandidate(targets, "provider:sol")
	if !ok || !strings.Contains(model.Detail, "low, high, xhigh") {
		t.Fatalf("model target = %#v, want supported efforts", model)
	}
	external, ok := subagentCandidate(targets, "acp:claude:opus")
	if !ok || !strings.Contains(external.Detail, "efforts: none") {
		t.Fatalf("external target = %#v, want ACP none effort", external)
	}

	efforts, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-effort:orbit:provider:sol", "", 10)
	if err != nil || !handled || len(efforts) != 3 || !subagentCandidateHasValue(efforts, "xhigh") {
		t.Fatalf("effort candidates = %#v, handled=%v err=%v", efforts, handled, err)
	}
	externalEfforts, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-effort:orbit:acp:claude:opus", "", 10)
	if err != nil || !handled || len(externalEfforts) != 1 || !subagentCandidateHasValue(externalEfforts, "none") {
		t.Fatalf("external effort candidates = %#v, handled=%v err=%v, want explicit none", externalEfforts, handled, err)
	}
	systemEfforts, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-effort:guardian:provider:sol", "", 10)
	if err != nil || !handled || len(systemEfforts) != 3 || !subagentCandidateHasValue(systemEfforts, "xhigh") {
		t.Fatalf("system effort candidates = %#v, handled=%v err=%v", systemEfforts, handled, err)
	}
}

func TestSubagentWizardStartsWithListOrBindThenUsesExplicitDefaultEffortChoice(t *testing.T) {
	root := subagentWizard()
	if root.Command != "subagent" || len(root.Steps) != 1 || root.Steps[0].Key != "action" || root.Branch == nil {
		t.Fatalf("subagent root wizard = %#v", root)
	}
	listWizard := root.Branch("action", "list", nil, nil)
	if listWizard == nil || listWizard.BuildExecLine == nil || listWizard.BuildExecLine(nil) != "/subagent list" {
		t.Fatalf("subagent list branch = %#v", listWizard)
	}
	wizard := root.Branch("action", "bind", nil, nil)
	if wizard == nil || len(wizard.Steps) != 3 {
		t.Fatalf("subagent bind wizard = %#v", wizard)
	}
	state := map[string]string{"subject": "orbit", "target": "acp:claude:opus", "effort": "none"}
	if wizard.Steps[2].ShouldSkip != nil {
		if wizard.Steps[2].ShouldSkip(state) {
			t.Fatal("delegation effort step unexpectedly skipped")
		}
	}
	if got := wizard.BuildExecLine(state); got != "/subagent bind orbit acp:claude:opus none" {
		t.Fatalf("external exec line = %q", got)
	}

	state = map[string]string{"subject": "zenith", "target": "provider:sol", "effort": "xhigh"}
	if got := wizard.BuildExecLine(state); got != "/subagent bind zenith provider:sol xhigh" {
		t.Fatalf("model exec line = %q", got)
	}
	state["effort"] = "high"
	if got := wizard.BuildExecLine(state); got != "/subagent bind zenith provider:sol high" {
		t.Fatalf("explicit default-effort exec line = %q", got)
	}
	state = map[string]string{"subject": "guardian", "target": "provider:sol", "effort": "xhigh"}
	if wizard.Steps[2].ShouldSkip(state) || wizard.BuildExecLine(state) != "/subagent bind guardian provider:sol xhigh" {
		t.Fatalf("system Agent wizard state = %#v", state)
	}
	state = map[string]string{"subject": "reviewer", "target": "default"}
	if !wizard.Steps[2].ShouldSkip(state) || wizard.BuildExecLine(state) != "/subagent bind reviewer default" {
		t.Fatalf("system Agent reset wizard state = %#v", state)
	}

	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
		SlashArgComplete: func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
			return []SlashArgCandidate{{Value: "list", Display: "List bindings"}, {Value: "bind", Display: "Bind Agent"}}, nil
		},
	})
	if !model.tryOpenSlashArgPicker("/subagent") || model.wizard == nil || model.wizard.currentStep() == nil || model.wizard.currentStep().Key != "action" {
		t.Fatalf("bare /subagent did not open the action wizard: %#v", model.wizard)
	}
}

func TestSlashSubagentListsAndBindsProfilesAndSystemAgents(t *testing.T) {
	service := &subagentDelegationStub{status: subagentTestStatus()}
	var notices []string
	var tables []acpcontrol.SlashCommandResult
	send := func(msg tea.Msg) {
		switch value := msg.(type) {
		case SlashNoticeMsg:
			notices = append(notices, value.Text)
		case SlashCommandResultMsg:
			tables = append(tables, value.Result)
		}
	}

	result := slashSubagentWithContext(context.Background(), service, send, "list")
	if result.Err != nil || !result.SuppressTurnDivider || len(notices) != 0 || len(tables) != 1 {
		t.Fatalf("list result = %#v notices=%#v tables=%#v", result, notices, tables)
	}
	if tables[0].Kind != acpcontrol.SlashCommandResultTable || tables[0].Command != "subagent" {
		t.Fatalf("list table result = %#v", tables[0])
	}
	wantTable := acpcontrol.SlashTableSnapshot{
		Title: "Subagents",
		Sections: []acpcontrol.SlashTableSection{
			{
				Title:   "Delegation Profiles",
				Columns: []string{"Profile", "Name", "Binding"},
				Rows: [][]string{
					{"self", "Session Default", "Current Session controller and effort"},
					{"breeze", "Caelis Breeze", "Unbound"},
					{"orbit", "Caelis Orbit", "openai-codex/gpt-5.6-sol [high]"},
					{"zenith", "Caelis Zenith", "Unbound"},
				},
			},
			{
				Title:   "System Agents",
				Columns: []string{"Agent", "Name", "Binding"},
				Rows: [][]string{
					{"guardian", "Guardian", "openai-codex/gpt-5.6-sol [xhigh]"},
					{"reviewer", "Reviewer", "Main Agent default"},
				},
			},
		},
	}
	if !reflect.DeepEqual(tables[0].Table, wantTable) {
		t.Fatalf("list table = %#v, want %#v", tables[0].Table, wantTable)
	}
	listOutput := slashOutputPlainForTest(renderSlashCommandResultLines(tables[0]))
	for _, want := range []string{"Caelis Breeze", "Caelis Orbit", "Caelis Zenith", "openai-codex/gpt-5.6-sol", "[high]", "System Agents", "Guardian", "Reviewer"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("list output = %q, want %q", listOutput, want)
		}
	}

	result = slashSubagentWithContext(context.Background(), service, send, "bind guardian provider:sol xhigh")
	if result.Err != nil || service.bindRequest.Handle != agentbinding.HandleGuardian || service.bindRequest.ProfileID != "provider:sol" || service.bindRequest.Effort != "xhigh" {
		t.Fatalf("system bind result = %#v request=%#v", result, service.bindRequest)
	}
	result = slashSubagentWithContext(context.Background(), service, send, "bind reviewer default")
	if result.Err != nil || service.reset != agentbinding.HandleReviewer {
		t.Fatalf("system reset result = %#v reset=%q", result, service.reset)
	}

	result = slashSubagentWithContext(context.Background(), service, send, "bind zenith provider:sol xhigh")
	if result.Err != nil || service.bindRequest.Handle != agentbinding.HandleZenith || service.bindRequest.ProfileID != "provider:sol" || service.bindRequest.Effort != "xhigh" {
		t.Fatalf("bind result = %#v request=%#v", result, service.bindRequest)
	}
	if got := strings.TrimSpace(notices[len(notices)-1]); !strings.HasPrefix(got, "subagent updated zenith") {
		t.Fatalf("model-backed bind notice = %q", got)
	}

	result = slashSubagentWithContext(context.Background(), service, send, "bind breeze self")
	if result.Err != nil || service.reset != agentbinding.HandleBreeze {
		t.Fatalf("self bind result = %#v reset=%q", result, service.reset)
	}
	if got := strings.TrimSpace(notices[len(notices)-1]); !strings.HasPrefix(got, "subagent updated breeze") {
		t.Fatalf("self bind notice = %q", got)
	}

	service.reset = ""
	result = slashSubagentWithContext(context.Background(), service, send, "reset orbit")
	if result.Err != nil || service.reset != "" || !strings.Contains(notices[len(notices)-1], "usage: /subagent") {
		t.Fatalf("removed reset action result = %#v reset=%q notices=%#v", result, service.reset, notices)
	}
}

func TestSubagentBindingNoticeUsesSameRendererForModelExternalAndSelf(t *testing.T) {
	status := subagentTestStatus()
	for i := range status.Handles {
		if status.Handles[i].Definition.Handle != agentbinding.HandleBreeze {
			continue
		}
		status.Handles[i].Binding = agentbinding.Binding{
			Handle: agentbinding.HandleBreeze, ProfileID: "acp:grok:4.5", Effort: "none",
		}
		status.Handles[i].Profile = modelprofile.ModelProfile{ID: "acp:grok:4.5", DisplayName: "Grok 4.5"}
	}
	for _, handle := range []agentbinding.Handle{agentbinding.HandleBreeze, agentbinding.HandleOrbit, agentbinding.HandleZenith} {
		lines := renderSlashNoticeLines(SlashNoticeMsg{Text: formatAgentBindingNotice(status, handle)})
		if len(lines) != 1 || !strings.HasPrefix(lines[0].Text, "subagent updated ") || !lines[0].Plain {
			t.Fatalf("handle %q rendered notice = %#v", handle, lines)
		}
	}
}

func TestProfileSlashDescriptionIncludesBoundProviderModelAndEffort(t *testing.T) {
	detail := subagentProfileCommandDetail(agentbinding.HandleStatus{
		Definition: agentbinding.Definition{
			Handle: agentbinding.HandleOrbit, Description: "General implementation and review.", Configurable: true,
		},
		Binding: agentbinding.Binding{
			Handle: agentbinding.HandleOrbit, ProfileID: "provider:sol", Effort: "high",
		},
		Profile: testProviderModelProfile(),
	})
	for _, want := range []string{"General implementation", "openai-codex/gpt-5.6-sol", "[high]"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("profile detail = %q, want %q", detail, want)
		}
	}
}

func subagentTestStatus() agentbinding.Status {
	status := agentbinding.Status{Targets: []modelprofile.ModelProfile{testProviderModelProfile(), testACPModelProfile()}}
	for _, definition := range agentbinding.Definitions() {
		item := agentbinding.HandleStatus{Definition: definition, Binding: agentbinding.Binding{Handle: definition.Handle}}
		switch definition.Handle {
		case agentbinding.HandleOrbit:
			item.Binding.ProfileID = "provider:sol"
			item.Binding.Effort = "high"
			item.Profile = testProviderModelProfile()
		case agentbinding.HandleGuardian:
			item.Binding.ProfileID = "provider:sol"
			item.Binding.Effort = "xhigh"
			item.Profile = testProviderModelProfile()
		}
		status.Handles = append(status.Handles, item)
	}
	return status
}

func testProviderModelProfile() modelprofile.ModelProfile {
	return modelprofile.ModelProfile{
		ID: "provider:sol", DisplayName: "openai-codex/gpt-5.6-sol",
		Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "openai-codex/gpt-5.6-sol"}},
		Effort: modelprofile.EffortCapability{DefaultEffort: "high", Choices: []modelprofile.EffortChoice{
			{Canonical: "low", WireValue: "low"}, {Canonical: "high", WireValue: "high"}, {Canonical: "xhigh", WireValue: "xhigh"},
		}},
	}
}

func testACPModelProfile() modelprofile.ModelProfile {
	return modelprofile.ModelProfile{
		ID: "acp:claude:opus", DisplayName: "Claude — Opus",
		Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{AgentID: "claude", RemoteModelID: "opus"}},
		Effort:  modelprofile.EffortCapability{DefaultEffort: "none", Choices: []modelprofile.EffortChoice{{Canonical: "none"}}},
	}
}

func subagentCandidate(candidates []SlashArgCandidate, value string) (SlashArgCandidate, bool) {
	for _, candidate := range candidates {
		if candidate.Value == value {
			return candidate, true
		}
	}
	return SlashArgCandidate{}, false
}

func subagentCandidateHasValue(candidates []SlashArgCandidate, value string) bool {
	_, ok := subagentCandidate(candidates, value)
	return ok
}
