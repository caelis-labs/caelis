package tuiapp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/experimental"
)

type experimentalState struct {
	snapshot experimental.Snapshot
	draft    experimental.Values
	selected int
	busy     bool
	loaded   bool
	err      string
	request  *appserver.ExperimentalRequest
}
type experimentalResultMsg struct {
	state         *experimentalState
	snapshot      experimental.Snapshot
	saving        bool
	knownTerminal bool
	err           error
}

func (m *Model) openExperimental() tea.Cmd {
	m.resetComposerAfterOverlayOpen()
	state := &experimentalState{busy: true}
	m.experimental = state
	client, initialize, id, baseCtx := m.cfg.Experimental, m.cfg.ExperimentalInitialize, m.currentSessionID, m.cfg.Context
	return func() tea.Msg {
		msg := experimentalResultMsg{state: state}
		if client == nil || initialize == nil {
			msg.err = errors.New("this Host does not support experimental features")
			return msg
		}
		ctx, cancel := context.WithTimeout(contextOrBackground(baseCtx), 10*time.Second)
		defer cancel()
		info, err := initialize(ctx)
		if err == nil && !slices.Contains(info.Capabilities, appserver.CapabilityExperimental) {
			err = errors.New("this Host does not advertise experimental-features-v1")
		}
		if err == nil {
			msg.snapshot, err = client.ExperimentalFeatures(ctx, id)
		}
		msg.err = err
		return msg
	}
}

func (m *Model) handleExperimentalKey(msg tea.KeyMsg) tea.Cmd {
	state := m.experimental
	switch msg.String() {
	case "esc", "ctrl+c":
		// Closing never cancels or replays a dispatched write. Its original receipt
		// remains with Control; reopening reads the authoritative configuration.
		m.experimental = nil
		return nil
	}
	if state.busy {
		return nil
	}
	switch msg.String() {
	case "up", "left":
		state.selected = max(0, state.selected-1)
	case "down", "right":
		state.selected = min(max(0, len(state.snapshot.Features)-1), state.selected+1)
	case "space", " ":
		if state.loaded && state.request == nil && len(state.snapshot.Features) > 0 {
			id := state.snapshot.Features[state.selected].ID
			state.draft[id] = !state.draft[id]
		}
	case "enter":
		if !state.loaded {
			return m.openExperimental()
		}
		return m.saveExperimental()
	}
	return nil
}

func (m *Model) saveExperimental() tea.Cmd {
	state := m.experimental
	if state.request == nil {
		values := experimental.Values{}
		for id, value := range state.draft {
			if value != state.snapshot.Desired[id] {
				values[id] = value
			}
		}
		if len(values) == 0 {
			m.experimental = nil
			return nil
		}
		revision, err := strconv.ParseUint(state.snapshot.Revision, 10, 64)
		if err != nil {
			state.err = "Invalid Host configuration revision."
			return nil
		}
		state.request = &appserver.ExperimentalRequest{WriteBase: appserver.WriteBase{OperationID: "experimental-" + rand.Text(), ExpectedRevision: &revision}, Values: values}
	}
	state.busy = true
	state.err = ""
	request, client, id, baseCtx := *state.request, m.cfg.Experimental, m.currentSessionID, m.cfg.Context
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(contextOrBackground(baseCtx), 10*time.Second)
		defer cancel()
		result, err := client.SetExperimentalFeatures(ctx, request)
		msg := experimentalResultMsg{state: state, saving: true, err: err, knownTerminal: result.Outcome == appserver.OutcomeCommitted || result.Outcome == appserver.OutcomeRejected || result.Outcome == appserver.OutcomeConflicted}
		if err == nil && result.Outcome != appserver.OutcomeCommitted {
			msg.err = fmt.Errorf("save is %s; check the original operation before retrying", result.Outcome)
		}
		// Read even after a rejected CAS so a deliberate next save uses fresh truth.
		snapshot, readErr := client.ExperimentalFeatures(ctx, id)
		if readErr == nil {
			msg.snapshot = snapshot
		}
		if msg.err != nil {
			return msg
		}
		if readErr != nil {
			msg.err = readErr
			return msg
		}
		for key, want := range request.Values {
			if snapshot.Desired[key] != want {
				msg.err = errors.New("saved values changed before readback; your selections are retained")
				return msg
			}
		}
		return msg
	}
}

func (m *Model) finishExperimental(msg experimentalResultMsg) tea.Cmd {
	if m.experimental != msg.state {
		return nil
	}
	state := m.experimental
	state.busy = false
	// Operation terminality is independent of the current shared configuration.
	// A later writer or failed readback cannot make a committed receipt unknown.
	if msg.knownTerminal {
		state.request = nil
	}
	if msg.snapshot.Revision != "" {
		state.snapshot = msg.snapshot
	}
	if msg.err != nil {
		state.err = msg.err.Error()
		return nil
	}
	if msg.saving {
		m.experimental = nil
		return m.showHint("Experimental features saved. Applies at the next Runtime activation; no App restart needed.", hintOptions{priority: HintPriorityNormal, clearAfter: copyHintDuration})
	}
	state.loaded = true
	state.draft = maps.Clone(msg.snapshot.Desired)
	state.err = ""
	return nil
}

func (m *Model) renderExperimental() string {
	state := m.experimental
	if state == nil {
		return ""
	}
	width := max(24, min(100, m.width-6))
	lines := []string{"Experimental features", "Host defaults · next Runtime activation · no App restart", "Loaded from saved settings · Space edits; Enter saves."}
	if state.loaded {
		effective := "No active Runtime for this Session."
		if state.snapshot.Effective != nil {
			var names []string
			for _, f := range state.snapshot.Features {
				if state.snapshot.Effective[f.ID] {
					names = append(names, f.Name)
				}
			}
			effective = "Current Runtime: all off."
			if len(names) > 0 {
				effective = "Current Runtime: " + strings.Join(names, ", ") + "."
			}
		}
		lines = append(lines, effective, "")
		for i, f := range state.snapshot.Features {
			cursor, mark := " ", " "
			if i == state.selected {
				cursor = "›"
			}
			if state.draft[f.ID] {
				mark = "x"
			}
			label := fmt.Sprintf("%s [%s] %-25s", cursor, mark, f.Name)
			line := label + "  " + f.Description
			if width < 78 {
				line = fmt.Sprintf("%s [%s] %s\n%s", cursor, mark, f.Name, lipgloss.NewStyle().Width(width-6).PaddingLeft(6).Render(f.Description))
			}
			if i == state.selected {
				line = m.theme.HelpHintTextStyle().Bold(true).Render(line)
			}
			lines = append(lines, line)
		}
	}
	if state.busy {
		if state.loaded {
			lines = append(lines, "Saving… Esc closes; saving continues.")
		} else {
			lines = append(lines, "Loading Host settings…")
		}
	}
	if state.err != "" {
		lines = append(lines, "", "Save/read error: "+state.err)
	}
	hint := "↑↓ move · Space toggle · Enter save · Esc discard and close"
	if state.request != nil && !state.busy {
		hint = "Selections retained · Enter checks original save · Esc closes"
	}
	lines = append(lines, "", hint)
	return lipgloss.NewStyle().Width(width).Padding(1, 2).Border(lipgloss.RoundedBorder()).Render(strings.Join(lines, "\n"))
}
