package tuiapp

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

func TestTeamPickerSearchAndCancelPreserveRoleWithoutView(t *testing.T) {
	m, service := newSubagentOverlayTestModel(t)
	// Exercise input dispatch without a render between events: the filtered
	// selection must already be authoritative when Enter arrives.
	_, _ = m.Update(tea.PasteMsg{Content: "orbit"})
	_, _ = m.Update(subagentSpecialKey(tea.KeyEnter))
	if row := m.currentSubagentRow(); row.binding.ProfileID != "provider:sol" || !row.current || row.binding.Effort != "high" {
		t.Fatalf("current binding was not selected: %#v", row)
	}
	for range 4 {
		_, _ = m.Update(subagentSpecialKey(tea.KeyRight))
	}
	if m.currentSubagentRow().binding.Effort != "xhigh" {
		t.Fatal("effort wrapped at the upper bound")
	}
	_, _ = m.Update(subagentSpecialKey(tea.KeyEscape))
	if m.subagentOverlay.query != "orbit" || m.currentSubagentRow().handle != agentbinding.HandleOrbit {
		t.Fatal("cancel lost the parent query or role")
	}
	_, _ = m.Update(subagentSpecialKey(tea.KeyEnter))
	if m.currentSubagentRow().binding.Effort != "high" {
		t.Fatal("cancel retained the unconfirmed effort")
	}
	_, _ = m.Update(tea.PasteMsg{Content: "Claude"})
	if row := m.currentSubagentRow(); row.binding.ProfileID != "acp:claude:opus" || row.binding.Effort != "none" {
		t.Fatalf("ACP search did not select its fixed effort: %#v", row)
	}
	_, _ = m.Update(subagentSpecialKey(tea.KeyEscape))
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'w', Mod: tea.ModCtrl}))
	for _, text := range []string{"f", "h", "l", "p", "n", "s", "d", "j", "k"} {
		_, _ = m.Update(keyPress(text))
	}
	if m.subagentOverlay.query != "fhlpnsdjk" || m.subagentOverlay.page != subagentPageMain {
		t.Fatal("printable search keys triggered an action")
	}
	_, cmd := m.Update(subagentSpecialKey(tea.KeyEnter))
	if cmd != nil || service.bindRequest.ProfileID != "" || service.reset != "" {
		t.Fatal("draft, cancel or empty search mutated configuration")
	}
}

func TestTeamPickerMutationBlocksDuplicateInputAndRetainsFailedDraft(t *testing.T) {
	m, service := newSubagentOverlayTestModel(t)
	_, _ = m.Update(tea.PasteMsg{Content: "orbit"})
	_, _ = m.Update(subagentSpecialKey(tea.KeyEnter))
	_, _ = m.Update(subagentSpecialKey(tea.KeyRight))
	_, cmd := m.Update(subagentSpecialKey(tea.KeyEnter))
	if cmd == nil || !m.subagentOverlay.pending {
		t.Fatal("confirm did not start a mutation")
	}
	for _, key := range []tea.KeyMsg{subagentSpecialKey(tea.KeyEnter), subagentSpecialKey(tea.KeyEscape), subagentSpecialKey(tea.KeyDown), keyPress("other")} {
		if _, duplicate := m.Update(key); duplicate != nil {
			t.Fatal("input dispatched another action while saving")
		}
	}
	m.renderSubagentOverlay()
	g := m.subagentOverlay.geometry
	_, _ = m.handleSubagentOverlayMouse(tea.MouseClickMsg(tea.Mouse{X: g.closeX, Y: g.closeY, Button: tea.MouseLeft}))
	_, _ = m.handleSubagentOverlayMouse(tea.MouseReleaseMsg(tea.Mouse{X: g.closeX, Y: g.closeY}))
	if m.subagentOverlay == nil || m.currentSubagentRow().binding.Effort != "xhigh" || m.subagentOverlay.query != "" {
		t.Fatal("saving allowed the draft or overlay to change")
	}
	msg := cmd().(subagentOverlayResultMsg)
	if service.bindRequest.Effort != "xhigh" {
		t.Fatal("confirmation did not capture the selected draft")
	}
	msg.err = errors.New("Host rejected the binding")
	_, _ = m.Update(msg)
	if m.subagentOverlay.pending || m.currentSubagentRow().binding.Effort != "xhigh" || m.subagentOverlay.page != subagentPageBinding {
		t.Fatal("failure lost the editable draft")
	}
	_, retry := m.Update(subagentSpecialKey(tea.KeyEnter))
	if retry == nil {
		t.Fatal("failed binding could not be retried")
	}
	_, _ = m.Update(retry())
	if m.subagentOverlay.page != subagentPageMain || m.subagentOverlay.query != "orbit" || m.currentSubagentRow().handle != agentbinding.HandleOrbit {
		t.Fatal("successful retry lost the original role")
	}
}

func TestTeamPickerLoadFailureCanRetryAndEmptySearchCannotAct(t *testing.T) {
	m, _ := newSubagentOverlayTestModel(t)
	open := m.openSubagentOverlay()
	msg := open().(subagentOverlayResultMsg)
	msg.status = agentbinding.Status{}
	msg.err = errors.New("Host unavailable")
	_, _ = m.Update(msg)
	frame := ansi.Strip(m.renderSubagentOverlay())
	for _, want := range []string{"Could not load", "Host unavailable", "enter retry"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("missing %q in error frame:\n%s", want, frame)
		}
	}
	_, retry := m.Update(subagentSpecialKey(tea.KeyEnter))
	if retry == nil {
		t.Fatal("load failure could not retry")
	}
	_, _ = m.Update(retry())
	_, _ = m.Update(tea.PasteMsg{Content: "no matches"})
	if _, cmd := m.Update(subagentSpecialKey(tea.KeyEnter)); cmd != nil {
		t.Fatal("empty search dispatched an action")
	}
	if !strings.Contains(ansi.Strip(m.renderSubagentOverlay()), "No matches") {
		t.Fatal("empty search omitted recovery guidance")
	}
}

func TestTeamPickerNewRoleDraftAndSnapshotNavigation(t *testing.T) {
	m, service := newSubagentOverlayTestModel(t)
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'n', Mod: tea.ModCtrl}))
	_, _ = m.Update(tea.PasteMsg{Content: "research"})
	_, _ = m.Update(subagentSpecialKey(tea.KeyEnter))
	_, _ = m.Update(tea.PasteMsg{Content: "Investigate unfamiliar systems"})
	_, _ = m.Update(subagentSpecialKey(tea.KeyEnter))
	_, _ = m.Update(subagentSpecialKey(tea.KeyEnter))
	_, _ = m.Update(subagentSpecialKey(tea.KeyEnter))
	if m.subagentOverlay.page != subagentPageNewRole || m.subagentOverlay.roleBinding.ProfileID == "" || m.currentSubagentRow().key != "field:binding" {
		t.Fatal("model selection did not return to the new role draft")
	}
	_, _ = m.Update(subagentSpecialKey(tea.KeyEscape))
	if service.createdRole.Handle != "" || service.bindRequest.ProfileID != "" {
		t.Fatal("cancelled new role persisted its binding")
	}
	m.subagentOverlay.status.Sets = []agentbinding.BindingSetStatus{{Name: "focused", Available: true}}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'p', Mod: tea.ModCtrl}))
	_, _ = m.Update(tea.PasteMsg{Content: "focused"})
	_, _ = m.Update(subagentSpecialKey(tea.KeyDelete))
	if m.quit || m.subagentOverlay.page != subagentPageConfirm || m.currentSubagentRow().action != subagentActionCancel {
		t.Fatal("delete shortcut did not open a cancel-first confirmation")
	}
	_, _ = m.Update(subagentSpecialKey(tea.KeyEnter))
	if m.subagentOverlay.page != subagentPageSets || m.subagentOverlay.query != "focused" || m.currentSubagentRow().key != "set:focused" {
		t.Fatal("cancelled deletion did not restore the binding set")
	}
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl}))
	_, _ = m.Update(subagentSpecialKey(tea.KeyTab))
	_, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if m.currentSubagentRow().key != "field:set-name" {
		t.Fatal("shift-tab did not return to the name field")
	}
	_, _ = m.Update(subagentSpecialKey(tea.KeyEscape))
	if m.subagentOverlay.query != "focused" || m.currentSubagentRow().key != "set:focused" {
		t.Fatal("cancelled save lost the binding set")
	}
}

func TestTeamPickerLongListGeometryAndMouseStayAligned(t *testing.T) {
	for _, size := range [][2]int{{132, 37}, {80, 23}, {60, 18}, {35, 16}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			m, _ := newSubagentOverlayTestModel(t)
			for i := range 24 {
				m.subagentOverlay.status.Handles = append(m.subagentOverlay.status.Handles, agentbinding.HandleStatus{
					Definition: agentbinding.Definition{Handle: agentbinding.Handle(fmt.Sprintf("role-%02d", i)), Configurable: true, Custom: true},
				})
			}
			_, _ = m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m.renderSubagentOverlay()
			for range len(m.subagentOverlay.rows) {
				m.moveSubagentSelection(1)
				frame := ansi.Strip(m.renderSubagentOverlay())
				g := m.subagentOverlay.geometry
				if g.width > size[0] || g.height > size[1] || g.rows[m.subagentOverlay.index] < 0 {
					t.Fatalf("selection escaped the physical frame: %#v\n%s", g, frame)
				}
				for _, line := range strings.Split(frame, "\n") {
					if displayColumns(line) > size[0] {
						t.Fatalf("row overflow: %q", line)
					}
				}
			}
			m.moveSubagentSelection(-2)
			m.renderSubagentOverlay()
			state := m.subagentOverlay
			g, before := state.geometry, state.index
			_, _ = m.handleSubagentOverlayMouse(tea.MouseMotionMsg(tea.Mouse{X: g.x - 1, Y: g.rows[before]}))
			if state.index != before {
				t.Fatal("outside hover changed selection")
			}
			_, _ = m.handleSubagentOverlayMouse(tea.MouseClickMsg(tea.Mouse{X: g.x + 2, Y: g.rows[before], Button: tea.MouseLeft}))
			_, cmd := m.handleSubagentOverlayMouse(tea.MouseReleaseMsg(tea.Mouse{X: g.x - 1, Y: g.rows[before]}))
			if cmd != nil || state.page != subagentPageMain {
				t.Fatal("outside release activated a row")
			}
			for index, y := range g.rows {
				if y < 0 {
					continue
				}
				_, _ = m.handleSubagentOverlayMouse(tea.MouseMotionMsg(tea.Mouse{X: g.x + 2, Y: y}))
				m.renderSubagentOverlay()
				if state.index != index || state.geometry.rows[index] != y {
					t.Fatal("hover moved the row under the pointer")
				}
			}
		})
	}
}

func TestTeamPickerClickTextFieldFocusesWithoutAdvancing(t *testing.T) {
	m, _ := newSubagentOverlayTestModel(t)
	m.openNewSubagentRole()
	m.moveSubagentSelection(2)
	m.renderSubagentOverlay()
	g := m.subagentOverlay.geometry
	_, _ = m.handleSubagentOverlayMouse(tea.MouseClickMsg(tea.Mouse{X: g.x + 5, Y: g.rows[0], Button: tea.MouseLeft}))
	_, _ = m.handleSubagentOverlayMouse(tea.MouseReleaseMsg(tea.Mouse{X: g.x + 5, Y: g.rows[0]}))
	_, _ = m.Update(tea.PasteMsg{Content: "research"})
	if m.currentSubagentRow().key != "field:handle" || m.subagentOverlay.roleHandle != "research" || m.subagentOverlay.roleDescription != "" {
		t.Fatal("clicking the handle field redirected editing to another field")
	}
}
