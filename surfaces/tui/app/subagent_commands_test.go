package tuiapp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	"github.com/caelis-labs/caelis/control/modelconfig"
	controlsystemagent "github.com/caelis-labs/caelis/control/systemagent"
	acpcontrol "github.com/caelis-labs/caelis/protocol/acp/control"
)

type subagentDelegationStub struct {
	status            controldelegation.Status
	systemStatus      controlsystemagent.Status
	bindRequest       controldelegation.BindRequest
	systemBindRequest controlsystemagent.BindRequest
	reset             controldelegation.Profile
	systemReset       controlsystemagent.ID
}

func (s *subagentDelegationStub) DelegationStatus(context.Context) (controldelegation.Status, error) {
	return s.status, nil
}

func (s *subagentDelegationStub) BindDelegation(_ context.Context, req controldelegation.BindRequest) (controldelegation.Status, error) {
	s.bindRequest = req
	return s.status, nil
}

func (s *subagentDelegationStub) ResetDelegation(_ context.Context, profile controldelegation.Profile) (controldelegation.Status, error) {
	s.reset = profile
	return s.status, nil
}

func (s *subagentDelegationStub) SystemAgentStatus(context.Context) (controlsystemagent.Status, error) {
	return s.systemStatus, nil
}

func (s *subagentDelegationStub) BindSystemAgent(_ context.Context, req controlsystemagent.BindRequest) (controlsystemagent.Status, error) {
	s.systemBindRequest = req
	return s.systemStatus, nil
}

func (s *subagentDelegationStub) ResetSystemAgent(_ context.Context, id controlsystemagent.ID) (controlsystemagent.Status, error) {
	s.systemReset = id
	return s.systemStatus, nil
}

func TestSubagentCompletionUsesFixedProfilesAndRosterTargets(t *testing.T) {
	service := &subagentDelegationStub{status: controldelegation.Status{Targets: []controldelegation.TargetStatus{
		{
			Agent:           controlagents.Agent{ID: "sol", Backing: controlagents.AgentBacking{ModelAlias: "openai-codex/gpt-5.6-sol"}},
			ReasoningLevels: []string{"low", "high", "xhigh"},
		},
		{
			Agent: controlagents.Agent{ID: "claude", Backing: controlagents.AgentBacking{ConnectionID: "claude"}, Defaults: controlagents.SessionOptions{ModelID: "opus"}},
		},
	}}, systemStatus: controlsystemagent.Status{Targets: []controlsystemagent.TargetStatus{{
		Agent: controlagents.Agent{ID: "sol", Backing: controlagents.AgentBacking{ModelAlias: "openai-codex/gpt-5.6-sol"}},
		Model: modelconfig.Config{ID: "openai-codex/gpt-5.6-sol", Alias: "sol", Provider: "openai-codex", Model: "gpt-5.6-sol", ReasoningLevels: []string{"low", "high", "xhigh"}},
	}}}}

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
	if err != nil || !handled || len(systemTargets) != 2 || !subagentCandidateHasValue(systemTargets, "default") || !subagentCandidateHasValue(systemTargets, "sol") {
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
	model, ok := subagentCandidate(targets, "sol")
	if !ok || !strings.Contains(model.Detail, "low, high, xhigh") {
		t.Fatalf("model target = %#v, want supported efforts", model)
	}
	external, ok := subagentCandidate(targets, "claude")
	if !ok || !strings.Contains(external.Detail, "Agent defaults") {
		t.Fatalf("external target = %#v, want ACP defaults without effort", external)
	}

	efforts, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-effort:orbit:sol", "", 10)
	if err != nil || !handled || len(efforts) != 4 || !subagentCandidateHasValue(efforts, "default") || !subagentCandidateHasValue(efforts, "xhigh") {
		t.Fatalf("effort candidates = %#v, handled=%v err=%v", efforts, handled, err)
	}
	externalEfforts, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-effort:orbit:claude", "", 10)
	if err != nil || !handled || len(externalEfforts) != 1 || !subagentCandidateHasValue(externalEfforts, "default") {
		t.Fatalf("external effort candidates = %#v, handled=%v err=%v, want Agent default", externalEfforts, handled, err)
	}
	systemEfforts, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-effort:guardian:sol", "", 10)
	if err != nil || !handled || len(systemEfforts) != 4 || !subagentCandidateHasValue(systemEfforts, "default") || !subagentCandidateHasValue(systemEfforts, "xhigh") {
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
	state := map[string]string{"subject": "orbit", "target": "claude"}
	if wizard.Steps[2].ShouldSkip != nil {
		if wizard.Steps[2].ShouldSkip(state) {
			t.Fatal("delegation effort step unexpectedly skipped")
		}
	}
	if got := wizard.BuildExecLine(state); got != "/subagent bind orbit claude" {
		t.Fatalf("external exec line = %q", got)
	}

	state = map[string]string{"subject": "zenith", "target": "sol", "effort": "xhigh"}
	if got := wizard.BuildExecLine(state); got != "/subagent bind zenith sol xhigh" {
		t.Fatalf("model exec line = %q", got)
	}
	state["effort"] = "default"
	if got := wizard.BuildExecLine(state); got != "/subagent bind zenith sol" {
		t.Fatalf("Agent-default exec line = %q", got)
	}
	state = map[string]string{"subject": "guardian", "target": "sol", "effort": "xhigh"}
	if wizard.Steps[2].ShouldSkip(state) || wizard.BuildExecLine(state) != "/subagent bind guardian sol xhigh" {
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
	service := &subagentDelegationStub{status: subagentTestStatus(), systemStatus: subagentSystemTestStatus()}
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
					{"orbit", "Caelis Orbit", "/sol · openai-codex/gpt-5.6-sol [high]"},
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
	for _, want := range []string{"Caelis Breeze", "Caelis Orbit", "Caelis Zenith", "/sol", "[high]", "System Agents", "Guardian", "Reviewer"} {
		if !strings.Contains(listOutput, want) {
			t.Fatalf("list output = %q, want %q", listOutput, want)
		}
	}

	result = slashSubagentWithContext(context.Background(), service, send, "bind guardian sol xhigh")
	if result.Err != nil || service.systemBindRequest.ID != controlsystemagent.Guardian || service.systemBindRequest.AgentID != "sol" || service.systemBindRequest.ReasoningEffort != "xhigh" {
		t.Fatalf("system bind result = %#v request=%#v", result, service.systemBindRequest)
	}
	result = slashSubagentWithContext(context.Background(), service, send, "bind reviewer default")
	if result.Err != nil || service.systemReset != controlsystemagent.Reviewer {
		t.Fatalf("system reset result = %#v reset=%q", result, service.systemReset)
	}

	result = slashSubagentWithContext(context.Background(), service, send, "bind zenith sol xhigh")
	if result.Err != nil || service.bindRequest.Profile != controldelegation.ProfileZenith || service.bindRequest.AgentID != "sol" || service.bindRequest.ReasoningEffort != "xhigh" {
		t.Fatalf("bind result = %#v request=%#v", result, service.bindRequest)
	}
	if got := strings.TrimSpace(notices[len(notices)-1]); !strings.HasPrefix(got, "subagent updated zenith") {
		t.Fatalf("model-backed bind notice = %q", got)
	}

	result = slashSubagentWithContext(context.Background(), service, send, "bind breeze self")
	if result.Err != nil || service.reset != controldelegation.ProfileBreeze {
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
	for i := range status.Profiles {
		if status.Profiles[i].Definition.Profile != controldelegation.ProfileBreeze {
			continue
		}
		status.Profiles[i].Binding = controldelegation.Binding{
			Profile: controldelegation.ProfileBreeze, Target: controldelegation.TargetAgent, AgentID: "grok",
		}
		status.Profiles[i].Agent = controlagents.Agent{
			ID: "grok", Backing: controlagents.AgentBacking{ConnectionID: "grok"}, Defaults: controlagents.SessionOptions{ModelID: "grok-4.5"},
		}
	}
	for _, profile := range []controldelegation.Profile{controldelegation.ProfileBreeze, controldelegation.ProfileOrbit, controldelegation.ProfileZenith} {
		lines := renderSlashNoticeLines(SlashNoticeMsg{Text: formatSubagentBindingNotice(status, profile)})
		if len(lines) != 1 || !strings.HasPrefix(lines[0].Text, "subagent updated ") || !lines[0].Plain {
			t.Fatalf("profile %q rendered notice = %#v", profile, lines)
		}
	}
}

func TestProfileSlashDescriptionIncludesBoundProviderModelAndEffort(t *testing.T) {
	detail := subagentProfileCommandDetail(controldelegation.ProfileStatus{
		Definition: controldelegation.Definition{
			Profile: controldelegation.ProfileOrbit, Description: "General implementation and review.", Configurable: true,
		},
		Binding: controldelegation.Binding{
			Profile: controldelegation.ProfileOrbit, Target: controldelegation.TargetAgent, AgentID: "sol", ReasoningEffort: "high",
		},
		Agent: controlagents.Agent{ID: "sol", Backing: controlagents.AgentBacking{ModelAlias: "openai-codex/gpt-5.6-sol"}},
	})
	for _, want := range []string{"General implementation", "openai-codex/gpt-5.6-sol", "[high]"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("profile detail = %q, want %q", detail, want)
		}
	}
}

func subagentTestStatus() controldelegation.Status {
	status := controldelegation.Status{}
	for _, definition := range controldelegation.Definitions() {
		binding := controldelegation.Binding{Profile: definition.Profile, Target: controldelegation.TargetSelf}
		profile := controldelegation.ProfileStatus{Definition: definition, Binding: binding}
		if definition.Profile == controldelegation.ProfileOrbit {
			profile.Binding = controldelegation.Binding{
				Profile: definition.Profile, Target: controldelegation.TargetAgent, AgentID: "sol", ReasoningEffort: "high",
			}
			profile.Agent = controlagents.Agent{ID: "sol", Backing: controlagents.AgentBacking{ModelAlias: "openai-codex/gpt-5.6-sol"}}
		}
		status.Profiles = append(status.Profiles, profile)
	}
	return status
}

func subagentSystemTestStatus() controlsystemagent.Status {
	status := controlsystemagent.Status{}
	for _, definition := range controlsystemagent.Definitions() {
		item := controlsystemagent.AgentStatus{Definition: definition, Binding: controlsystemagent.Binding{ID: definition.ID}}
		if definition.ID == controlsystemagent.Guardian {
			item.Binding.AgentID = "sol"
			item.Binding.ReasoningEffort = "xhigh"
			item.Agent = controlagents.Agent{ID: "sol", Backing: controlagents.AgentBacking{ModelAlias: "openai-codex/gpt-5.6-sol"}}
		}
		status.Agents = append(status.Agents, item)
	}
	return status
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
