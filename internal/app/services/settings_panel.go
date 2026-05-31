package services

import (
	"context"
	"errors"
	"maps"
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appresources "github.com/OnslaughtSnail/caelis/internal/app/resources"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

const (
	settingsActionSandboxPrepare   = "sandbox.prepare"
	settingsActionSandboxRepair    = "sandbox.repair"
	settingsActionSandboxPreflight = "sandbox.preflight"
	settingsActionSandboxReset     = "sandbox.reset"
	settingsActionModelConnect     = "model.connect"
)

type SettingsPanelRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
}

type SettingsPanelActionRequest struct {
	SessionRef             session.Ref `json:"session_ref,omitempty"`
	ActionID               string      `json:"action_id,omitempty"`
	AllowNonElevatedRepair bool        `json:"allow_non_elevated_repair,omitempty"`
}

func (s SettingsService) Panel(ctx context.Context, req SettingsPanelRequest) (appviewmodel.SettingsPanelView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return appviewmodel.SettingsPanelView{}, err
	}
	doc, err := s.Document(ctx)
	if err != nil {
		return appviewmodel.SettingsPanelView{}, err
	}
	settingsView := settingsViewFromDocument(doc)
	status, err := s.services.Status().View(ctx, StatusRequest{SessionRef: req.SessionRef})
	if err != nil {
		return appviewmodel.SettingsPanelView{}, err
	}
	sandboxStatus, err := s.services.Sandbox().Status(ctx)
	if err != nil {
		return appviewmodel.SettingsPanelView{}, err
	}
	sandboxActions := sandboxPanelActions(sandboxStatus)
	view := appviewmodel.SettingsPanelView{
		Configured: s.Configured(),
		Settings:   settingsView,
		Runtime:    status.Runtime,
		Model:      status.Model,
		Agents:     status.Agents,
		Sandbox: appviewmodel.SandboxPanel{
			Status:  sandboxPanelStatus(sandboxStatus),
			Actions: sandboxActions,
		},
		Resources: status.Resources,
		Actions:   append([]appviewmodel.SettingsPanelAction(nil), sandboxActions...),
	}
	if !view.Model.Configured {
		view.Actions = append(view.Actions, modelConnectAction())
	}
	view.Sections = settingsPanelSections(view, doc)
	view.ConfigOptions = settingsConfigOptionsFromPanel(view)
	view.Diagnostics = settingsPanelDiagnostics(view, sandboxStatus)
	return view, nil
}

func (s SettingsService) RunPanelAction(ctx context.Context, req SettingsPanelActionRequest) (appviewmodel.SettingsPanelView, error) {
	actionID := strings.ToLower(strings.TrimSpace(req.ActionID))
	switch actionID {
	case settingsActionSandboxPrepare:
		_, err := s.services.Sandbox().Prepare(ctx)
		if err != nil {
			return appviewmodel.SettingsPanelView{}, err
		}
	case settingsActionSandboxRepair:
		_, err := s.services.Sandbox().Repair(ctx)
		if err != nil {
			return appviewmodel.SettingsPanelView{}, err
		}
	case settingsActionSandboxPreflight:
		_, err := s.services.Sandbox().Preflight(ctx, req.AllowNonElevatedRepair)
		if err != nil {
			return appviewmodel.SettingsPanelView{}, err
		}
	case settingsActionSandboxReset:
		_, err := s.services.Sandbox().Reset(ctx)
		if err != nil {
			return appviewmodel.SettingsPanelView{}, err
		}
	default:
		return appviewmodel.SettingsPanelView{}, errors.New("app/services: unknown settings panel action " + strings.TrimSpace(req.ActionID))
	}
	return s.Panel(ctx, SettingsPanelRequest{SessionRef: req.SessionRef})
}

func settingsPanelSections(view appviewmodel.SettingsPanelView, doc appsettings.Document) []appviewmodel.SettingsPanelSection {
	settings := view.Settings
	editable := view.Configured
	skillBudget := appsettings.NormalizeSkillPolicy(doc.Skills).MaxExpansionChars
	if skillBudget <= 0 {
		skillBudget = appsettings.DefaultSkillExpansionChars
	}
	return []appviewmodel.SettingsPanelSection{
		{
			ID:    "runtime",
			Title: "Runtime",
			Fields: []appviewmodel.SettingsPanelField{
				textSettingsField("runtime.app_name", "App", settings.Runtime.AppName, false),
				textSettingsField("runtime.user_id", "User", settings.Runtime.UserID, false),
				textSettingsField("runtime.workspace_key", "Workspace key", settings.Runtime.WorkspaceKey, editable),
				textSettingsField("runtime.workspace_cwd", "Workspace path", settings.Runtime.WorkspaceCWD, editable),
				textSettingsField("runtime.model", "Default model", settings.Runtime.Model, editable),
			},
		},
		{
			ID:    "model",
			Title: "Models",
			Fields: []appviewmodel.SettingsPanelField{
				textSettingsField("model.configured_count", "Configured models", strconv.Itoa(view.Model.Count), false),
				textSettingsField("model.current", "Current model", currentModelPanelValue(view.Model), false),
			},
			Actions: modelSectionActions(view),
		},
		{
			ID:    "store",
			Title: "Store",
			Fields: []appviewmodel.SettingsPanelField{
				selectSettingsField("store.backend", "Backend", settings.Store.Backend, editable, []appviewmodel.SettingsPanelFieldOption{
					{Value: "jsonl", Label: "JSONL"},
					{Value: "sqlite", Label: "SQLite"},
					{Value: "memory", Label: "Memory"},
				}),
				textSettingsField("store.uri", "URI", settings.Store.URI, editable),
			},
		},
		{
			ID:      "sandbox",
			Title:   "Sandbox",
			Fields:  sandboxPanelFields(settings.Sandbox, view.Sandbox.Status, editable),
			Actions: append([]appviewmodel.SettingsPanelAction(nil), view.Sandbox.Actions...),
		},
		{
			ID:    "prompt",
			Title: "Prompt",
			Fields: []appviewmodel.SettingsPanelField{
				selectSettingsField("prompt.agent_instructions", "Agent instructions", settings.Prompt.AgentInstructions, editable, []appviewmodel.SettingsPanelFieldOption{
					{Value: appsettings.PromptAgentInstructionsAll, Label: "All"},
					{Value: appsettings.PromptAgentInstructionsWorkspaceOnly, Label: "Workspace only"},
					{Value: appsettings.PromptAgentInstructionsDisabled, Label: "Disabled"},
				}),
				selectSettingsField("prompt.plugin_prompts", "Plugin prompts", settings.Prompt.PluginPrompts, editable, []appviewmodel.SettingsPanelFieldOption{
					{Value: appsettings.PromptPluginPromptsEnabled, Label: "Enabled"},
					{Value: appsettings.PromptPluginPromptsDisabled, Label: "Disabled"},
				}),
				selectSettingsField("prompt.environment", "Environment context", settings.Prompt.Environment, editable, []appviewmodel.SettingsPanelFieldOption{
					{Value: appsettings.PromptEnvironmentEnabled, Label: "Enabled"},
					{Value: appsettings.PromptEnvironmentDisabled, Label: "Disabled"},
				}),
			},
		},
		{
			ID:    "compaction",
			Title: "Compaction",
			Fields: []appviewmodel.SettingsPanelField{
				selectSettingsField("compaction.auto_mode", "Automatic compaction", settings.Compaction.AutoMode, editable, []appviewmodel.SettingsPanelFieldOption{
					{Value: "", Label: "Default"},
					{Value: "enabled", Label: "Enabled"},
					{Value: "disabled", Label: "Disabled"},
				}),
				numberSettingsField("compaction.watermark", "Watermark", strconv.FormatFloat(settings.Compaction.AutoWatermarkRatio, 'f', -1, 64), editable),
				numberSettingsField("compaction.max_source_chars", "Max source chars", strconv.Itoa(settings.Compaction.MaxSourceChars), editable),
				numberSettingsField("compaction.retention.task_index_limit", "Task index limit", strconv.Itoa(settings.Compaction.TaskIndexLimit), editable),
				numberSettingsField("compaction.retention.controller_index_limit", "Controller index limit", strconv.Itoa(settings.Compaction.ControllerIndexLimit), editable),
				textSettingsField("compaction.prompt", "Prompt", settings.Compaction.Prompt, editable),
			},
		},
		{
			ID:    "skills",
			Title: "Skills",
			Fields: []appviewmodel.SettingsPanelField{
				selectSettingsField("skills.loading_mode", "Loading mode", settings.Skills.LoadingMode, editable, []appviewmodel.SettingsPanelFieldOption{
					{Value: appsettings.SkillLoadingModeExplicit, Label: "Explicit"},
					{Value: appsettings.SkillLoadingModeMetadataOnly, Label: "Metadata only"},
					{Value: appsettings.SkillLoadingModeDisabled, Label: "Disabled"},
				}),
				numberSettingsField("skills.max_expansion_chars", "Max expansion chars", strconv.Itoa(skillBudget), editable),
			},
		},
		{
			ID:    "resources",
			Title: "Resources",
			Fields: []appviewmodel.SettingsPanelField{
				textSettingsField("resources.plugins", "Plugins", strconv.Itoa(view.Resources.Plugins), false),
				textSettingsField("resources.prompts", "Prompts", strconv.Itoa(view.Resources.Prompts), false),
				textSettingsField("resources.skills", "Skills", strconv.Itoa(view.Resources.Skills), false),
				textSettingsField("resources.model_tools", "Model tools", strconv.Itoa(view.Resources.ModelTools), false),
				textSettingsField("resources.diagnostics", "Diagnostics", resourceDiagnosticSummary(view.Resources), false),
			},
		},
	}
}

func sandboxPanelFields(settings appviewmodel.SandboxSettings, status appviewmodel.SandboxPanelStatus, editable bool) []appviewmodel.SettingsPanelField {
	return []appviewmodel.SettingsPanelField{
		selectSettingsField("sandbox.backend", "Requested backend", firstNonEmpty(settings.Backend, status.RequestedBackend), editable, []appviewmodel.SettingsPanelFieldOption{
			{Value: "auto", Label: "Auto"},
			{Value: "host", Label: "Host"},
			{Value: "seatbelt", Label: "Seatbelt"},
			{Value: "bwrap", Label: "Bubblewrap"},
			{Value: "landlock", Label: "Landlock"},
			{Value: "windows", Label: "Windows"},
		}),
		textSettingsField("sandbox.resolved_backend", "Resolved backend", status.ResolvedBackend, false),
		textSettingsField("sandbox.route", "Route", status.Route, false),
		selectSettingsField("sandbox.network", "Network", settings.Network, editable, []appviewmodel.SettingsPanelFieldOption{
			{Value: "inherit", Label: "Inherit"},
			{Value: "enabled", Label: "Enabled"},
			{Value: "disabled", Label: "Disabled"},
		}),
		pathListSettingsField("sandbox.readable_roots", "Readable roots", settings.ReadableRoots, editable),
		pathListSettingsField("sandbox.writable_roots", "Writable roots", settings.WritableRoots, editable),
		textSettingsField("sandbox.helper_path", "Helper path", settings.HelperPath, editable),
	}
}

func settingsPanelDiagnostics(view appviewmodel.SettingsPanelView, sandboxStatus SandboxStatus) []appviewmodel.SettingsPanelDiagnostic {
	var out []appviewmodel.SettingsPanelDiagnostic
	if !view.Configured {
		out = append(out, appviewmodel.SettingsPanelDiagnostic{
			Severity: appresources.DiagnosticWarning,
			Source:   "settings",
			Kind:     "store",
			Message:  "settings manager is not configured; changes are unavailable",
		})
	}
	if !view.Model.Configured {
		out = append(out, appviewmodel.SettingsPanelDiagnostic{
			Severity:  appresources.DiagnosticWarning,
			Source:    "model",
			Kind:      "configuration",
			Message:   "no model is configured",
			ActionIDs: []string{settingsActionModelConnect},
		})
	}
	for _, diagnostic := range sandboxStatus.Diagnostics {
		out = append(out, settingsPanelDiagnosticFromSandbox(diagnostic))
	}
	for _, diagnostic := range view.Resources.Diagnostics {
		out = append(out, appviewmodel.SettingsPanelDiagnostic{
			Severity: strings.TrimSpace(diagnostic.Severity),
			Source:   "resources",
			Kind:     strings.TrimSpace(diagnostic.Kind),
			ID:       strings.TrimSpace(diagnostic.ID),
			Path:     strings.TrimSpace(diagnostic.Path),
			Message:  strings.TrimSpace(diagnostic.Message),
			Meta:     maps.Clone(diagnostic.Meta),
		})
	}
	return out
}

func settingsPanelDiagnosticFromSandbox(in SandboxDiagnostic) appviewmodel.SettingsPanelDiagnostic {
	kind := strings.TrimSpace(in.Kind)
	return appviewmodel.SettingsPanelDiagnostic{
		Severity:  strings.TrimSpace(in.Severity),
		Source:    "sandbox",
		Kind:      kind,
		Message:   strings.TrimSpace(in.Message),
		ActionIDs: sandboxDiagnosticActionIDs(kind),
		Meta:      maps.Clone(in.Meta),
	}
}

func sandboxDiagnosticActionIDs(kind string) []string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "setup":
		return []string{settingsActionSandboxPrepare, settingsActionSandboxRepair, settingsActionSandboxPreflight}
	case "fallback":
		return []string{settingsActionSandboxRepair, settingsActionSandboxPreflight}
	case "network", "roots":
		return []string{settingsActionSandboxPreflight}
	default:
		return nil
	}
}

func sandboxPanelStatus(in SandboxStatus) appviewmodel.SandboxPanelStatus {
	return appviewmodel.SandboxPanelStatus{
		RequestedBackend:         strings.TrimSpace(in.RequestedBackend),
		ResolvedBackend:          strings.TrimSpace(in.ResolvedBackend),
		Route:                    strings.TrimSpace(in.Route),
		Isolation:                strings.TrimSpace(in.Isolation),
		DefaultPermission:        strings.TrimSpace(in.DefaultPermission),
		Network:                  strings.TrimSpace(in.Network),
		DefaultNetwork:           strings.TrimSpace(in.DefaultNetwork),
		NetworkControl:           in.NetworkControl,
		PathPolicy:               in.PathPolicy,
		ReadableRootCount:        in.ReadableRootCount,
		WritableRootCount:        in.WritableRootCount,
		FallbackToHost:           in.FallbackToHost,
		FallbackReason:           strings.TrimSpace(in.FallbackReason),
		FallbackInstallHint:      strings.TrimSpace(in.FallbackInstallHint),
		SetupRequired:            in.SetupRequired,
		SetupError:               strings.TrimSpace(in.SetupError),
		SetupMarkerCurrent:       in.SetupMarkerCurrent,
		SetupMarkerReason:        strings.TrimSpace(in.SetupMarkerReason),
		SandboxRuntimeConfigured: in.SandboxRuntimeConfigured,
	}
}

func sandboxPanelActions(status SandboxStatus) []appviewmodel.SettingsPanelAction {
	enabled := status.SandboxRuntimeConfigured
	return []appviewmodel.SettingsPanelAction{
		{
			ID:          settingsActionSandboxPrepare,
			Label:       "Prepare",
			Description: "Prepare the selected sandbox backend.",
			Target:      "sandbox",
			Kind:        "lifecycle",
			Command:     "/settings run " + settingsActionSandboxPrepare,
			Enabled:     enabled,
		},
		{
			ID:          settingsActionSandboxRepair,
			Label:       "Repair",
			Description: "Repair or prepare the selected sandbox backend.",
			Target:      "sandbox",
			Kind:        "lifecycle",
			Command:     "/settings run " + settingsActionSandboxRepair,
			Enabled:     enabled,
		},
		{
			ID:          settingsActionSandboxPreflight,
			Label:       "Preflight",
			Description: "Run sandbox preflight checks.",
			Target:      "sandbox",
			Kind:        "diagnostic",
			Command:     "/settings run " + settingsActionSandboxPreflight,
			Enabled:     enabled,
		},
		{
			ID:                   settingsActionSandboxReset,
			Label:                "Reset",
			Description:          "Reset sandbox setup state when the backend supports it.",
			Target:               "sandbox",
			Kind:                 "lifecycle",
			Command:              "/settings run " + settingsActionSandboxReset,
			Enabled:              enabled,
			Destructive:          true,
			RequiresConfirmation: true,
		},
	}
}

func modelSectionActions(view appviewmodel.SettingsPanelView) []appviewmodel.SettingsPanelAction {
	if view.Model.Configured {
		return nil
	}
	return []appviewmodel.SettingsPanelAction{modelConnectAction()}
}

func modelConnectAction() appviewmodel.SettingsPanelAction {
	return appviewmodel.SettingsPanelAction{
		ID:          settingsActionModelConnect,
		Label:       "Connect model",
		Description: "Open the shared model connection flow.",
		Target:      "model",
		Kind:        "navigation",
		Command:     "/connect",
		Enabled:     true,
	}
}

func textSettingsField(id string, label string, value string, editable bool) appviewmodel.SettingsPanelField {
	return decorateSettingsPanelField(appviewmodel.SettingsPanelField{
		ID:       id,
		Label:    label,
		Kind:     "text",
		Value:    strings.TrimSpace(value),
		Editable: editable,
	})
}

func numberSettingsField(id string, label string, value string, editable bool) appviewmodel.SettingsPanelField {
	return decorateSettingsPanelField(appviewmodel.SettingsPanelField{
		ID:       id,
		Label:    label,
		Kind:     "number",
		Value:    strings.TrimSpace(value),
		Editable: editable,
	})
}

func selectSettingsField(id string, label string, value string, editable bool, options []appviewmodel.SettingsPanelFieldOption) appviewmodel.SettingsPanelField {
	return decorateSettingsPanelField(appviewmodel.SettingsPanelField{
		ID:       id,
		Label:    label,
		Kind:     "select",
		Value:    strings.TrimSpace(value),
		Editable: editable,
		Options:  append([]appviewmodel.SettingsPanelFieldOption(nil), options...),
	})
}

func pathListSettingsField(id string, label string, values []string, editable bool) appviewmodel.SettingsPanelField {
	clean := commandNonEmpty(values)
	field := textSettingsField(id, label, strings.Join(clean, ", "), editable)
	field.Kind = "path_list"
	field.Detail = strconv.Itoa(len(clean)) + " roots"
	return field
}

func currentModelPanelValue(status appviewmodel.ModelStatus) string {
	if status.Current == nil {
		return ""
	}
	return firstNonEmpty(status.Current.Detail, status.Current.Model, status.Current.ID)
}

func resourceDiagnosticSummary(status appviewmodel.ResourceStatus) string {
	parts := []string{
		"info=" + strconv.Itoa(status.InfoCount),
		"warnings=" + strconv.Itoa(status.WarningCount),
		"errors=" + strconv.Itoa(status.ErrorCount),
	}
	return strings.Join(parts, " ")
}
