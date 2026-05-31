package gatewaydriver

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (d *GatewayDriver) statusFromAppView(ctx context.Context) (StatusSnapshot, bool, error) {
	if d == nil || d.stack == nil {
		return StatusSnapshot{}, false, nil
	}
	activeSession, hasSession := d.currentSession()
	ref := coresession.Ref{}
	if hasSession {
		ref = coreRefFromPort(activeSession.SessionRef)
	}
	view, ok, err := d.stack.AppStatusView(ctx, ref)
	if !ok || err != nil {
		return StatusSnapshot{}, ok, err
	}
	d.mu.Lock()
	bindingKey := d.bindingKey
	liveModelText := d.modelText
	liveSessionMode := d.sessionMode
	liveSandboxType := d.sandboxType
	defaultModelText := d.defaultModelText
	d.mu.Unlock()

	workspace := firstNonEmpty(view.Runtime.WorkspaceCWD, d.stack.Workspace.CWD)
	sessionID := ""
	if view.Session != nil {
		sessionID = strings.TrimSpace(view.Session.Ref.SessionID)
		workspace = firstNonEmpty(view.Session.Workspace.CWD, workspace)
	} else if hasSession {
		sessionID = strings.TrimSpace(activeSession.SessionID)
	}
	modelText, provider, modelName := appStatusModelText(view.Model)
	modelText = firstNonEmpty(modelText, liveModelText, defaultModelText, view.Runtime.DefaultModel, "not configured")
	reasoning := strings.TrimSpace(view.Model.ReasoningEffort)
	modeID := firstNonEmpty(view.Mode.Current.ID, liveSessionMode, "auto-review")
	modeLabel := firstNonEmpty(view.Mode.Current.Name, modeID)
	sandboxType := firstNonEmpty(view.Runtime.SandboxBackend, liveSandboxType, "auto")
	inputTokens := view.Usage.ContextBudget.EstimatedInputTokens
	if inputTokens < view.Usage.Total.TotalTokens {
		inputTokens = view.Usage.Total.TotalTokens
	}
	route := ""
	if strings.EqualFold(strings.TrimSpace(sandboxType), "host") {
		route = "host"
	}
	status := StatusSnapshot{
		SessionID:                sessionID,
		Workspace:                workspaceStatusDisplay(ctx, workspace),
		Model:                    formatReasoningModelDisplay(modelText, reasoning),
		ReasoningEffort:          reasoning,
		Provider:                 provider,
		ModelName:                modelName,
		MissingAPIKey:            view.Model.MissingAPIKey,
		ModeLabel:                modeLabel,
		SessionMode:              modeID,
		StoreDir:                 firstNonEmpty(view.Runtime.StoreURI, view.Runtime.StoreBackend),
		SandboxType:              sandboxType,
		SandboxRequestedBackend:  firstNonEmpty(sandboxType, "auto"),
		SandboxResolvedBackend:   sandboxType,
		Route:                    route,
		HostExecution:            route == "host",
		Surface:                  bindingKey,
		SessionUsageTotal:        view.Usage.Total,
		SessionUsageMain:         view.Usage.Main,
		SessionUsageSubagents:    view.Usage.Subagents,
		SessionUsageAutoReview:   view.Usage.AutoReview,
		SessionUsageCompaction:   view.Usage.Compaction,
		SessionInputTokens:       view.Usage.Total.InputTokens,
		SessionCachedInputTokens: view.Usage.Total.CachedInputTokens,
		SessionOutputTokens:      view.Usage.Total.OutputTokens,
		SessionReasoningTokens:   view.Usage.Total.ReasoningTokens,
		SessionTotalTokens:       view.Usage.Total.TotalTokens,
		PromptTokens:             inputTokens,
		TotalTokens:              inputTokens,
		ContextWindowTokens:      view.Usage.ContextBudget.ContextWindowTokens,
	}
	if sandboxStatus := d.stack.SandboxStatus(); sandboxStatus.RequestedBackend != "" || sandboxStatus.ResolvedBackend != "" || sandboxStatus.Route != "" {
		status.SandboxRequestedBackend = firstNonEmpty(sandboxStatus.RequestedBackend, status.SandboxRequestedBackend)
		status.SandboxResolvedBackend = firstNonEmpty(sandboxStatus.ResolvedBackend, status.SandboxResolvedBackend)
		status.Route = firstNonEmpty(sandboxStatus.Route, status.Route)
		status.FallbackReason = firstNonEmpty(strings.TrimSpace(sandboxStatus.FallbackReason), status.FallbackReason)
		status.SandboxInstallHint = firstNonEmpty(strings.TrimSpace(sandboxStatus.InstallHint), status.SandboxInstallHint)
		status.SandboxSetup = sandbox.CloneSetupStatus(sandboxStatus.Setup)
		status.SandboxSetupRequired = sandboxStatus.SetupRequired
		status.SandboxSetupError = strings.TrimSpace(sandboxStatus.SetupError)
		status.SandboxSetupMarkerCurrent = sandboxStatus.SetupMarkerCurrent
		status.SandboxSetupMarkerReason = strings.TrimSpace(sandboxStatus.SetupMarkerReason)
		status.SandboxGlobalSetupCurrent = sandboxStatus.GlobalSetupCurrent
		status.SandboxGlobalSetupRequired = sandboxStatus.GlobalSetupRequired
		status.SandboxGlobalSetupReason = strings.TrimSpace(sandboxStatus.GlobalSetupReason)
		status.SandboxWorkspaceSetupCurrent = sandboxStatus.WorkspaceSetupCurrent
		status.SandboxWorkspaceSetupRequired = sandboxStatus.WorkspaceSetupRequired
		status.SandboxWorkspaceSetupReason = strings.TrimSpace(sandboxStatus.WorkspaceSetupReason)
		status.SandboxWorkspaceSetupRoot = strings.TrimSpace(sandboxStatus.WorkspaceSetupRoot)
		status.SandboxWorkspaceSetupWriteRoots = sandboxStatus.WorkspaceSetupWriteRoots
		status.SandboxWorkspaceSetupPolicyHash = strings.TrimSpace(sandboxStatus.WorkspaceSetupPolicyHash)
		status.SandboxWorkspaceSetupUpdatedAt = sandboxStatus.WorkspaceSetupUpdatedAt
		status.SecuritySummary = firstNonEmpty(strings.TrimSpace(sandboxStatus.SecuritySummary), status.SecuritySummary)
		status.HostExecution = strings.EqualFold(strings.TrimSpace(status.Route), "host")
	}
	if hasSession && activeSession.Controller.Kind == session.ControllerKindACP {
		acpStatus, activeACP, err := d.activeACPControllerStatus(ctx)
		if err != nil {
			return StatusSnapshot{}, false, err
		}
		if activeACP {
			acpModelText := acpControllerModelText(acpStatus, activeSession)
			acpReasoning := strings.TrimSpace(acpStatus.ReasoningEffort)
			status.Model = formatReasoningModelDisplay(acpModelText, acpReasoning)
			status.ReasoningEffort = acpReasoning
			if mode := strings.TrimSpace(acpStatus.Mode); mode != "" {
				status.SessionMode = mode
			}
			if label := acpControllerModeDisplay(acpStatus); label != "" {
				status.ModeLabel = label
			} else if mode := strings.TrimSpace(acpStatus.Mode); mode != "" {
				status.ModeLabel = mode
			}
			status.Provider = "acp"
			status.ModelName = strings.TrimSpace(acpStatus.Model)
			status.MissingAPIKey = false
			status.FullAccessMode = false
			status.PromptTokens = 0
			status.CompletionTokens = 0
			status.TotalTokens = 0
			status.ContextWindowTokens = 0
		}
	}
	active := d.stack.ActiveTurns()
	status.ActiveJobs = len(active)
	status.Running = len(active) > 0
	if kind, ok := activeTurnKindForSession(active, coreRefFromPort(activeSession.SessionRef)); ok {
		status.ActiveTurnKind = string(kind)
	}
	return status, true, nil
}

func appStatusModelText(status appviewmodel.ModelStatus) (string, string, string) {
	choice := status.Current
	if choice == nil {
		for i := range status.Choices {
			if status.Choices[i].Default {
				choice = &status.Choices[i]
				break
			}
		}
	}
	if choice == nil && len(status.Choices) > 0 {
		choice = &status.Choices[0]
	}
	if choice == nil {
		return "", "", ""
	}
	provider := strings.TrimSpace(choice.Provider)
	modelName := strings.TrimSpace(choice.Model)
	modelText := firstNonEmpty(choice.Alias, choice.ID)
	if modelText == "" && provider != "" && modelName != "" {
		modelText = provider + "/" + modelName
	}
	return modelText, provider, modelName
}
