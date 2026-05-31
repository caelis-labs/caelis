package tuiapp

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	tea "charm.land/bubbletea/v2"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

const transcriptActionTokenPrefix = "transcript_action:"

func transcriptActionClickToken(action appviewmodel.TranscriptAction) string {
	if strings.TrimSpace(action.Command) == "" || !action.Enabled {
		return ""
	}
	raw, err := json.Marshal(action)
	if err != nil {
		return ""
	}
	return transcriptActionTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
}

func transcriptActionFromClickToken(token string) (appviewmodel.TranscriptAction, bool) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(token), transcriptActionTokenPrefix)
	if !ok || strings.TrimSpace(raw) == "" {
		return appviewmodel.TranscriptAction{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return appviewmodel.TranscriptAction{}, false
	}
	var action appviewmodel.TranscriptAction
	if err := json.Unmarshal(data, &action); err != nil {
		return appviewmodel.TranscriptAction{}, false
	}
	if strings.TrimSpace(action.Command) == "" {
		return appviewmodel.TranscriptAction{}, false
	}
	return action, true
}

func appendTranscriptActionRows(rows []RenderedRow, blockID string, actions []appviewmodel.TranscriptAction, width int, ctx BlockRenderContext) []RenderedRow {
	for _, action := range actions {
		if !action.Enabled || strings.TrimSpace(action.Command) == "" {
			continue
		}
		token := transcriptActionClickToken(action)
		if token == "" {
			continue
		}
		plain := transcriptActionPlain(action)
		if width > 0 {
			plain = truncateTailDisplay(plain, maxInt(12, width))
		}
		styled := styleTranscriptActionRow(plain, action, ctx)
		rows = append(rows, StyledPlainClickableRow(blockID, plain, styled, token))
	}
	return rows
}

func transcriptActionPlain(action appviewmodel.TranscriptAction) string {
	label := firstNonEmpty(action.Label, action.Kind, action.ID, "Action")
	target := strings.TrimSpace(action.TargetID)
	if target != "" {
		return "  action: " + label + " " + target
	}
	return "  action: " + label
}

func styleTranscriptActionRow(plain string, action appviewmodel.TranscriptAction, ctx BlockRenderContext) string {
	prefix, rest, ok := strings.Cut(plain, ": ")
	if !ok {
		return ctx.Theme.HelpHintTextStyle().Render(plain)
	}
	label := strings.TrimSpace(firstNonEmpty(action.Label, action.Kind, action.ID, rest))
	target := strings.TrimSpace(action.TargetID)
	styled := ctx.Theme.TranscriptMetaStyle().Render(prefix + ": ")
	if action.Destructive {
		styled += ctx.Theme.ToolErrorStyle().Render(label)
	} else {
		styled += ctx.Theme.ToolNameStyle().Render(label)
	}
	if target != "" {
		styled += " " + ctx.Theme.HelpHintTextStyle().Render(target)
	}
	return styled
}

func (m *Model) tryTranscriptActionClickToken(token string) (bool, tea.Cmd) {
	action, ok := transcriptActionFromClickToken(token)
	if !ok {
		return false, nil
	}
	return m.beginTranscriptAction(action)
}

func (m *Model) beginTranscriptAction(action appviewmodel.TranscriptAction) (bool, tea.Cmd) {
	command := action.Command
	if strings.TrimSpace(command) == "" {
		return true, nil
	}
	if m.cfg.ExecuteLine == nil {
		m.setInputText(command)
		return true, nil
	}
	details := []PromptDetail{
		{Label: "Action", Value: firstNonEmpty(action.Label, action.Kind, action.ID), Emphasis: true},
		{Label: "Target", Value: strings.TrimSpace(action.TargetID)},
	}
	if action.RequiresInput {
		prefix := command
		if !strings.HasSuffix(prefix, " ") {
			prefix += " "
		}
		return m.beginCommandPanelAction(commandPanelAction{prompt: &commandPanelPrompt{
			title:   firstNonEmpty(action.Label, action.Kind, "Action"),
			prompt:  "Input",
			details: details,
			buildLine: func(value string) string {
				value = strings.TrimSpace(value)
				if value == "" {
					return ""
				}
				return prefix + value
			},
		}}, command)
	}
	if action.Destructive {
		return m.beginCommandPanelAction(commandPanelAction{prompt: confirmCommandPanelPrompt(
			firstNonEmpty(action.Label, action.Kind, "Action")+"?",
			"Confirm transcript action",
			details,
			strings.TrimSpace(command),
		)}, command)
	}
	return m.beginCommandPanelAction(commandPanelAction{line: command}, command)
}
