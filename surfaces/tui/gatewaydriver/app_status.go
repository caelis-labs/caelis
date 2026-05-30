package gatewaydriver

import (
	"context"
	"strings"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (d *GatewayDriver) statusFromAppView(ctx context.Context) (StatusSnapshot, bool, error) {
	if d == nil || d.stack == nil {
		return StatusSnapshot{}, false, nil
	}
	activeSession, hasSession := d.currentSession()
	ref := session.SessionRef{}
	if hasSession {
		ref = activeSession.SessionRef
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
	route := ""
	if strings.EqualFold(strings.TrimSpace(sandboxType), "host") {
		route = "host"
	}
	status := StatusSnapshot{
		SessionID:               sessionID,
		Workspace:               workspaceStatusDisplay(ctx, workspace),
		Model:                   formatReasoningModelDisplay(modelText, reasoning),
		ReasoningEffort:         reasoning,
		Provider:                provider,
		ModelName:               modelName,
		ModeLabel:               modeLabel,
		SessionMode:             modeID,
		SandboxType:             sandboxType,
		SandboxRequestedBackend: firstNonEmpty(sandboxType, "auto"),
		SandboxResolvedBackend:  sandboxType,
		Route:                   route,
		HostExecution:           route == "host",
		Surface:                 bindingKey,
	}
	if gw, err := d.gateway(); err == nil && gw != nil {
		active := gw.ActiveTurns()
		status.ActiveJobs = len(active)
		status.Running = len(active) > 0
		if kind, ok := activeTurnKindForSession(active, activeSession.SessionRef); ok {
			status.ActiveTurnKind = kind
		}
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
