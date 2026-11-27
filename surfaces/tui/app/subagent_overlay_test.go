package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestSubagentOverlayRendersOpaqueResponsiveFrame(t *testing.T) {
	model, _ := newSubagentOverlayTestModel(t)
	frame := ansi.Strip(model.View().Content)
	for _, want := range []string{
		"/team", "breeze", "orbit", "zenith",
		"openai-codex/gpt-5.6-sol", "Guardian", "Reviewer", "esc close",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("overlay frame omitted %q\n%s", want, frame)
		}
	}
	lines := strings.Split(frame, "\n")
	if len(lines) != 32 {
		t.Fatalf("frame height = %d, want 32", len(lines))
	}
	for index, line := range lines {
		if got := displayColumns(line); got != 100 {
			t.Fatalf("frame row %d width = %d, want 100: %q", index, got, line)
		}
	}
	for _, unwanted := range []string{"provider:sol", "self", "Participant profiles", "System Agents",
		"Custom roles", "Binding set", "ctrl+n", "ctrl+p", "ctrl+s"} {
		if strings.Contains(frame, unwanted) {
			t.Fatalf("overlay retained %q:\n%s", unwanted, frame)
		}
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 32})
	model = updated.(*Model)
	_ = model.View()
	if geometry := model.subagentOverlay.geometry; geometry.width != model.completionOverlayInnerWidth() {
		t.Fatalf("wide-screen team picker did not use the main-pane model picker width: %#v", geometry)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	model = updated.(*Model)
	_ = model.View()
	if geometry := model.subagentOverlay.geometry; geometry.width > 60 || geometry.height > 18 {
		t.Fatalf("small-screen overlay geometry = %#v, want within 60x18", geometry)
	}
}

func TestTeamOverlayOnlyShowsFixedConfigurableRoles(t *testing.T) {
	model, service := newSubagentOverlayTestModel(t)
	service.status.Handles = append(service.status.Handles, agentbinding.HandleStatus{
		Definition: agentbinding.Definition{Handle: "research", Class: agentbinding.HandleClassDelegation, Custom: true, Configurable: true},
		Binding:    agentbinding.Binding{Handle: "research", ProfileID: "provider:sol", Effort: "high"},
		Profile:    testProviderModelProfile(),
	})
	service.status.Sets = []agentbinding.BindingSetStatus{{Name: "deep-work", Available: true, Active: true}}
	model.subagentOverlay.status = service.status
	frame := ansi.Strip(model.renderSubagentOverlay())
	var handles []string
	for _, row := range model.subagentOverlay.rows {
		handles = append(handles, string(row.handle))
	}
	if got, want := strings.Join(handles, ","), "breeze,orbit,zenith,guardian,reviewer"; got != want {
		t.Fatalf("configurable rows = %s, want %s:\n%s", got, want, frame)
	}
	if strings.Contains(frame, "research") || strings.Contains(frame, "deep-work") {
		t.Fatalf("overlay displayed a custom role or preset:\n%s", frame)
	}
	for _, key := range []tea.Key{
		{Code: 'n', Mod: tea.ModCtrl}, {Code: 'p', Mod: tea.ModCtrl},
		{Code: 's', Mod: tea.ModCtrl}, {Code: tea.KeyDelete},
	} {
		_, cmd := model.Update(tea.KeyPressMsg(key))
		if cmd != nil || model.subagentOverlay.page != subagentPageMain || len(model.subagentOverlay.rows) != 5 {
			t.Fatalf("retired key %v changed the overlay", key)
		}
	}
}

func TestSubagentOverlayDismissesWelcomeCardOnOpen(t *testing.T) {
	service := &subagentDelegationStub{status: subagentTestStatus()}
	model := NewModel(Config{
		Commands:        DefaultCommands(),
		Wizards:         DefaultWizards(),
		ControlService:  service,
		ShowWelcomeCard: true,
		NoColor:         true,
		NoAnimation:     true,
	})
	_ = model.Init()
	if !modelHasBlockKind(model, BlockWelcome) {
		t.Fatal("test setup omitted welcome card")
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	model = updated.(*Model)
	updated, cmd := model.submitInteractiveLine("/team", "/team", nil)
	model = updated.(*Model)
	if cmd == nil || model.subagentOverlay == nil {
		t.Fatal("opening subagent overlay failed")
	}
	if model.welcomeCardPending || modelHasBlockKind(model, BlockWelcome) {
		t.Fatal("opening subagent overlay retained the welcome card")
	}
}

func TestSubagentOverlayBindingPickerUsesModelFirstLabels(t *testing.T) {
	model, _ := newSubagentOverlayTestModel(t)
	longProfile := testProviderModelProfile()
	longProfile.ID = "provider:terra"
	longProfile.DisplayName = "openai-codex/gpt-5.6-terra"
	longProfile.Backend.Provider.ModelConfigID = longProfile.DisplayName
	longProfile.Effort.Choices = []modelprofile.EffortChoice{
		{Canonical: "low", WireValue: "low"},
		{Canonical: "medium", WireValue: "medium"},
		{Canonical: "high", WireValue: "high"},
		{Canonical: "xhigh", WireValue: "xhigh"},
		{Canonical: "max", WireValue: "max"},
		{Canonical: "ultra", WireValue: "ultra"},
	}
	model.subagentOverlay.status.Targets = append(model.subagentOverlay.status.Targets, longProfile)
	selectSubagentTestRow(t, model, "handle:orbit")
	_ = model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	frame := ansi.Strip(model.renderSubagentOverlay())
	for _, want := range []string{
		"openai-codex/gpt-5.6-sol",
		"openai-codex/gpt-5.6-terra",
		"Claude — Opus",
		"high",
		"ACP",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("binding picker omitted %q\n%s", want, frame)
		}
	}
	if len(model.subagentOverlay.rows) != 3 {
		t.Fatalf("binding picker rows = %d, want 3 profiles without Default row", len(model.subagentOverlay.rows))
	}
	for _, unwanted := range []string{"provider:sol", "acp:claude:opus"} {
		if strings.Contains(frame, unwanted) {
			t.Fatalf("binding picker exposed internal profile ID %q\n%s", unwanted, frame)
		}
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 30, Height: 18})
	model = updated.(*Model)
	smallFrame := ansi.Strip(model.View().Content)
	for index, line := range strings.Split(smallFrame, "\n") {
		if got := displayColumns(line); got != 30 {
			t.Fatalf("small binding-picker row %d width = %d, want 30: %q", index, got, line)
		}
	}
	if geometry := model.subagentOverlay.geometry; geometry.width > 30 || geometry.height > 18 {
		t.Fatalf("small binding-picker geometry = %#v, want within 30x18", geometry)
	}
}

func TestSubagentOverlayReviewerPickerAllowsACPButGuardianDoesNot(t *testing.T) {
	reviewer, _ := newSubagentOverlayTestModel(t)
	selectSubagentTestRow(t, reviewer, "handle:reviewer")
	_ = reviewer.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	reviewerFrame := ansi.Strip(reviewer.renderSubagentOverlay())
	if !strings.Contains(reviewerFrame, "Claude — Opus") || !strings.Contains(reviewerFrame, "ACP") {
		t.Fatalf("Reviewer binding picker omitted ACP profile\n%s", reviewerFrame)
	}

	guardian, _ := newSubagentOverlayTestModel(t)
	selectSubagentTestRow(t, guardian, "handle:guardian")
	_ = guardian.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	guardianFrame := ansi.Strip(guardian.renderSubagentOverlay())
	if strings.Contains(guardianFrame, "Claude — Opus") || strings.Contains(guardianFrame, "ACP") {
		t.Fatalf("Guardian binding picker exposed ACP profile\n%s", guardianFrame)
	}
}

func TestSubagentOverlayDisambiguatesDuplicateModelNamesWithoutProfileIDs(t *testing.T) {
	model, _ := newSubagentOverlayTestModel(t)
	duplicate := testProviderModelProfile()
	duplicate.ID = "provider:team-sol"
	duplicate.Backend.Provider.ModelConfigID = "openai-codex@team/openai-codex/gpt-5.6-sol"
	model.subagentOverlay.status.Targets = append(model.subagentOverlay.status.Targets, duplicate)
	selectSubagentTestRow(t, model, "handle:orbit")
	_ = model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	model.renderSubagentOverlay()

	details := map[string]string{}
	for _, row := range model.subagentOverlay.rows {
		if row.binding.ProfileID != "" && row.binding.Effort == "high" {
			details[row.binding.ProfileID] = row.detail
		}
		if strings.HasPrefix(row.label, "provider:") {
			t.Fatalf("duplicate ModelProfile row exposed internal ID: %#v", row)
		}
	}
	if !strings.Contains(details["provider:sol"], "openai-codex") ||
		!strings.Contains(details["provider:team-sol"], "openai-codex@team") {
		t.Fatalf("duplicate model details = %#v", details)
	}
}

func TestSubagentOverlayKeyboardBinds(t *testing.T) {
	model, service := newSubagentOverlayTestModel(t)
	selectSubagentTestRow(t, model, "handle:orbit")
	if cmd := model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter)); cmd != nil {
		t.Fatal("opening binding picker returned mutation command")
	}
	model.renderSubagentOverlay()
	selectSubagentTestRow(t, model, "binding:provider:sol")
	_ = model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyRight))
	cmd := model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("binding selection returned nil command")
	}
	msg := cmd()
	if msg == nil {
		t.Fatal("binding mutation returned nil message")
	}
	updated, _ := model.Update(msg)
	model = updated.(*Model)
	if service.bindRequest != (agentbinding.Binding{
		Handle: agentbinding.HandleOrbit, ProfileID: "provider:sol", Effort: "xhigh",
	}) {
		t.Fatalf("binding request = %#v", service.bindRequest)
	}

	if model.subagentOverlay.page != subagentPageMain || model.currentSubagentRow().handle != agentbinding.HandleOrbit {
		t.Fatal("binding picker did not return to the selected role")
	}

}

func TestSubagentOverlayIgnoresResultFromEarlierRequest(t *testing.T) {
	model, _ := newSubagentOverlayTestModel(t)
	staleRequest := model.subagentOverlay.request
	model.subagentOverlay = nil
	cmd := model.openSubagentOverlay()
	if cmd == nil || model.subagentOverlay == nil {
		t.Fatal("reopening overlay failed")
	}
	current := model.subagentOverlay
	if current.request == staleRequest {
		t.Fatalf("reopened overlay request = %d, want a new generation", current.request)
	}
	current.pending = true

	if cmd := model.handleSubagentOverlayResult(subagentOverlayResultMsg{
		request: staleRequest,
		status:  agentbinding.Status{Sets: []agentbinding.BindingSetStatus{{Name: "stale"}}},
	}); cmd != nil {
		t.Fatal("stale result returned a follow-up command")
	}
	if !current.loading || !current.pending || len(current.status.Sets) != 0 {
		t.Fatalf("stale result mutated current overlay: %#v", current)
	}
}

func TestCustomRoleIsProjectedIntoSlashCommandsWithoutReorderingCore(t *testing.T) {
	got := agentbinding.ProjectBoundDirectNames([]string{"help", "orbit", "status"}, agentbinding.Status{
		Handles: []agentbinding.HandleStatus{
			{
				Definition: agentbinding.Definition{Handle: "orbit", Class: agentbinding.HandleClassDelegation, Configurable: true},
				Binding:    agentbinding.Binding{ProfileID: "provider:model", Effort: "high"},
			},
			{
				Definition: agentbinding.Definition{Handle: "research", Class: agentbinding.HandleClassDelegation, Configurable: true, Custom: true},
				Binding:    agentbinding.Binding{ProfileID: "acp:claude:opus", Effort: "xhigh"},
			},
		},
	})
	want := []string{"help", "orbit", "status", "research"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("ProjectBoundDirectNames() = %#v, want %#v", got, want)
	}
}

func newSubagentOverlayTestModel(t *testing.T) (*Model, *subagentDelegationStub) {
	t.Helper()
	service := &subagentDelegationStub{status: subagentTestStatus()}
	model := NewModel(Config{
		Commands:       DefaultCommands(),
		Wizards:        DefaultWizards(),
		ControlService: service,
		NoColor:        true,
		NoAnimation:    true,
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 100, Height: 32})
	model = updated.(*Model)
	updated, cmd := model.submitInteractiveLine("/team", "/team", nil)
	model = updated.(*Model)
	if cmd == nil {
		t.Fatal("open overlay returned nil command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(*Model)
	if model.subagentOverlay == nil || model.subagentOverlay.loading {
		t.Fatalf("loaded overlay = %#v", model.subagentOverlay)
	}
	_ = model.View()
	return model, service
}

func selectSubagentTestRow(t *testing.T, model *Model, key string) {
	t.Helper()
	model.renderSubagentOverlay()
	for index, row := range model.subagentOverlay.rows {
		if row.key == key {
			model.subagentOverlay.index = index
			return
		}
	}
	t.Fatalf("overlay rows omitted %q: %#v", key, model.subagentOverlay.rows)
}

func subagentSpecialKey(code rune) tea.KeyMsg {
	return tea.KeyPressMsg(tea.Key{Code: code})
}

func modelHasBlockKind(model *Model, kind BlockKind) bool {
	if model == nil || model.doc == nil {
		return false
	}
	for _, block := range model.doc.Blocks() {
		if block.Kind() == kind {
			return true
		}
	}
	return false
}

func TestTeamHoverTracksRowsWithoutEmptyFilterRow(t *testing.T) {
	model, _ := newSubagentOverlayTestModel(t)
	if model.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatal("team does not receive unpressed mouse motion")
	}
	idle := model.renderSubagentOverlay()
	g := model.subagentOverlay.geometry
	if strings.Contains(ansi.Strip(idle), "Type to search") || strings.Contains(ansi.Strip(idle), "[x]") {
		t.Fatal("empty filter or legacy close button occupies chrome")
	}
	before := model.subagentOverlay.index
	target := (before + 1) % len(g.rows)
	model.handleSubagentOverlayMouse(tea.MouseMotionMsg(tea.Mouse{X: g.x + 3, Y: g.rows[target]}))
	if model.subagentOverlay.index != target {
		t.Fatalf("hover did not select row %d: got %d", target, model.subagentOverlay.index)
	}
}
