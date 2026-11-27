package gatewayapp_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig/typesafe"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	tuiapp "github.com/caelis-labs/caelis/surfaces/tui/app"
	"github.com/charmbracelet/x/ansi"
)

func TestRuntimeTeamHTTPStaleGuardianDraftRequiresReload(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	host := startApplicationHTTPHost(t, filepath.Join(root, "store"), root, &applicationHTTPModel{})
	defer host.close(t)
	connectApplicationChildModel(t, ctx, host, "guardian-draft")
	connectApplicationChildModel(t, ctx, host, "guardian-concurrent")
	status, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	connected, err := host.host.ConnectModel(ctx, appserver.ConnectModelRequest{
		WriteBase: appserver.WriteBase{OperationID: "connect-classifier", ExpectedRevision: &status.Configuration.Revision},
		Config:    appserver.ConnectConfig{Provider: "custom-judgment", Model: typesafe.DefaultModel, BaseURL: "https://provider.invalid/v1", APIKey: "SYNTHETIC_ONLY"},
	})
	if err != nil || connected.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("connect classifier: %+v, %v", connected, err)
	}
	savedBinding := func(handle agentbinding.Handle) agentbinding.Binding {
		t.Helper()
		current, readErr := host.host.AgentBindingStatus(ctx, appserver.AgentRequest{})
		if readErr != nil {
			t.Fatal(readErr)
		}
		for _, item := range current.Handles {
			if item.Definition.Handle == handle {
				return item.Binding
			}
		}
		t.Fatalf("missing binding %s", handle)
		return agentbinding.Binding{}
	}
	client := host.host
	adapter, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
		WorkspaceDir: root, Surface: "tui", Sessions: client, Participants: client, SubagentInputs: client,
		Status: client, Agents: client, Configuration: client, Completion: client, Plugins: client,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := tuiapp.NewModel(tuiapp.Config{Context: ctx, Commands: tuiapp.DefaultCommands(), ControlService: adapter, NoColor: true, NoAnimation: true})
	step := func(msg tea.Msg) tea.Cmd {
		updated, cmd := m.Update(msg)
		m = updated.(*tuiapp.Model)
		return cmd
	}
	key := func(code rune) tea.Cmd { return step(tea.KeyPressMsg(tea.Key{Code: code})) }
	step(tea.WindowSizeMsg{Width: 120, Height: 36})
	for _, r := range "/team" {
		step(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
	}
	cmd := key(tea.KeyEnter)
	if cmd == nil {
		t.Fatal("missing team load command")
	}
	step(cmd())
	step(tea.PasteMsg{Content: "Guardian"})
	key(tea.KeyEnter)
	click := func(label string) {
		t.Helper()
		for y, line := range strings.Split(ansi.Strip(m.View().Content), "\n") {
			if x := strings.Index(line, label); x >= 0 {
				mouse := tea.Mouse{X: ansi.StringWidth(line[:x]), Y: y, Button: tea.MouseLeft}
				if step(tea.MouseClickMsg(mouse)) != nil || step(tea.MouseReleaseMsg(mouse)) != nil {
					t.Fatalf("click on %q ran a command", label)
				}
				return
			}
		}
		t.Fatalf("no visible label %q", label)
	}
	click("custom/guardian-draft")
	if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, "/team › Guardian › Classifier") || !selectedTeamModel(frame, "Disabled") {
		t.Fatalf("click did not stage the review model and open the Classifier picker:\n%s", frame)
	}
	key(tea.KeyEscape)
	if !selectedTeamModel(m.View().Content, "custom/guardian-draft") {
		t.Fatal("returning from the Classifier picker did not restore the staged review model")
	}
	if cmd := key(tea.KeyEnter); cmd != nil {
		t.Fatal("reopening the Classifier picker ran a command")
	}
	// Selecting by keyboard keeps the pair pending until Enter. A row click
	// on the Classifier page would confirm the pair immediately.
	if cmd := key(tea.KeyDown); cmd != nil {
		t.Fatal("moving to the classifier ran a command")
	}
	if frame := m.View().Content; !selectedTeamModel(frame, "custom/jev-1.13.0") {
		t.Fatalf("missing selected classifier draft:\n%s", ansi.Strip(frame))
	}
	bindRuntimeTeamHTTP(t, ctx, host, agentbinding.HandleGuardian, "guardian-concurrent", "concurrent-reviewer")
	if binding := savedBinding(agentbinding.HandleGuardian); !strings.HasSuffix(binding.ProfileID, "/guardian-concurrent") {
		t.Fatalf("concurrent Host write did not take effect: %+v", binding)
	}
	before, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	cmd = key(tea.KeyEnter)
	if cmd == nil {
		t.Fatal("missing Guardian apply command")
	}
	step(cmd())
	after, err := host.host.SessionStatus(ctx, appserver.StatusRequest{})
	if err != nil || after.Configuration.Revision != before.Configuration.Revision {
		t.Fatalf("stale draft changed Host revision: before=%d, after=%+v, error=%v", before.Configuration.Revision, after.Configuration, err)
	}
	if binding := savedBinding(agentbinding.HandleGuardian); !strings.HasSuffix(binding.ProfileID, "/guardian-concurrent") {
		t.Fatalf("stale draft overwrote the concurrent review binding: %+v", binding)
	}
	if classifier := savedBinding(agentbinding.HandleGuardianScreen); classifier.ProfileID != "" {
		t.Fatalf("stale draft partially committed the classifier: %+v", classifier)
	}
	if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, "Team changed on the Host. Press Enter to reload.") {
		t.Fatalf("missing conflict recovery:\n%s", frame)
	}
	cmd = key(tea.KeyEnter)
	if cmd == nil {
		t.Fatal("conflict reload did not run")
	}
	step(cmd())
	if frame := ansi.Strip(m.View().Content); !strings.Contains(frame, "guardian-concurrent") || strings.Contains(frame, "Team changed on the Host") {
		t.Fatalf("Team did not reload the latest binding:\n%s", frame)
	}
}

func selectedTeamModel(frame, label string) bool {
	for _, line := range strings.Split(ansi.Strip(frame), "\n") {
		if strings.Contains(line, "›") && strings.Contains(line, label) {
			return true
		}
	}
	return false
}
