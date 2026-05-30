package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
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
			{Name: "model", Description: "Switch or inspect models", InputHint: "use <alias> [reasoning]"},
			{Name: "approval", Description: "Switch approval mode", InputHint: "auto-review|manual"},
			{Name: "status", Description: "Show current runtime status"},
			{Name: "resume", Description: "Resume a previous session", InputHint: "session id"},
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
