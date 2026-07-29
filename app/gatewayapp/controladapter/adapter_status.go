package controladapter

import (
	"context"
	"strings"

	controlstatus "github.com/caelis-labs/caelis/control/status"
)

func (d *Adapter) LightweightStatus(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	return d.status(ctx, false)
}

func (d *Adapter) Status(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	return d.status(ctx, true)
}

func (d *Adapter) status(ctx context.Context, includeDiagnostics bool) (controlstatus.StatusSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	modelText, sessionMode, sandboxType := d.defaultDisplays()
	reasoningEffort := ""
	defaultModelText := ""
	if d.stack != nil && d.stack.Model.DefaultAliasFn != nil {
		if alias := strings.TrimSpace(d.stack.Model.DefaultAliasFn()); alias != "" {
			modelText = alias
			defaultModelText = alias
		}
	}
	if d.stack != nil && d.stack.Model.DefaultEffortFn != nil {
		reasoningEffort = strings.TrimSpace(d.stack.Model.DefaultEffortFn())
	}
	sandboxStatus := SandboxStatus{}
	if includeDiagnostics && d.stack != nil && d.stack.Sandbox.StatusFn != nil {
		sandboxStatus = d.stack.Sandbox.StatusFn()
	}
	activeSession, ok := d.currentSession()
	if ok && d.stack != nil && d.stack.Status.RuntimeStateFn != nil {
		if state, err := d.stack.Status.RuntimeStateFn(ctx, activeSession.SessionRef); err == nil {
			if strings.TrimSpace(state.ModelAlias) != "" {
				modelText = strings.TrimSpace(state.ModelAlias)
				if defaultModelText != "" && !strings.EqualFold(modelText, defaultModelText) {
					reasoningEffort = ""
				}
			}
			if strings.TrimSpace(state.ReasoningEffort) != "" {
				reasoningEffort = strings.TrimSpace(state.ReasoningEffort)
			}
			if strings.TrimSpace(state.SessionMode) != "" {
				sessionMode = strings.TrimSpace(state.SessionMode)
			}
		} else if ctx.Err() != nil {
			return controlstatus.StatusSnapshot{}, ctx.Err()
		}
	}
	acpStatus, activeACP, acpStatusErr := d.activeACPControllerStatus(ctx)
	if acpStatusErr != nil {
		return controlstatus.StatusSnapshot{}, acpStatusErr
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

	status := controlstatus.StatusSnapshot{
		Session: controlstatus.StatusSession{
			ID:          sessionID,
			Workspace:   workspaceStatusDisplay(ctx, workspaceCWD),
			ModeLabel:   firstNonEmpty(sessionMode, liveSessionMode),
			SessionMode: firstNonEmpty(sessionMode, liveSessionMode),
			Surface:     bindingKey,
		},
		ModelStatus: controlstatus.StatusModel{
			Display:         formatReasoningModelDisplay(rawModelText, reasoningEffort),
			ReasoningEffort: reasoningEffort,
		},
		SandboxStatus: controlstatus.StatusSandbox{
			Type:             firstNonEmpty(sandboxType, liveSandboxType),
			RequestedBackend: firstNonEmpty(sandboxStatus.RequestedBackend, "auto"),
			ResolvedBackend:  firstNonEmpty(sandboxStatus.ResolvedBackend, sandboxStatus.RequestedBackend, liveSandboxType),
			Route:            route,
			FallbackReason:   sandboxStatus.FallbackReason,
			InstallHint:      sandboxStatus.InstallHint,
			Setup:            sandboxSetupStatusFromPort(sandboxStatus.Setup),
			SecuritySummary:  securitySummary,
			HostExecution:    strings.EqualFold(strings.TrimSpace(route), "host"),
		},
	}
	if d.stack != nil {
		req := DoctorRequest{}
		if ok {
			req.SessionRef = activeSession.SessionRef
		}
		if includeDiagnostics && d.stack.Status.DoctorFn != nil {
			if report, err := d.stack.Status.DoctorFn(ctx, req); err == nil {
				applyDoctorStatus(&status, report)
				if alias := strings.TrimSpace(report.ActiveModelAlias); alias != "" {
					rawModelText = alias
					status.ModelStatus.Display = formatReasoningModelDisplay(alias, status.ModelStatus.ReasoningEffort)
				}
			} else if ctx.Err() != nil {
				return controlstatus.StatusSnapshot{}, ctx.Err()
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
		if includeDiagnostics && ok && !activeACP && d.stack.Model.SessionUsageSnapshotFn != nil {
			if usage, err := d.stack.Model.SessionUsageSnapshotFn(ctx, activeSession.SessionRef, rawModelText); err == nil {
				status.Usage.TotalTokens = usage.TotalTokens
				status.Usage.ContextWindowTokens = usage.ContextWindowTokens
			} else if ctx.Err() != nil {
				return controlstatus.StatusSnapshot{}, ctx.Err()
			}
		}
		if includeDiagnostics && ok {
			if usage, err := d.sessionTokenUsageBreakdown(ctx, activeSession.SessionRef); err == nil {
				status.Usage.SessionUsageTotal = usageSnapshotFromKernel(usage.Total)
				status.Usage.SessionUsageByModel = modelUsageSnapshotsFromBreakdown(usage)
			} else if ctx.Err() != nil {
				return controlstatus.StatusSnapshot{}, ctx.Err()
			}
		}
		if includeDiagnostics && !activeACP && d.stack.Model.ProviderUsageFn != nil {
			if usage, found, err := d.stack.Model.ProviderUsageFn(ctx, rawModelText); err == nil && found {
				status.RateLimits = statusRateLimitsFromProviderUsage(usage)
			} else if ctx.Err() != nil {
				return controlstatus.StatusSnapshot{}, ctx.Err()
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
	if err := ctx.Err(); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	return status, nil
}

func applyDoctorStatus(status *controlstatus.StatusSnapshot, report DoctorReport) {
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
	status.SandboxStatus.SecuritySummary = firstNonEmpty(strings.TrimSpace(report.SandboxSecuritySummary), status.SandboxStatus.SecuritySummary)
	if mode := strings.TrimSpace(report.SessionMode); mode != "" {
		status.Session.ModeLabel = mode
		status.Session.SessionMode = mode
	}
	if id := strings.TrimSpace(report.SessionID); id != "" {
		status.Session.ID = id
	}
}
