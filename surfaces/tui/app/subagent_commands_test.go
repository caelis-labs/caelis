package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type subagentDelegationStub struct {
	ControlServices
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

func TestTeamCommandOpensDedicatedOverlay(t *testing.T) {
	for _, command := range []string{"/team", "/subagent", " /TEAM "} {
		t.Run(command, func(t *testing.T) {
			service := &subagentDelegationStub{status: subagentTestStatus()}
			model := NewModel(Config{
				Commands:       DefaultCommands(),
				Wizards:        DefaultWizards(),
				ControlService: service,
				ExecuteLine: func(Submission) TaskResultMsg {
					t.Fatal("team configuration reached prompt execution")
					return TaskResultMsg{}
				},
			})
			_, cmd := model.submitInteractiveLine(command, command, nil)
			if cmd == nil || model.subagentOverlay == nil || !model.subagentOverlay.loading || model.wizard != nil || model.turnRunning() {
				t.Fatalf("team overlay = %#v wizard=%#v running=%v", model.subagentOverlay, model.wizard, model.turnRunning())
			}
			if msg := cmd(); msg == nil {
				t.Fatal("open command returned no binding result")
			} else if _, ok := msg.(subagentOverlayResultMsg); !ok {
				t.Fatalf("open command message = %T, want binding result", msg)
			}
			if service.bindRequest.Handle != "" || service.reset != "" || model.doc.Len() != 0 {
				t.Fatal("opening configuration changed bindings or appended transcript output")
			}
		})
	}
}

func TestTeamCommandRejectsArgumentsWithoutExecution(t *testing.T) {
	for _, command := range []string{"/team list", "/team bind orbit provider:sol high", "/subagent list", "/subagent bind guardian default"} {
		t.Run(command, func(t *testing.T) {
			service := &subagentDelegationStub{status: subagentTestStatus()}
			model := NewModel(Config{Commands: DefaultCommands(), ControlService: service})
			_, _ = model.submitInteractiveLine(command, command, nil)
			if got := ansi.Strip(model.hint); got != "usage: /team" {
				t.Fatalf("hint = %q, want argument-free team usage", got)
			}
			if model.subagentOverlay != nil || model.turnRunning() || model.doc.Len() != 0 || service.bindRequest.Handle != "" || service.reset != "" {
				t.Fatal("invalid team arguments opened configuration, mutated bindings, or started a turn")
			}
		})
	}
}

func TestTeamCommandAndAliasRemainUnavailableWhileRunning(t *testing.T) {
	for _, command := range []string{"/team", "/subagent"} {
		model := NewModel(Config{Commands: DefaultCommands()})
		model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
		_, _ = model.submitInteractiveLine(command, command, nil)
		if model.subagentOverlay != nil || model.pendingQueue.visibleCount() != 0 || !strings.Contains(ansi.Strip(model.hint), "unavailable while running") {
			t.Fatalf("%s bypassed running command gate: overlay=%#v hint=%q", command, model.subagentOverlay, model.hint)
		}
	}
}

func TestAgentCommandBindingsKeepBuiltinPriorityWithoutChangingConfiguration(t *testing.T) {
	for _, bound := range []bool{false, true} {
		status := subagentTestStatus()
		for _, name := range []agentbinding.Handle{"team", "subagent", "exit", "quit"} {
			item := agentbinding.HandleStatus{Definition: agentbinding.Definition{
				Handle: name, Class: agentbinding.HandleClassDelegation, Configurable: true, Custom: true, Description: "Conflicting role description",
			}}
			if bound {
				item.Binding = agentbinding.Binding{Handle: name, ProfileID: "provider:sol", Effort: "high"}
			}
			status.Handles = append(status.Handles, item)
		}
		service := &subagentDelegationStub{status: status}
		commands := appendAgentSlashCommandsWithContext(context.Background(), service, DefaultCommands())
		for _, canonical := range []string{"team", "quit"} {
			count := 0
			for _, command := range commands {
				if command == canonical {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("bound=%v: %s appears %d times in %#v", bound, canonical, count, commands)
			}
		}
		for _, alias := range []string{"subagent", "exit"} {
			if indexOfString(commands, alias) >= 0 {
				t.Fatalf("bound=%v: alias/custom role %s advertised separately: %#v", bound, alias, commands)
			}
		}
		details := profileCommandDetailsWithContext(context.Background(), service)
		if details["team"] != "" || details["quit"] != "" || details["subagent"] != "" || details["exit"] != "" {
			t.Fatalf("custom role overwrote built-in descriptions: %#v", details)
		}
		if len(service.status.Handles) != len(agentbinding.Definitions())+4 {
			t.Fatal("command projection removed roles from configuration")
		}
	}
}

func TestProfileSlashDescriptionIncludesBoundProviderModelAndEffort(t *testing.T) {
	detail := agentProfileCommandDetail(agentbinding.HandleStatus{
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
	if detail := agentProfileCommandDetail(agentbinding.HandleStatus{}); detail != "unbound · configure with /team" {
		t.Fatalf("unbound profile detail = %q", detail)
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
