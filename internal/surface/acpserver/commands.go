package acpserver

import (
	"context"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func (s *Server) responseWithAvailableCommands(ctx context.Context, payload any, err error, sessionID string) (any, *jsonrpc.RPCError) {
	if err != nil {
		return responseOrError(payload, err)
	}
	return s.withAvailableCommands(ctx, payload, sessionID), nil
}

func (s *Server) withAvailableCommands(ctx context.Context, payload any, sessionID string) any {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || s.conn == nil || s.services.Engine() == nil {
		return payload
	}
	return jsonrpc.PostWriteResult{
		Payload: payload,
		AfterWrite: func() {
			_ = s.publishAvailableCommands(context.WithoutCancel(ctx), sessionID)
		},
	}
}

func (s *Server) publishAvailableCommands(ctx context.Context, sessionID string) error {
	if s.conn == nil {
		return nil
	}
	catalog, err := s.services.Commands().Available(ctx, appservices.CommandCatalogRequest{})
	if err != nil {
		return err
	}
	commands := acpAvailableCommands(catalog.Commands)
	if len(commands) == 0 {
		return nil
	}
	return s.conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
		SessionID: strings.TrimSpace(sessionID),
		Update: schema.AvailableCommandsUpdate{
			SessionUpdate:     schema.UpdateAvailableCmds,
			AvailableCommands: commands,
		},
	})
}

func (s *Server) executeCommandPrompt(ctx context.Context, ref session.Ref, input string, parts []model.ContentPart) (bool, error) {
	if s.services.Engine() == nil {
		return false, nil
	}
	result, err := s.services.Commands().Execute(ctx, appservices.CommandExecutionRequest{
		SessionRef:   ref,
		Input:        input,
		ContentParts: parts,
	})
	if err != nil || !result.Handled {
		return result.Handled, err
	}
	if err := s.publishCommandOutput(ref.SessionID, result.Output); err != nil {
		return true, err
	}
	if err := s.publishCommandSurfacePayloads(ref.SessionID, result); err != nil {
		return true, err
	}
	for _, event := range result.Events {
		if err := s.publishEvent(ctx, nil, event); err != nil {
			return true, err
		}
	}
	if result.SessionRef != nil && strings.TrimSpace(result.SessionRef.SessionID) != "" {
		snapshot, err := s.loadSnapshot(ctx, result.SessionRef.SessionID)
		if err != nil {
			return true, err
		}
		if err := s.publishSnapshot(ctx, snapshot); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (s *Server) publishCommandSurfacePayloads(sessionID string, result appviewmodel.CommandExecutionView) error {
	if s.conn == nil {
		return nil
	}
	updates := acpSurfaceUpdatesFromCommand(result)
	for _, update := range updates {
		if err := s.conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
			SessionID: strings.TrimSpace(sessionID),
			Update:    update,
		}); err != nil {
			return err
		}
	}
	return nil
}

func acpSurfaceUpdatesFromCommand(result appviewmodel.CommandExecutionView) []schema.SurfaceUpdate {
	command := strings.TrimSpace(result.Command)
	out := make([]schema.SurfaceUpdate, 0, 4)
	add := func(kind string, payload any) {
		if payload == nil {
			return
		}
		out = append(out, schema.SurfaceUpdate{
			SessionUpdate: schema.UpdateSurface,
			Surface:       "acp",
			Kind:          strings.TrimSpace(kind),
			Payload:       payload,
			Meta: map[string]any{
				"source":  "app-services",
				"command": command,
			},
		})
	}
	if result.Status != nil {
		add("status", result.Status)
	}
	if result.Doctor != nil {
		add("doctor", result.Doctor)
	}
	if result.SettingsPanel != nil {
		add("settings_panel", result.SettingsPanel)
	}
	if result.TaskPanel != nil {
		add("task_panel", result.TaskPanel)
	}
	if result.ResumePanel != nil {
		add("resume_panel", result.ResumePanel)
	}
	if result.ApprovalPanel != nil {
		add("approval_panel", result.ApprovalPanel)
	}
	if result.ModelSelection != nil {
		add("model_selection", result.ModelSelection)
	}
	if result.ControllerPanel != nil {
		add("controller_panel", result.ControllerPanel)
	}
	if result.ModelConnectPanel != nil {
		add("model_connect_panel", result.ModelConnectPanel)
	}
	if result.AgentManagement != nil {
		add("agent_management", result.AgentManagement)
	}
	return out
}

func (s *Server) publishCommandOutput(sessionID string, text string) error {
	if s.conn == nil {
		return nil
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return s.conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
		SessionID: strings.TrimSpace(sessionID),
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: text},
		},
	})
}

func acpAvailableCommands(commands []appviewmodel.CommandView) []schema.AvailableCommand {
	if len(commands) == 0 {
		return nil
	}
	out := make([]schema.AvailableCommand, 0, len(commands))
	for _, command := range commands {
		name := strings.TrimSpace(command.Name)
		if name == "" {
			continue
		}
		out = append(out, schema.AvailableCommand{
			Name:        name,
			Description: strings.TrimSpace(command.Description),
			Input:       acpCommandInput(command.InputHint),
		})
	}
	return out
}

func acpCommandInput(hint string) *schema.AvailableCommandInput {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return nil
	}
	return &schema.AvailableCommandInput{Hint: hint}
}
