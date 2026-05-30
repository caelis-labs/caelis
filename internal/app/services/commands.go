package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type CommandCatalogRequest struct{}

type CommandExecutionRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
	Input      string      `json:"input,omitempty"`
}

func (s CommandService) Available(context.Context, CommandCatalogRequest) (appviewmodel.CommandCatalogView, error) {
	return appviewmodel.CommandCatalogView{
		Commands: []appviewmodel.CommandView{
			{Name: "agent", Description: "Manage ACP agents", InputHint: "use|add|install|list|remove"},
			{Name: "connect", Description: "Configure a model provider", InputHint: "provider model [base-url] [timeout] [token] [context] [max-output] [reasoning-levels]"},
			{Name: "model", Description: "Switch or inspect models", InputHint: "use <alias> [reasoning]|del <alias>"},
			{Name: "approval", Description: "Inspect or switch approval mode", InputHint: "[auto-review|manual]"},
			{Name: "status", Description: "Show current runtime status"},
			{Name: "resume", Description: "Resume a previous session", InputHint: "[session id]"},
			{Name: "compact", Description: "Compact the current conversation"},
		},
	}, nil
}

func (s CommandService) Execute(ctx context.Context, req CommandExecutionRequest) (appviewmodel.CommandExecutionView, error) {
	command, args, ok := parseSlashCommand(req.Input)
	if !ok {
		return appviewmodel.CommandExecutionView{}, nil
	}
	switch command {
	case "approval":
		return s.executeApproval(ctx, req.SessionRef, args)
	case "model":
		return s.executeModel(ctx, req.SessionRef, args)
	case "resume":
		return s.executeResume(ctx, args)
	case "status":
		if strings.TrimSpace(args) != "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /status")
		}
		status, err := s.services.Status().View(ctx, StatusRequest{SessionRef: req.SessionRef})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: command,
			Output:  formatCommandStatus(status),
		}, nil
	case "compact":
		if strings.TrimSpace(args) != "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /compact")
		}
		if _, err := s.services.Compaction().Compact(ctx, CompactSessionRequest{
			SessionRef: req.SessionRef,
			Trigger:    "manual",
		}); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: command,
			Output:  "compaction completed",
		}, nil
	default:
		return appviewmodel.CommandExecutionView{}, nil
	}
}

func (s CommandService) executeApproval(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	mode := strings.TrimSpace(args)
	if mode == "" {
		current, err := s.services.Modes().Current(ctx, ref)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: "approval",
			Output:  "approval mode: " + firstNonEmpty(current.ID, "auto-review"),
		}, nil
	}
	fields := strings.Fields(mode)
	if len(fields) != 1 {
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /approval [auto-review|manual]")
	}
	next, err := s.services.Modes().Set(ctx, ref, fields[0])
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	return appviewmodel.CommandExecutionView{
		Handled: true,
		Command: "approval",
		Output:  "approval mode: " + next.ID,
	}, nil
}

func (s CommandService) executeModel(ctx context.Context, ref session.Ref, args string) (appviewmodel.CommandExecutionView, error) {
	sub, rest, hasSub := splitCommandArg(args)
	if !hasSub || strings.EqualFold(sub, "list") || strings.EqualFold(sub, "ls") {
		if strings.TrimSpace(rest) != "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model [list]")
		}
		choices, err := s.services.Models().List(ctx)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		currentID := ""
		if len(choices) > 0 {
			if current, ok, err := s.services.Models().Current(ctx, ref); err != nil {
				return appviewmodel.CommandExecutionView{}, err
			} else if ok {
				currentID = current.ID
			}
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: "model",
			Output:  formatCommandModels(choices, currentID),
		}, nil
	}
	switch strings.ToLower(sub) {
	case "use":
		modelRef, reasoning := parseCommandModelUse(rest)
		if modelRef == "" {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model use <alias> [reasoning]")
		}
		cfg, err := s.services.Models().Use(ctx, ref, modelRef, reasoning)
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		output := "model switched to: " + formatModelConfigName(cfg)
		if reasoning != "" {
			output += " (reasoning: " + reasoning + ")"
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: "model",
			Output:  output,
		}, nil
	case "del", "delete", "rm":
		modelRef := strings.TrimSpace(rest)
		if modelRef == "" || strings.ContainsAny(modelRef, " \t\n") {
			return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model del <alias>")
		}
		if err := s.services.Models().Delete(ctx, modelRef); err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: "model",
			Output:  "model deleted: " + modelRef,
		}, nil
	default:
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /model [list|use <alias> [reasoning]|del <alias>]")
	}
}

func (s CommandService) executeResume(ctx context.Context, args string) (appviewmodel.CommandExecutionView, error) {
	sessionID := strings.TrimSpace(args)
	if sessionID == "" {
		page, err := s.services.Sessions().List(ctx, ListSessionsRequest{Limit: 10})
		if err != nil {
			return appviewmodel.CommandExecutionView{}, err
		}
		return appviewmodel.CommandExecutionView{
			Handled: true,
			Command: "resume",
			Output:  formatCommandSessions(page),
		}, nil
	}
	if strings.ContainsAny(sessionID, " \t\n") {
		return appviewmodel.CommandExecutionView{}, fmt.Errorf("app/services: usage: /resume [session id]")
	}
	snapshot, err := s.services.Sessions().Load(ctx, session.Ref{SessionID: sessionID})
	if err != nil {
		return appviewmodel.CommandExecutionView{}, err
	}
	ref := snapshot.Session.Ref
	return appviewmodel.CommandExecutionView{
		Handled:    true,
		Command:    "resume",
		Output:     formatCommandResume(snapshot),
		SessionRef: &ref,
	}, nil
}

func parseSlashCommand(input string) (string, string, bool) {
	trimmed := strings.TrimSpace(input)
	if !strings.HasPrefix(trimmed, "/") || strings.HasPrefix(trimmed, "//") {
		return "", "", false
	}
	trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "/"))
	if trimmed == "" {
		return "", "", false
	}
	command, args, _ := strings.Cut(trimmed, " ")
	command = strings.ToLower(strings.TrimSpace(command))
	if command == "" {
		return "", "", false
	}
	return command, strings.TrimSpace(args), true
}

func splitCommandArg(input string) (string, string, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return "", "", false
	}
	head, tail, ok := strings.Cut(trimmed, " ")
	if !ok {
		return strings.TrimSpace(head), "", true
	}
	return strings.TrimSpace(head), strings.TrimSpace(tail), true
}

func parseCommandModelUse(args string) (string, string) {
	modelRef, reasoning, _ := strings.Cut(strings.TrimSpace(args), " ")
	return strings.TrimSpace(modelRef), strings.ToLower(strings.TrimSpace(reasoning))
}

func formatCommandStatus(status appviewmodel.StatusView) string {
	lines := []string{"status:"}
	if status.Session != nil {
		if sessionID := strings.TrimSpace(status.Session.Ref.SessionID); sessionID != "" {
			lines = append(lines, "  session: "+sessionID)
		}
		if title := strings.TrimSpace(status.Session.Title); title != "" {
			lines = append(lines, "  title: "+title)
		}
	}
	if status.Model.Current != nil {
		modelText := firstNonEmpty(status.Model.Current.Alias, status.Model.Current.Model, status.Model.Current.ID)
		detail := firstNonEmpty(status.Model.Current.Provider, status.Model.Current.Detail)
		if detail != "" {
			modelText += " (" + detail + ")"
		}
		lines = append(lines, "  model: "+modelText)
	} else if status.Model.Configured {
		lines = append(lines, fmt.Sprintf("  model: %d configured", status.Model.Count))
	} else {
		lines = append(lines, "  model: not configured")
	}
	if mode := strings.TrimSpace(status.Mode.Current.ID); mode != "" {
		lines = append(lines, "  mode: "+mode)
	}
	if status.Runtime.StoreBackend != "" || status.Runtime.StoreURI != "" {
		store := firstNonEmpty(status.Runtime.StoreBackend, "store")
		if status.Runtime.StoreURI != "" {
			store += " " + status.Runtime.StoreURI
		}
		lines = append(lines, "  store: "+store)
	}
	if sandbox := strings.TrimSpace(status.Runtime.SandboxBackend); sandbox != "" {
		lines = append(lines, "  sandbox: "+sandbox)
	}
	if status.Agents.Count > 0 {
		lines = append(lines, fmt.Sprintf("  agents: %d", status.Agents.Count))
	}
	if status.Resources.ErrorCount > 0 || status.Resources.WarningCount > 0 {
		lines = append(lines, fmt.Sprintf("  resources: %d warnings, %d errors", status.Resources.WarningCount, status.Resources.ErrorCount))
	}
	if status.Usage.Total.TotalTokens > 0 {
		lines = append(lines, fmt.Sprintf("  tokens: %d", status.Usage.Total.TotalTokens))
	}
	return strings.Join(lines, "\n")
}

func formatCommandModels(choices []appsettings.ModelChoice, currentID string) string {
	lines := []string{"models:"}
	if len(choices) == 0 {
		lines = append(lines, "  none configured")
		return strings.Join(lines, "\n")
	}
	currentID = strings.TrimSpace(currentID)
	for _, choice := range choices {
		name := formatModelChoiceName(choice)
		markers := []string{}
		if choice.Default {
			markers = append(markers, "default")
		}
		if currentID != "" && strings.EqualFold(choice.ID, currentID) {
			markers = append(markers, "current")
		}
		if len(markers) > 0 {
			name += " (" + strings.Join(markers, ", ") + ")"
		}
		lines = append(lines, "  "+name)
	}
	return strings.Join(lines, "\n")
}

func formatModelChoiceName(choice appsettings.ModelChoice) string {
	label := firstNonEmpty(choice.Alias, choice.ID, choice.Model)
	providerModel := strings.Trim(strings.TrimSpace(choice.Provider)+"/"+strings.TrimSpace(choice.Model), "/")
	if label == "" {
		label = providerModel
	} else if providerModel != "" && !strings.EqualFold(label, providerModel) {
		label += "  " + providerModel
	}
	return label
}

func formatModelConfigName(cfg appsettings.ModelConfig) string {
	label := firstNonEmpty(cfg.Alias, cfg.ID, cfg.Model)
	providerModel := strings.Trim(strings.TrimSpace(cfg.Provider)+"/"+strings.TrimSpace(cfg.Model), "/")
	if label == "" {
		label = providerModel
	} else if providerModel != "" && !strings.EqualFold(label, providerModel) {
		label += "  " + providerModel
	}
	return label
}

func formatCommandSessions(page session.SessionPage) string {
	lines := []string{"available sessions:"}
	if len(page.Sessions) == 0 {
		lines = append(lines, "  none")
		return strings.Join(lines, "\n")
	}
	for _, item := range page.Sessions {
		sessionID := strings.TrimSpace(item.Session.SessionID)
		if sessionID == "" {
			continue
		}
		line := "  " + sessionID
		if title := strings.TrimSpace(item.Session.Title); title != "" {
			line += "  " + title
		}
		if !item.Session.UpdatedAt.IsZero() {
			line += "  (" + item.Session.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z") + ")"
		} else if !item.LastEventAt.IsZero() {
			line += "  (" + item.LastEventAt.UTC().Format("2006-01-02T15:04:05Z") + ")"
		}
		lines = append(lines, line)
	}
	if len(lines) == 1 {
		lines = append(lines, "  none")
	}
	return strings.Join(lines, "\n")
}

func formatCommandResume(snapshot session.Snapshot) string {
	sessionID := strings.TrimSpace(snapshot.Session.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(snapshot.Session.Ref.SessionID)
	}
	lines := []string{"resume session: " + sessionID}
	if title := strings.TrimSpace(snapshot.Session.Title); title != "" {
		lines = append(lines, "  title: "+title)
	}
	if cwd := strings.TrimSpace(snapshot.Session.Workspace.CWD); cwd != "" {
		lines = append(lines, "  cwd: "+cwd)
	}
	lines = append(lines, fmt.Sprintf("  events: %d", len(snapshot.Events)))
	return strings.Join(lines, "\n")
}
