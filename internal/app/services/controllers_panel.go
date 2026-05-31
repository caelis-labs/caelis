package services

import (
	"context"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type ControllerPanelRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
}

func (s ControllerService) Panel(ctx context.Context, req ControllerPanelRequest) (appviewmodel.ControllerPanelView, error) {
	status, ok, err := s.Status(ctx, req.SessionRef)
	if err != nil {
		return appviewmodel.ControllerPanelView{}, err
	}
	if !ok {
		return appviewmodel.ControllerPanelView{
			Active: false,
			Diagnostics: []appviewmodel.ControllerPanelDiagnostic{{
				Severity: "info",
				Kind:     "controller_inactive",
				Message:  "no active ACP controller",
			}},
		}, nil
	}
	view := controllerStatusView(status)
	if view == nil {
		return appviewmodel.ControllerPanelView{}, nil
	}
	return controllerPanelFromStatus(*view), nil
}

func controllerPanelFromStatus(status appviewmodel.ControllerStatus) appviewmodel.ControllerPanelView {
	view := appviewmodel.ControllerPanelView{
		Active:      true,
		Status:      cloneControllerStatus(status),
		Summary:     controllerPanelSummary(status),
		Sections:    controllerPanelSections(status),
		Actions:     controllerPanelActions(status),
		Diagnostics: controllerPanelDiagnostics(status.Diagnostics),
	}
	return view
}

func cloneControllerStatus(status appviewmodel.ControllerStatus) *appviewmodel.ControllerStatus {
	out := status
	out.ModelOptions = cloneControllerChoices(status.ModelOptions)
	out.EffortOptions = cloneControllerChoices(status.EffortOptions)
	out.EffortOptionsByModel = cloneControllerChoicesMap(status.EffortOptionsByModel)
	out.Commands = append([]appviewmodel.ControllerCommand(nil), status.Commands...)
	out.ConfigOptions = cloneControllerConfigOptionsView(status.ConfigOptions)
	out.ModeOptions = append([]appviewmodel.ControllerMode(nil), status.ModeOptions...)
	if status.Lifecycle != nil {
		lifecycle := *status.Lifecycle
		out.Lifecycle = &lifecycle
	}
	out.Diagnostics = cloneControllerDiagnostics(status.Diagnostics)
	return &out
}

func controllerPanelSummary(status appviewmodel.ControllerStatus) appviewmodel.ControllerPanelSummary {
	summary := appviewmodel.ControllerPanelSummary{
		Agent:           strings.TrimSpace(status.Agent),
		RemoteSessionID: strings.TrimSpace(status.RemoteSessionID),
		Model:           strings.TrimSpace(status.Model),
		ReasoningEffort: strings.TrimSpace(status.ReasoningEffort),
		Mode:            strings.TrimSpace(status.Mode),
	}
	if status.Lifecycle != nil {
		summary.Phase = strings.TrimSpace(status.Lifecycle.Phase)
		summary.Running = status.Lifecycle.Running
		summary.Recovering = status.Lifecycle.Recovering
		summary.UpdatedAt = status.Lifecycle.UpdatedAt
		if summary.RemoteSessionID == "" {
			summary.RemoteSessionID = strings.TrimSpace(status.Lifecycle.RemoteSessionID)
		}
	}
	if summary.UpdatedAt.IsZero() {
		summary.UpdatedAt = status.UpdatedAt
	}
	return summary
}

func controllerPanelSections(status appviewmodel.ControllerStatus) []appviewmodel.ControllerPanelSection {
	sections := []appviewmodel.ControllerPanelSection{
		controllerLifecycleSection(status),
		controllerConfigSection(status),
	}
	out := sections[:0]
	for _, section := range sections {
		if len(section.Fields) > 0 || len(section.Actions) > 0 {
			out = append(out, section)
		}
	}
	return out
}

func controllerLifecycleSection(status appviewmodel.ControllerStatus) appviewmodel.ControllerPanelSection {
	fields := []appviewmodel.ControllerPanelField{
		controllerPanelField("controller.agent", "Agent", "text", status.Agent, false, nil),
		controllerPanelField("controller.remote_session", "Remote session", "text", firstNonEmpty(status.RemoteSessionID, controllerLifecycleRemoteSession(status)), false, nil),
	}
	if status.Lifecycle != nil {
		fields = append(fields,
			controllerPanelField("controller.phase", "Phase", "text", status.Lifecycle.Phase, false, nil),
			controllerPanelField("controller.turn", "Turn", "text", status.Lifecycle.TurnID, false, nil),
		)
		if status.Lifecycle.Error != "" {
			fields = append(fields, controllerPanelField("controller.error", "Error", "text", status.Lifecycle.Error, false, nil))
		}
	}
	return appviewmodel.ControllerPanelSection{
		ID:      "lifecycle",
		Title:   "Lifecycle",
		Fields:  compactControllerPanelFields(fields),
		Actions: []appviewmodel.ControllerPanelAction{controllerPanelAction("controller.handoff.local", "handoff", "Return to local", true, false, false)},
	}
}

func controllerConfigSection(status appviewmodel.ControllerStatus) appviewmodel.ControllerPanelSection {
	fields := []appviewmodel.ControllerPanelField{
		controllerPanelField("controller.model", "Model", "select", status.Model, len(status.ModelOptions) > 0, status.ModelOptions),
		controllerPanelField("controller.reasoning", "Reasoning", "select", status.ReasoningEffort, len(status.EffortOptions) > 0, status.EffortOptions),
		controllerPanelField("controller.mode", "Mode", "select", status.Mode, len(status.ModeOptions) > 0, controllerModeChoices(status.ModeOptions)),
	}
	for _, option := range status.ConfigOptions {
		optionID := strings.TrimSpace(option.ID)
		if optionID == "" {
			continue
		}
		switch strings.ToLower(optionID) {
		case "model", "reasoning", "mode":
			continue
		}
		fieldID := "controller.config." + optionID
		if controllerPanelHasField(fields, fieldID) {
			continue
		}
		fields = append(fields, controllerPanelField(
			fieldID,
			firstNonEmpty(option.Name, option.ID),
			firstNonEmpty(option.Type, "text"),
			option.CurrentValue,
			len(option.Options) > 0,
			option.Options,
		))
	}
	return appviewmodel.ControllerPanelSection{
		ID:      "configuration",
		Title:   "Configuration",
		Fields:  compactControllerPanelFields(fields),
		Actions: controllerConfigActions(status),
	}
}

func controllerConfigActions(status appviewmodel.ControllerStatus) []appviewmodel.ControllerPanelAction {
	return []appviewmodel.ControllerPanelAction{
		controllerPanelAction("controller.model.set", "set_model", "Set model", len(status.ModelOptions) > 0, true, false),
		controllerPanelAction("controller.mode.set", "set_mode", "Set mode", len(status.ModeOptions) > 0, true, false),
		controllerPanelAction("controller.mode.cycle", "cycle_mode", "Cycle mode", len(status.ModeOptions) > 1, false, false),
	}
}

func controllerPanelActions(status appviewmodel.ControllerStatus) []appviewmodel.ControllerPanelAction {
	actions := []appviewmodel.ControllerPanelAction{controllerPanelAction("controller.handoff.local", "handoff", "Return to local", true, false, false)}
	actions = append(actions, controllerConfigActions(status)...)
	return actions
}

func controllerPanelDiagnostics(in []appviewmodel.ControllerDiagnostic) []appviewmodel.ControllerPanelDiagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerPanelDiagnostic, 0, len(in))
	for _, diagnostic := range in {
		out = append(out, appviewmodel.ControllerPanelDiagnostic{
			Severity: strings.TrimSpace(diagnostic.Severity),
			Kind:     strings.TrimSpace(diagnostic.Kind),
			Message:  strings.TrimSpace(diagnostic.Message),
			Meta:     maps.Clone(diagnostic.Meta),
		})
	}
	return out
}

func controllerPanelField(id string, label string, kind string, value string, editable bool, options []appviewmodel.ControllerConfigChoice) appviewmodel.ControllerPanelField {
	return appviewmodel.ControllerPanelField{
		ID:       strings.TrimSpace(id),
		Label:    strings.TrimSpace(label),
		Kind:     strings.TrimSpace(kind),
		Value:    strings.TrimSpace(value),
		Editable: editable,
		Options:  cloneControllerChoices(options),
	}
}

func controllerPanelAction(id string, kind string, label string, enabled bool, requiresInput bool, destructive bool) appviewmodel.ControllerPanelAction {
	return appviewmodel.ControllerPanelAction{
		ID:            strings.TrimSpace(id),
		Kind:          strings.TrimSpace(kind),
		Label:         strings.TrimSpace(label),
		Enabled:       enabled,
		RequiresInput: requiresInput,
		Destructive:   destructive,
	}
}

func compactControllerPanelFields(fields []appviewmodel.ControllerPanelField) []appviewmodel.ControllerPanelField {
	out := make([]appviewmodel.ControllerPanelField, 0, len(fields))
	for _, field := range fields {
		if strings.TrimSpace(field.ID) == "" || strings.TrimSpace(field.Value) == "" && !field.Editable && len(field.Options) == 0 {
			continue
		}
		out = append(out, field)
	}
	return out
}

func controllerPanelHasField(fields []appviewmodel.ControllerPanelField, id string) bool {
	for _, field := range fields {
		if strings.EqualFold(strings.TrimSpace(field.ID), strings.TrimSpace(id)) {
			return true
		}
	}
	return false
}

func controllerLifecycleRemoteSession(status appviewmodel.ControllerStatus) string {
	if status.Lifecycle == nil {
		return ""
	}
	return strings.TrimSpace(status.Lifecycle.RemoteSessionID)
}

func controllerModeChoices(modes []appviewmodel.ControllerMode) []appviewmodel.ControllerConfigChoice {
	if len(modes) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerConfigChoice, 0, len(modes))
	for _, mode := range modes {
		value := strings.TrimSpace(mode.ID)
		if value == "" {
			continue
		}
		out = append(out, appviewmodel.ControllerConfigChoice{
			Value:       value,
			Name:        strings.TrimSpace(mode.Name),
			Description: strings.TrimSpace(mode.Description),
		})
	}
	return out
}

func cloneControllerChoices(in []appviewmodel.ControllerConfigChoice) []appviewmodel.ControllerConfigChoice {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerConfigChoice, len(in))
	copy(out, in)
	return out
}

func cloneControllerChoicesMap(in map[string][]appviewmodel.ControllerConfigChoice) map[string][]appviewmodel.ControllerConfigChoice {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]appviewmodel.ControllerConfigChoice, len(in))
	for key, choices := range in {
		out[key] = cloneControllerChoices(choices)
	}
	return out
}

func cloneControllerConfigOptionsView(in []appviewmodel.ControllerConfigOption) []appviewmodel.ControllerConfigOption {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerConfigOption, 0, len(in))
	for _, option := range in {
		option.Options = cloneControllerChoices(option.Options)
		out = append(out, option)
	}
	return out
}

func cloneControllerDiagnostics(in []appviewmodel.ControllerDiagnostic) []appviewmodel.ControllerDiagnostic {
	if len(in) == 0 {
		return nil
	}
	out := make([]appviewmodel.ControllerDiagnostic, 0, len(in))
	for _, diagnostic := range in {
		diagnostic.Meta = maps.Clone(diagnostic.Meta)
		out = append(out, diagnostic)
	}
	return out
}
