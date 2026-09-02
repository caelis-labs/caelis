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
		"◆ Subagents",
		"Binding set",
		"Delegation Profiles",
		"orbit",
		"openai-codex/gpt-5.6-sol",
		"System Agents",
		"Memory Steward",
		"Static (zero-token)",
		"Save binding set…",
		"Esc close",
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
	if strings.Contains(frame, "provider:sol") {
		t.Fatalf("overlay exposed internal profile ID\n%s", frame)
	}
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 160, Height: 32})
	model = updated.(*Model)
	_ = model.View()
	if geometry := model.subagentOverlay.geometry; geometry.width <= 96 || geometry.width > 160 {
		t.Fatalf("wide-screen overlay geometry = %#v, want wider than 96 columns within viewport", geometry)
	}
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 60, Height: 18})
	model = updated.(*Model)
	_ = model.View()
	if geometry := model.subagentOverlay.geometry; geometry.width > 60 || geometry.height > 18 {
		t.Fatalf("small-screen overlay geometry = %#v, want within 60x18", geometry)
	}
}

func TestSubagentOverlayStewardDefaultIsStatic(t *testing.T) {
	model, _ := newSubagentOverlayTestModel(t)
	selectSubagentTestRow(t, model, "handle:steward")
	_ = model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	frame := ansi.Strip(model.renderSubagentOverlay())
	if !strings.Contains(frame, "Use static zero-token Memory") {
		t.Fatalf("Steward binding picker omitted static default\n%s", frame)
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
	updated, cmd := model.submitInteractiveLine("/subagent", "/subagent", nil)
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
		"[low | medium | high | xhigh | max | ultra]",
		"[none]  · ACP",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("binding picker omitted %q\n%s", want, frame)
		}
	}
	profileRows := 0
	for _, row := range model.subagentOverlay.rows {
		if row.binding.ProfileID != "" {
			profileRows++
		}
	}
	if profileRows != 3 {
		t.Fatalf("binding picker profile rows = %d, want one row for each of 3 profiles", profileRows)
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
	if !strings.Contains(reviewerFrame, "Claude — Opus") || !strings.Contains(reviewerFrame, "· ACP") {
		t.Fatalf("Reviewer binding picker omitted ACP profile\n%s", reviewerFrame)
	}

	guardian, _ := newSubagentOverlayTestModel(t)
	selectSubagentTestRow(t, guardian, "handle:guardian")
	_ = guardian.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	guardianFrame := ansi.Strip(guardian.renderSubagentOverlay())
	if strings.Contains(guardianFrame, "Claude — Opus") || strings.Contains(guardianFrame, "· ACP") {
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

func TestSubagentOverlayActionRowsEmphasizeLabelsOnly(t *testing.T) {
	model, _ := newSubagentOverlayTestModel(t)
	model.openSaveSubagentSet()
	rows := model.subagentSaveSetRows()
	for _, row := range rows[1:] {
		line := model.renderSubagentRow(row, false, 80)
		boldStart := strings.Index(line, "\x1b[1m")
		boldEnd := strings.Index(line, "\x1b[m")
		if boldEnd < 0 {
			boldEnd = strings.Index(line, "\x1b[0m")
		}
		detailStart := strings.Index(line, row.detail)
		if boldStart < 0 || boldEnd < 0 {
			t.Fatalf("action %q did not emphasize its label: %q", row.label, line)
		}
		if detailStart < 0 || boldEnd > detailStart {
			t.Fatalf("action %q emphasized its detail: %q", row.label, line)
		}
	}
}

func TestSubagentOverlayKeyboardBindsAndCreatesRole(t *testing.T) {
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

	model.openSubagentPage(subagentPageMain, 0)
	model.renderSubagentOverlay()
	_ = model.handleSubagentOverlayKey(tea.KeyPressMsg(tea.Key{Text: "n"}))
	model.renderSubagentOverlay()
	_ = model.handleSubagentOverlayPaste(tea.PasteMsg{Content: "research"})
	_ = model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	_ = model.handleSubagentOverlayPaste(tea.PasteMsg{Content: "Investigate unfamiliar systems"})
	_ = model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyDown))
	_ = model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	model.renderSubagentOverlay()
	selectSubagentTestRow(t, model, "binding:acp:claude:opus")
	_ = model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	model.renderSubagentOverlay()
	bindingRow := model.subagentOverlay.rows[2]
	if bindingRow.key != "field:binding" ||
		bindingRow.detail != "Claude — Opus [none]" {
		t.Fatalf("selected role binding row = %#v", bindingRow)
	}
	selectSubagentTestRow(t, model, "create")
	cmd = model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("create role returned nil command")
	}
	_ = cmd()
	if service.createdRole.Handle != "research" ||
		service.createdRole.Description != "Investigate unfamiliar systems" ||
		service.bindRequest.ProfileID != "acp:claude:opus" ||
		service.bindRequest.Effort != "none" {
		t.Fatalf("created role = %#v binding=%#v", service.createdRole, service.bindRequest)
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
	current.afterMutation = &subagentOverlayNav{page: subagentPageSets}

	if cmd := model.handleSubagentOverlayResult(subagentOverlayResultMsg{
		request: staleRequest,
		status:  agentbinding.Status{Sets: []agentbinding.BindingSetStatus{{Name: "stale"}}},
	}); cmd != nil {
		t.Fatal("stale result returned a follow-up command")
	}
	if !current.loading || !current.pending || current.afterMutation == nil || len(current.status.Sets) != 0 {
		t.Fatalf("stale result mutated current overlay: %#v", current)
	}
}

func TestSubagentOverlayNewRoleBindingStartsOnEnabledProfile(t *testing.T) {
	model, _ := newSubagentOverlayTestModel(t)
	model.openNewSubagentRole()
	model.subagentOverlay.roleHandle = "research"
	selectSubagentTestRow(t, model, "field:binding")
	if cmd := model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter)); cmd != nil {
		t.Fatal("opening new-role binding picker returned a command")
	}
	model.renderSubagentOverlay()

	row := model.currentSubagentRow()
	if !row.enabled || row.reset || row.key == "binding:reset" {
		t.Fatalf("initial new-role binding row = %#v, want an enabled profile", row)
	}
	for _, candidate := range model.subagentOverlay.rows {
		if candidate.key == "binding:reset" {
			t.Fatalf("new-role binding picker retained unavailable reset row: %#v", model.subagentOverlay.rows)
		}
	}
}

func TestSubagentOverlayMouseAndBindingSetShortcuts(t *testing.T) {
	model, service := newSubagentOverlayTestModel(t)
	model.subagentOverlay.status.Sets = []agentbinding.BindingSetStatus{{
		Name: "default", Available: true,
	}}
	_ = model.View()
	geometry := model.subagentOverlay.geometry
	rowY := geometry.rows[0]
	x := geometry.x + 4
	_, _ = model.handleMouse(tea.MouseClickMsg(tea.Mouse{X: x, Y: rowY, Button: tea.MouseLeft}))
	_, _ = model.handleMouse(tea.MouseReleaseMsg(tea.Mouse{X: x, Y: rowY, Button: tea.MouseNone}))
	if model.subagentOverlay.page != subagentPageSets {
		t.Fatalf("mouse click page = %v, want binding sets", model.subagentOverlay.page)
	}
	model.renderSubagentOverlay()
	before := model.subagentOverlay.index
	_, _ = model.handleMouse(tea.MouseWheelMsg(tea.Mouse{
		X: x, Y: model.subagentOverlay.geometry.rows[0], Button: tea.MouseWheelDown,
	}))
	if model.subagentOverlay.index == before {
		t.Fatal("mouse wheel did not move binding-set selection")
	}

	_ = model.handleSubagentOverlayKey(tea.KeyPressMsg(tea.Key{Text: "s"}))
	model.renderSubagentOverlay()
	_ = model.handleSubagentOverlayPaste(tea.PasteMsg{Content: "deep-work"})
	selectSubagentTestRow(t, model, "save")
	cmd := model.handleSubagentOverlayKey(subagentSpecialKey(tea.KeyEnter))
	if cmd == nil {
		t.Fatal("save binding set returned nil command")
	}
	msg := cmd()
	if service.savedSet != "deep-work" {
		t.Fatalf("saved binding set = %q, want deep-work", service.savedSet)
	}
	updated, _ := model.Update(msg)
	model = updated.(*Model)
	_ = model.View()
	geometry = model.subagentOverlay.geometry
	_, _ = model.handleMouse(tea.MouseClickMsg(tea.Mouse{
		X: geometry.closeX, Y: geometry.closeY, Button: tea.MouseLeft,
	}))
	_, _ = model.handleMouse(tea.MouseReleaseMsg(tea.Mouse{
		X: geometry.closeX, Y: geometry.closeY, Button: tea.MouseNone,
	}))
	if model.subagentOverlay != nil {
		t.Fatal("mouse close did not dismiss subagent overlay")
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
	updated, cmd := model.submitInteractiveLine("/subagent", "/subagent", nil)
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
