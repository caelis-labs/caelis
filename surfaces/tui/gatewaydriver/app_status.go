package gatewaydriver

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func (d *GatewayDriver) statusFromAppView(ctx context.Context, includeDiagnostics bool) (StatusSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.stack == nil {
		return StatusSnapshot{}, fmt.Errorf("surfaces/tui/gatewaydriver: stack is unavailable")
	}
	activeSession, hasSession := d.currentSession()
	ref := coresession.Ref{}
	if hasSession {
		ref = activeSession.Ref
	}
	view, ok, err := d.stack.AppStatusView(ctx, ref, includeDiagnostics)
	if err != nil {
		return StatusSnapshot{}, err
	}
	if !ok {
		return StatusSnapshot{}, fmt.Errorf("surfaces/tui/gatewaydriver: app status view dependency is unavailable")
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
		PermissionGrantCount:     view.Permissions.GrantCount,
		PermissionReadRootCount:  view.Permissions.ReadRootCount,
		PermissionWriteRootCount: view.Permissions.WriteRootCount,
		PromptTokens:             inputTokens,
		TotalTokens:              inputTokens,
		ContextWindowTokens:      view.Usage.ContextBudget.ContextWindowTokens,
	}
	if view.Sandbox != nil {
		applyAppSandboxStatus(&status, *view.Sandbox)
	}
	if hasSession && activeSession.Controller.Kind == coresession.ControllerACP {
		acpStatus, activeACP, err := d.activeACPControllerStatus(ctx)
		if err != nil {
			return StatusSnapshot{}, err
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
	if kind, ok := activeTurnKindForSession(active, activeSession.Ref); ok {
		status.ActiveTurnKind = string(kind)
	}
	return status, nil
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

func applyAppSandboxStatus(status *StatusSnapshot, sandboxStatus appviewmodel.SandboxStatus) {
	if status == nil {
		return
	}
	status.SandboxRequestedBackend = firstNonEmpty(strings.TrimSpace(sandboxStatus.RequestedBackend), status.SandboxRequestedBackend)
	status.SandboxResolvedBackend = firstNonEmpty(strings.TrimSpace(sandboxStatus.ResolvedBackend), status.SandboxResolvedBackend)
	status.SandboxType = firstNonEmpty(status.SandboxResolvedBackend, status.SandboxRequestedBackend, status.SandboxType)
	status.Route = firstNonEmpty(strings.TrimSpace(sandboxStatus.Route), status.Route)
	status.FallbackReason = firstNonEmpty(strings.TrimSpace(sandboxStatus.FallbackReason), status.FallbackReason)
	status.SandboxInstallHint = firstNonEmpty(strings.TrimSpace(sandboxStatus.FallbackInstallHint), status.SandboxInstallHint)
	status.SandboxSetup = sandbox.CloneSetupStatus(sandboxStatus.Setup)
	status.SandboxSetupRequired = sandboxStatus.SetupRequired
	status.SandboxSetupError = strings.TrimSpace(sandboxStatus.SetupError)
	status.SandboxSetupMarkerCurrent = sandboxStatus.SetupMarkerCurrent
	status.SandboxSetupMarkerReason = strings.TrimSpace(sandboxStatus.SetupMarkerReason)
	status.SecuritySummary = firstNonEmpty(sandboxSecuritySummaryFromView(sandboxStatus), status.SecuritySummary)
	status.HostExecution = strings.EqualFold(strings.TrimSpace(status.Route), "host")
	global, hasGlobal := sandboxSetupCheckByScope(status.SandboxSetup, sandbox.SetupGlobal)
	workspace, hasWorkspace := sandboxSetupCheckByScope(status.SandboxSetup, sandbox.SetupWorkspace)
	status.SandboxGlobalSetupCurrent = hasGlobal && global.Current
	status.SandboxGlobalSetupRequired = hasGlobal && global.Required
	status.SandboxGlobalSetupReason = setupReason(global, hasGlobal)
	status.SandboxWorkspaceSetupCurrent = hasWorkspace && workspace.Current
	status.SandboxWorkspaceSetupRequired = hasWorkspace && workspace.Required
	status.SandboxWorkspaceSetupReason = setupReason(workspace, hasWorkspace)
	status.SandboxWorkspaceSetupRoot = setupRoot(workspace, hasWorkspace)
	status.SandboxWorkspaceSetupWriteRoots = setupCount(workspace, hasWorkspace, "write_roots")
	status.SandboxWorkspaceSetupPolicyHash = setupDetail(workspace, hasWorkspace, "policy_hash")
	status.SandboxWorkspaceSetupUpdatedAt = workspace.UpdatedAt
}

func sandboxSecuritySummaryFromView(status appviewmodel.SandboxStatus) string {
	parts := make([]string, 0, 4)
	if isolation := strings.TrimSpace(status.Isolation); isolation != "" {
		parts = append(parts, "isolation="+isolation)
	}
	if permission := strings.TrimSpace(status.DefaultPermission); permission != "" {
		parts = append(parts, "permission="+permission)
	}
	if network := strings.TrimSpace(status.Network); network != "" {
		parts = append(parts, "network="+network)
	}
	if status.PathPolicy {
		parts = append(parts, "path_policy=on")
	}
	return strings.Join(parts, " ")
}
