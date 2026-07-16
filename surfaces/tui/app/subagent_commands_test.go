package tuiapp

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
)

type subagentDelegationStub struct {
	status      controldelegation.Status
	bindRequest controldelegation.BindRequest
	reset       controldelegation.Profile
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

func TestSubagentCompletionUsesFixedProfilesAndRosterTargets(t *testing.T) {
	service := &subagentDelegationStub{status: controldelegation.Status{Targets: []controldelegation.TargetStatus{
		{
			Agent:           controlagents.Agent{ID: "sol", Backing: controlagents.AgentBacking{ModelAlias: "openai-codex/gpt-5.6-sol"}},
			ReasoningLevels: []string{"low", "high", "xhigh"},
		},
		{
			Agent: controlagents.Agent{ID: "claude", Backing: controlagents.AgentBacking{ConnectionID: "claude"}, Defaults: controlagents.SessionOptions{ModelID: "opus"}},
		},
	}}}

	profiles, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-profile", "", 10)
	if err != nil || !handled || len(profiles) != 3 {
		t.Fatalf("profile candidates = %#v, handled=%v err=%v", profiles, handled, err)
	}
	for _, want := range []string{"breeze", "orbit", "zenith"} {
		if !subagentCandidateHasValue(profiles, want) {
			t.Fatalf("profile candidates = %#v, want %q", profiles, want)
		}
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

	efforts, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-effort:sol", "", 10)
	if err != nil || !handled || len(efforts) != 4 || !subagentCandidateHasValue(efforts, "default") || !subagentCandidateHasValue(efforts, "xhigh") {
		t.Fatalf("effort candidates = %#v, handled=%v err=%v", efforts, handled, err)
	}
	externalEfforts, handled, err := completeSubagentSlashArgs(context.Background(), service, "subagent-effort:claude", "", 10)
	if err != nil || !handled || len(externalEfforts) != 1 || !subagentCandidateHasValue(externalEfforts, "default") {
		t.Fatalf("external effort candidates = %#v, handled=%v err=%v, want Agent default", externalEfforts, handled, err)
	}
}

func TestSubagentWizardUsesExplicitDefaultEffortChoice(t *testing.T) {
	wizard := subagentWizard()
	if wizard.Command != "subagent" || len(wizard.Steps) != 3 {
		t.Fatalf("subagent wizard = %#v", wizard)
	}
	state := map[string]string{"profile": "orbit", "target": "claude"}
	if wizard.Steps[2].ShouldSkip != nil {
		t.Fatal("effort step must not depend on hidden candidate flags")
	}
	if got := wizard.BuildExecLine(state); got != "/subagent bind orbit claude" {
		t.Fatalf("external exec line = %q", got)
	}

	state = map[string]string{"profile": "zenith", "target": "sol", "effort": "xhigh"}
	if got := wizard.BuildExecLine(state); got != "/subagent bind zenith sol xhigh" {
		t.Fatalf("model exec line = %q", got)
	}
	state["effort"] = "default"
	if got := wizard.BuildExecLine(state); got != "/subagent bind zenith sol" {
		t.Fatalf("Agent-default exec line = %q", got)
	}

	model := NewModel(Config{
		Commands: DefaultCommands(),
		Wizards:  DefaultWizards(),
		SlashArgComplete: func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
			return []SlashArgCandidate{{Value: "breeze", Display: "Caelis Breeze"}}, nil
		},
	})
	if !model.tryOpenSlashArgPicker("/subagent") || model.wizard == nil || model.wizard.currentStep() == nil || model.wizard.currentStep().Key != "profile" {
		t.Fatalf("bare /subagent did not open the profile wizard: %#v", model.wizard)
	}
}

func TestSlashSubagentListsAndBindsProfiles(t *testing.T) {
	service := &subagentDelegationStub{status: subagentTestStatus()}
	var notices []string
	send := func(msg tea.Msg) {
		if notice, ok := msg.(SlashNoticeMsg); ok {
			notices = append(notices, notice.Text)
		}
	}

	result := slashSubagentWithContext(context.Background(), service, send, "list")
	if result.Err != nil || !result.SuppressTurnDivider || len(notices) != 1 {
		t.Fatalf("list result = %#v notices=%#v", result, notices)
	}
	for _, want := range []string{"Caelis Breeze", "Caelis Orbit", "Caelis Zenith", "/sol", "[high]"} {
		if !strings.Contains(notices[0], want) {
			t.Fatalf("list notice = %q, want %q", notices[0], want)
		}
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
