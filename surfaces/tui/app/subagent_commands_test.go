package tuiapp

import (
	"context"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type subagentDelegationStub struct {
	controlprompt.Service
	status      agentbinding.Status
	bindRequest agentbinding.Binding
	reset       agentbinding.Handle
	createdRole agentbinding.Role
	savedSet    string
}

func (s *subagentDelegationStub) AgentBindingStatus(context.Context) (agentbinding.Status, error) {
	return s.status, nil
}

func (s *subagentDelegationStub) AgentStatus(context.Context) (controlprompt.AgentStatusSnapshot, error) {
	return controlprompt.AgentStatusSnapshot{}, nil
}

func (s *subagentDelegationStub) BindAgentBinding(_ context.Context, req agentbinding.Binding) (agentbinding.Status, error) {
	s.bindRequest = req
	return s.status, nil
}

func (s *subagentDelegationStub) ResetAgentBinding(_ context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	s.reset = handle
	return s.status, nil
}

func (s *subagentDelegationStub) CreateAgentRole(_ context.Context, role agentbinding.Role, binding agentbinding.Binding) (agentbinding.Status, error) {
	s.createdRole = role
	s.bindRequest = binding
	return s.status, nil
}

func (s *subagentDelegationStub) DeleteAgentRole(_ context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	s.reset = handle
	return s.status, nil
}

func (s *subagentDelegationStub) SaveAgentBindingSet(_ context.Context, name string) (agentbinding.Status, error) {
	s.savedSet = name
	return s.status, nil
}

func (s *subagentDelegationStub) ApplyAgentBindingSet(_ context.Context, name string) (agentbinding.Status, error) {
	s.savedSet = name
	return s.status, nil
}

func (s *subagentDelegationStub) DeleteAgentBindingSet(_ context.Context, name string) (agentbinding.Status, error) {
	s.savedSet = name
	return s.status, nil
}

func TestBareSubagentOpensDedicatedOverlay(t *testing.T) {
	service := &subagentDelegationStub{status: subagentTestStatus()}
	model := NewModel(Config{
		Commands:       DefaultCommands(),
		Wizards:        DefaultWizards(),
		ControlService: service,
	})
	_, cmd := model.submitInteractiveLine("/subagent", "/subagent", nil)
	if model.subagentOverlay == nil || !model.subagentOverlay.loading || model.wizard != nil {
		t.Fatalf("bare /subagent overlay = %#v wizard=%#v", model.subagentOverlay, model.wizard)
	}
	msg := cmd()
	if _, ok := msg.(subagentOverlayResultMsg); !ok {
		t.Fatalf("open command message = %T, want subagentOverlayResultMsg", msg)
	}
}

func TestSlashSubagentListsAndBindsProfilesAndSystemAgents(t *testing.T) {
	service := &subagentDelegationStub{status: subagentTestStatus()}
	var notices []string
	var tables []controlprompt.SlashCommandResult
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
	if tables[0].Kind != controlprompt.SlashCommandResultTable || tables[0].Command != "subagent" {
		t.Fatalf("list table result = %#v", tables[0])
	}
	wantTable := controlprompt.SlashTableSnapshot{
		Title: "Subagents",
		Sections: []controlprompt.SlashTableSection{
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
