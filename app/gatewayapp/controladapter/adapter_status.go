package controladapter

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func (d *Adapter) LightweightStatus(ctx context.Context) (StatusSnapshot, error) {
	return d.status(ctx, false)
}

func (d *Adapter) Status(ctx context.Context) (StatusSnapshot, error) {
	return d.status(ctx, true)
}

func (d *Adapter) status(ctx context.Context, includeDiagnostics bool) (StatusSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	modelText, sessionMode, sandboxType := d.defaultDisplays()
	reasoningEffort := ""
	if d.stack != nil && d.stack.Model.DefaultAliasFn != nil {
		if alias := strings.TrimSpace(d.stack.Model.DefaultAliasFn()); alias != "" {
			modelText = alias
		}
	}
	sandboxStatus := SandboxStatus{}
	if includeDiagnostics && d.stack != nil && d.stack.Sandbox.StatusFn != nil {
		sandboxStatus = d.stack.Sandbox.StatusFn()
	}
	activeSession, ok := d.currentSession()
	if ok && d.stack != nil && d.stack.Status.RuntimeStateFn != nil {
		if state, err := d.stack.Status.RuntimeStateFn(context.Background(), activeSession.SessionRef); err == nil {
			if strings.TrimSpace(state.ModelAlias) != "" {
				modelText = strings.TrimSpace(state.ModelAlias)
			}
			if strings.TrimSpace(state.ReasoningEffort) != "" {
				reasoningEffort = strings.TrimSpace(state.ReasoningEffort)
			}
			if strings.TrimSpace(state.SessionMode) != "" {
				sessionMode = strings.TrimSpace(state.SessionMode)
			}
		}
	}
	acpStatus, activeACP, acpStatusErr := d.activeACPControllerStatus(ctx)
	if acpStatusErr != nil {
		return StatusSnapshot{}, acpStatusErr
	}
	acpModeID := ""
	acpModeLabel := ""
	acpModelText := ""
	if activeACP {
		acpModelText = acpControllerModelText(acpStatus, activeSession)
		modelText = acpModelText
		reasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
		acpModeID = strings.TrimSpace(acpStatus.Mode)
		acpModeLabel = acpControllerModeDisplay(acpStatus)
	}
	sandboxType = firstNonEmpty(sandboxStatus.ResolvedBackend, sandboxStatus.RequestedBackend, sandboxType)
	route := sandboxStatus.Route
	securitySummary := sandboxStatus.SecuritySummary
	d.mu.Lock()
	sessionID := ""
	if ok {
		sessionID = activeSession.SessionID
	}
	liveModelText := d.modelText
	liveSessionMode := d.sessionMode
	liveSandboxType := d.sandboxType
	bindingKey := d.bindingKey
	d.mu.Unlock()
	rawModelText := firstNonEmpty(modelText, liveModelText)
	workspaceCWD := ""
	if ok {
		workspaceCWD = strings.TrimSpace(activeSession.CWD)
	}
	if workspaceCWD == "" && d.stack != nil {
		workspaceCWD = strings.TrimSpace(d.stack.Session.Workspace.CWD)
	}

	status := StatusSnapshot{
		Session: control.StatusSession{
			ID:          sessionID,
			Workspace:   workspaceStatusDisplay(ctx, workspaceCWD),
			ModeLabel:   firstNonEmpty(sessionMode, liveSessionMode),
			SessionMode: firstNonEmpty(sessionMode, liveSessionMode),
			Surface:     bindingKey,
		},
		ModelStatus: control.StatusModel{
			Display:         formatReasoningModelDisplay(rawModelText, reasoningEffort),
			ReasoningEffort: reasoningEffort,
		},
		SandboxStatus: control.StatusSandbox{
			Type:                     firstNonEmpty(sandboxType, liveSandboxType),
			RequestedBackend:         firstNonEmpty(sandboxStatus.RequestedBackend, "auto"),
			ResolvedBackend:          firstNonEmpty(sandboxStatus.ResolvedBackend, sandboxStatus.RequestedBackend, liveSandboxType),
			Route:                    route,
			FallbackReason:           sandboxStatus.FallbackReason,
			InstallHint:              sandboxStatus.InstallHint,
			Setup:                    sandboxSetupStatusFromPort(sandboxStatus.Setup),
			SetupRequired:            sandboxStatus.SetupRequired,
			SetupError:               sandboxStatus.SetupError,
			SetupMarkerCurrent:       sandboxStatus.SetupMarkerCurrent,
			SetupMarkerReason:        sandboxStatus.SetupMarkerReason,
			GlobalSetupCurrent:       sandboxStatus.GlobalSetupCurrent,
			GlobalSetupRequired:      sandboxStatus.GlobalSetupRequired,
			GlobalSetupReason:        sandboxStatus.GlobalSetupReason,
			WorkspaceSetupCurrent:    sandboxStatus.WorkspaceSetupCurrent,
			WorkspaceSetupRequired:   sandboxStatus.WorkspaceSetupRequired,
			WorkspaceSetupReason:     sandboxStatus.WorkspaceSetupReason,
			WorkspaceSetupRoot:       sandboxStatus.WorkspaceSetupRoot,
			WorkspaceSetupWriteRoots: sandboxStatus.WorkspaceSetupWriteRoots,
			WorkspaceSetupPolicyHash: sandboxStatus.WorkspaceSetupPolicyHash,
			WorkspaceSetupUpdatedAt:  sandboxStatus.WorkspaceSetupUpdatedAt,
			SecuritySummary:          securitySummary,
			HostExecution:            strings.EqualFold(strings.TrimSpace(route), "host"),
		},
	}
	if d.stack != nil {
		req := DoctorRequest{}
		if ok {
			req.SessionRef = activeSession.SessionRef
		}
		if includeDiagnostics && d.stack.Status.DoctorFn != nil {
			if report, err := d.stack.Status.DoctorFn(context.Background(), req); err == nil {
				applyDoctorStatus(&status, report)
				if alias := strings.TrimSpace(report.ActiveModelAlias); alias != "" {
					rawModelText = alias
					status.ModelStatus.Display = formatReasoningModelDisplay(alias, status.ModelStatus.ReasoningEffort)
				}
			}
		}
		if status.ModelStatus.ReasoningEffort == "" {
			if activeACP {
				status.ModelStatus.ReasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
				status.ModelStatus.Display = formatReasoningModelDisplay(firstNonEmpty(strings.TrimSpace(acpStatus.Model), rawModelText), status.ModelStatus.ReasoningEffort)
			} else if d.stack.Model.ConfigFn != nil {
				if cfg, ok := d.stack.Model.ConfigFn(rawModelText); ok {
					status.ModelStatus.ReasoningEffort = firstNonEmpty(cfg.ReasoningEffort, cfg.DefaultReasoningEffort)
					status.ModelStatus.Display = formatReasoningModelDisplay(rawModelText, status.ModelStatus.ReasoningEffort)
				}
			}
		}
		if ok && !activeACP && d.stack.Model.SessionUsageSnapshotFn != nil {
			if usage, err := d.stack.Model.SessionUsageSnapshotFn(context.Background(), activeSession.SessionRef, rawModelText); err == nil {
				status.Usage.TotalTokens = usage.TotalTokens
				status.Usage.ContextWindowTokens = usage.ContextWindowTokens
			}
		}
		if ok {
			if usage, err := d.sessionTokenUsageBreakdown(context.Background(), activeSession.SessionRef); err == nil {
				status.Usage.SessionUsageTotal = usageSnapshotFromKernel(usage.Total)
				status.Usage.SessionUsageMain = usageSnapshotFromKernel(usage.Main)
				status.Usage.SessionUsageSubagents = usageSnapshotFromKernel(usage.Subagents)
				status.Usage.SessionUsageAutoReview = usageSnapshotFromKernel(usage.AutoReview)
				status.Usage.SessionUsageByModel = modelUsageSnapshotsFromBreakdown(usage)
				status.Usage.SessionInputTokens = usage.Total.PromptTokens
				status.Usage.SessionCachedInputTokens = usage.Total.CachedInputTokens
				status.Usage.SessionOutputTokens = usage.Total.CompletionTokens
				status.Usage.SessionReasoningTokens = usage.Total.ReasoningTokens
				status.Usage.SessionTotalTokens = usage.Total.TotalTokens
			}
		}
	}
	if activeACP {
		rawModelText = firstNonEmpty(strings.TrimSpace(acpStatus.Model), acpModelText, rawModelText)
		status.ModelStatus.Display = formatReasoningModelDisplay(rawModelText, strings.TrimSpace(acpStatus.ReasoningEffort))
		status.ModelStatus.ReasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
		if acpModeID != "" {
			status.Session.SessionMode = acpModeID
		}
		if acpModeLabel != "" || acpModeID != "" {
			status.Session.ModeLabel = firstNonEmpty(acpModeLabel, acpModeID)
		}
		status.ModelStatus.Provider = "acp"
		status.ModelStatus.Name = strings.TrimSpace(acpStatus.Model)
		status.ModelStatus.MissingAPIKey = false
		status.SandboxStatus.FullAccessMode = false
		status.Usage.PromptTokens = 0
		status.Usage.CompletionTokens = 0
		status.Usage.TotalTokens = 0
		status.Usage.ContextWindowTokens = 0
	}
	if status.Usage.TotalTokens > 0 {
		status.Usage.PromptTokens = status.Usage.TotalTokens
	}
	if status.SandboxStatus.FullAccessMode {
		status.SandboxStatus.HostExecution = true
		status.SandboxStatus.Route = firstNonEmpty(strings.TrimSpace(status.SandboxStatus.Route), "host")
		if strings.TrimSpace(status.SandboxStatus.Route) != "host" {
			status.SandboxStatus.Route = "host"
		}
	}
	if gw, err := d.gatewayTurns(); err == nil && gw != nil {
		active := gw.ActiveTurns()
		status.Runtime.ActiveJobs = len(active)
		status.Runtime.Running = len(active) > 0
		if kind, ok := activeTurnKindForSession(active, activeSession.SessionRef); ok {
			status.Runtime.ActiveTurnKind = kind
		}
	}
	return status, nil
}

func applyDoctorStatus(status *StatusSnapshot, report DoctorReport) {
	if status == nil {
		return
	}
	status.Session.StoreDir = strings.TrimSpace(report.StoreDir)
	status.ModelStatus.Provider = strings.TrimSpace(report.ActiveProvider)
	status.ModelStatus.Name = strings.TrimSpace(report.ActiveModel)
	status.ModelStatus.MissingAPIKey = report.MissingAPIKey
	status.SandboxStatus.HostExecution = report.HostExecution
	status.SandboxStatus.FullAccessMode = report.FullAccessMode
	status.SandboxStatus.RequestedBackend = firstNonEmpty(strings.TrimSpace(report.SandboxRequestedBackend), status.SandboxStatus.RequestedBackend)
	status.SandboxStatus.ResolvedBackend = firstNonEmpty(strings.TrimSpace(report.SandboxResolvedBackend), status.SandboxStatus.ResolvedBackend)
	status.SandboxStatus.Route = firstNonEmpty(strings.TrimSpace(report.SandboxRoute), status.SandboxStatus.Route)
	status.SandboxStatus.FallbackReason = firstNonEmpty(strings.TrimSpace(report.SandboxFallbackReason), status.SandboxStatus.FallbackReason)
	status.SandboxStatus.InstallHint = firstNonEmpty(strings.TrimSpace(report.SandboxInstallHint), status.SandboxStatus.InstallHint)
	if report.SandboxSetup != nil {
		status.SandboxStatus.Setup = sandboxSetupStatusFromPort(*report.SandboxSetup)
	}
	status.SandboxStatus.SetupRequired = report.SandboxSetupRequired || status.SandboxStatus.SetupRequired
	status.SandboxStatus.SetupError = firstNonEmpty(strings.TrimSpace(report.SandboxSetupError), status.SandboxStatus.SetupError)
	status.SandboxStatus.SetupMarkerCurrent = report.SandboxSetupMarkerCurrent || status.SandboxStatus.SetupMarkerCurrent
	status.SandboxStatus.SetupMarkerReason = firstNonEmpty(strings.TrimSpace(report.SandboxSetupMarkerReason), status.SandboxStatus.SetupMarkerReason)
	status.SandboxStatus.GlobalSetupCurrent = report.SandboxGlobalSetupCurrent || status.SandboxStatus.GlobalSetupCurrent
	status.SandboxStatus.GlobalSetupRequired = report.SandboxGlobalSetupRequired || status.SandboxStatus.GlobalSetupRequired
	status.SandboxStatus.GlobalSetupReason = firstNonEmpty(strings.TrimSpace(report.SandboxGlobalSetupReason), status.SandboxStatus.GlobalSetupReason)
	status.SandboxStatus.WorkspaceSetupCurrent = report.SandboxWorkspaceSetupCurrent || status.SandboxStatus.WorkspaceSetupCurrent
	status.SandboxStatus.WorkspaceSetupRequired = report.SandboxWorkspaceSetupRequired || status.SandboxStatus.WorkspaceSetupRequired
	status.SandboxStatus.WorkspaceSetupReason = firstNonEmpty(strings.TrimSpace(report.SandboxWorkspaceSetupReason), status.SandboxStatus.WorkspaceSetupReason)
	status.SandboxStatus.WorkspaceSetupRoot = firstNonEmpty(strings.TrimSpace(report.SandboxWorkspaceSetupRoot), status.SandboxStatus.WorkspaceSetupRoot)
	if report.SandboxWorkspaceSetupWriteRoots > 0 {
		status.SandboxStatus.WorkspaceSetupWriteRoots = report.SandboxWorkspaceSetupWriteRoots
	}
	status.SandboxStatus.WorkspaceSetupPolicyHash = firstNonEmpty(strings.TrimSpace(report.SandboxWorkspaceSetupPolicyHash), status.SandboxStatus.WorkspaceSetupPolicyHash)
	if !report.SandboxWorkspaceSetupUpdatedAt.IsZero() {
		status.SandboxStatus.WorkspaceSetupUpdatedAt = report.SandboxWorkspaceSetupUpdatedAt
	}
	status.SandboxStatus.SecuritySummary = firstNonEmpty(strings.TrimSpace(report.SandboxSecuritySummary), status.SandboxStatus.SecuritySummary)
	if mode := strings.TrimSpace(report.SessionMode); mode != "" {
		status.Session.ModeLabel = mode
		status.Session.SessionMode = mode
	}
	if id := strings.TrimSpace(report.SessionID); id != "" {
		status.Session.ID = id
	}
}
