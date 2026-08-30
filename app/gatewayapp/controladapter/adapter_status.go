package controladapter

import (
	"context"
	"strings"

	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/control/workspacetrust"
)

func (d *assembler) LightweightStatus(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	return d.status(ctx, false)
}

func (d *assembler) Status(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	return d.status(ctx, true)
}

func (d *assembler) status(ctx context.Context, includeDiagnostics bool) (controlstatus.StatusSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	configurationRevision := uint64(0)
	if d.deps != nil && d.deps.Status.ConfigurationRevisionFn != nil {
		var err error
		configurationRevision, err = d.deps.Status.ConfigurationRevisionFn(ctx)
		if err != nil {
			return controlstatus.StatusSnapshot{}, err
		}
	}
	modelText, sessionMode, sandboxType := d.defaultDisplays()
	reasoningEffort := ""
	effectiveModelText := ""
	if d.deps != nil && d.deps.Model.EffectiveAliasFn != nil {
		if alias := strings.TrimSpace(d.deps.Model.EffectiveAliasFn()); alias != "" {
			modelText = alias
			effectiveModelText = alias
		}
	}
	if d.deps != nil && d.deps.Model.EffectiveEffortFn != nil {
		reasoningEffort = strings.TrimSpace(d.deps.Model.EffectiveEffortFn())
	}
	sandboxStatus := SandboxStatusProjection{}
	if d.deps != nil && d.deps.Sandbox.StatusFn != nil {
		sandboxStatus = d.deps.Sandbox.StatusFn()
	}
	activeSession, ok := d.currentSession()
	if ok && d.deps != nil && d.deps.Status.RuntimeStateFn != nil {
		if state, err := d.deps.Status.RuntimeStateFn(ctx, activeSession.SessionRef); err == nil {
			if strings.TrimSpace(state.ModelAlias) != "" {
				modelText = strings.TrimSpace(state.ModelAlias)
				if effectiveModelText != "" && !strings.EqualFold(modelText, effectiveModelText) {
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
	if sandboxStatus.FullAccessMode {
		sessionMode = processOwnedFullAccessSessionMode
	}
	processOwnedSessionMode := isProcessOwnedSessionMode(sessionMode)
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
	if workspaceCWD == "" && d.deps != nil {
		workspaceCWD = strings.TrimSpace(d.deps.Session.Workspace.CWD)
	}
	workspaceTrust := workspacetrust.Unknown
	if d.deps != nil && d.deps.Status.WorkspaceTrustFn != nil {
		var err error
		workspaceTrust, err = d.deps.Status.WorkspaceTrustFn(ctx, workspaceCWD)
		if err != nil {
			return controlstatus.StatusSnapshot{}, err
		}
	}

	status := controlstatus.StatusSnapshot{
		Configuration: controlstatus.StatusConfiguration{Revision: configurationRevision, WorkspaceTrust: workspaceTrust},
		Usage:         controlstatus.StatusUsage{ContextUsageReplace: activeACP},
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
			FullAccessMode:   sandboxStatus.FullAccessMode,
		},
	}
	if activeACP {
		status.Usage.ContextUsageControllerEpoch = strings.TrimSpace(activeSession.Controller.EpochID)
	}
	if d.deps != nil {
		// The configured context window is static model metadata, not a usage
		// diagnostic. Keep it available to lightweight status consumers so a
		// later total-only provider usage event can still render the ratio.
		// Surfaces hide the compact label until observed token usage arrives.
		if !activeACP && d.deps.Model.ConfigFn != nil {
			if cfg, found := d.deps.Model.ConfigFn(rawModelText); found && cfg.ContextWindowTokens > 0 {
				status.Usage.ContextWindowTokens = cfg.ContextWindowTokens
				status.Usage.ContextUsageAvailable = true
			}
		}
		if activeACP && ok {
			usage, found, err := d.latestACPControllerContextUsage(
				ctx,
				activeSession,
				firstNonEmpty(acpStatus.Agent, activeSession.Controller.AgentName, activeSession.Controller.ControllerID),
				firstNonEmpty(acpStatus.Model, activeSession.Controller.Placement.Model),
			)
			if err == nil && found {
				status.Usage.TotalTokens = usage.TotalTokens
				status.Usage.ContextWindowTokens = usage.ContextWindowTokens
				status.Usage.ContextUsageAvailable = true
			} else if ctx.Err() != nil {
				return controlstatus.StatusSnapshot{}, ctx.Err()
			}
		}
		req := DoctorRequest{}
		if ok {
			req.SessionRef = activeSession.SessionRef
		}
		if includeDiagnostics && d.deps.Status.DoctorFn != nil {
			if report, err := d.deps.Status.DoctorFn(ctx, req); err == nil {
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
			} else if d.deps.Model.ConfigFn != nil {
				if cfg, ok := d.deps.Model.ConfigFn(rawModelText); ok {
					status.ModelStatus.ReasoningEffort = firstNonEmpty(cfg.ReasoningEffort, cfg.DefaultReasoningEffort)
					status.ModelStatus.Display = formatReasoningModelDisplay(rawModelText, status.ModelStatus.ReasoningEffort)
				}
			}
		}
		if includeDiagnostics && ok && !activeACP && d.deps.Model.SessionUsageSnapshotFn != nil {
			if usage, err := d.deps.Model.SessionUsageSnapshotFn(ctx, activeSession.SessionRef, rawModelText); err == nil {
				status.Usage.TotalTokens = usage.TotalTokens
				status.Usage.ContextUsageAvailable = true
				if usage.ContextWindowTokens > 0 {
					status.Usage.ContextWindowTokens = usage.ContextWindowTokens
				}
			} else if ctx.Err() != nil {
				return controlstatus.StatusSnapshot{}, ctx.Err()
			}
		}
		if includeDiagnostics && ok {
			if usage, err := d.sessionTokenUsageBreakdown(ctx, activeSession.SessionRef); err == nil {
				status.Usage.SessionUsageTotal = usageSnapshotFromSession(usage.Total)
				status.Usage.SessionUsageByModel = modelUsageSnapshotsFromBreakdown(usage)
			} else if ctx.Err() != nil {
				return controlstatus.StatusSnapshot{}, ctx.Err()
			}
		}
		if includeDiagnostics && !activeACP && d.deps.Model.ProviderUsageFn != nil {
			if usage, found, err := d.deps.Model.ProviderUsageFn(ctx, rawModelText); err == nil && found {
				status.RateLimits = statusRateLimitsFromProviderUsage(usage)
			} else if ctx.Err() != nil {
				return controlstatus.StatusSnapshot{}, ctx.Err()
			}
		}
	}
	if activeACP {
		rawModelText = firstNonEmpty(strings.TrimSpace(acpStatus.Model), acpModelText, rawModelText)
		status.ModelStatus.Alias = rawModelText
		status.ModelStatus.Display = formatReasoningModelDisplay(rawModelText, strings.TrimSpace(acpStatus.ReasoningEffort))
		status.ModelStatus.ReasoningEffort = strings.TrimSpace(acpStatus.ReasoningEffort)
		if acpModeID != "" && !processOwnedSessionMode {
			status.Session.SessionMode = acpModeID
		}
		if !processOwnedSessionMode && (acpModeLabel != "" || acpModeID != "") {
			status.Session.ModeLabel = firstNonEmpty(acpModeLabel, acpModeID)
		}
		status.ModelStatus.Provider = "acp"
		status.ModelStatus.Name = rawModelText
		status.ModelStatus.MissingAPIKey = false
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

func applyDoctorStatus(status *controlstatus.StatusSnapshot, report DoctorStatusProjection) {
	if status == nil {
		return
	}
	status.Session.StoreDir = strings.TrimSpace(report.StoreDir)
	status.Session.PolicyProfile = strings.TrimSpace(report.PolicyProfile)
	status.ModelStatus.Alias = strings.TrimSpace(report.ActiveModelAlias)
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
	status.Runtime.ActiveSessions = append([]string(nil), report.ActiveTurnSessions...)
	status.Diagnostics = controlstatus.StatusDiagnostics{
		GoVersion:               strings.TrimSpace(report.GoVersion),
		GOOS:                    strings.TrimSpace(report.GOOS),
		GOARCH:                  strings.TrimSpace(report.GOARCH),
		ConfigPath:              strings.TrimSpace(report.ConfigPath),
		ConfigDirMode:           strings.TrimSpace(report.ConfigDirMode),
		ConfigFileMode:          strings.TrimSpace(report.ConfigFileMode),
		ConfigDirSecure:         report.ConfigDirSecure,
		ConfigFileSecure:        report.ConfigFileSecure,
		ConfigPermissionsSecure: report.ConfigPermissionsSecure,
		TokenSource:             strings.TrimSpace(report.TokenSource),
		PersistedPlaintextToken: report.PersistedPlaintextToken,
		Warnings:                append([]string(nil), report.Warnings...),
	}
	if mode := strings.TrimSpace(report.SessionMode); mode != "" {
		status.Session.ModeLabel = mode
		status.Session.SessionMode = mode
	}
	if id := strings.TrimSpace(report.SessionID); id != "" {
		status.Session.ID = id
	}
}
