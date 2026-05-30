package acpserver

import (
	"context"
	"strings"

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

func (s *Server) executeCommandPrompt(ctx context.Context, ref session.Ref, input string) (bool, error) {
	if s.services.Engine() == nil {
		return false, nil
	}
	result, err := s.services.Commands().Execute(ctx, appservices.CommandExecutionRequest{
		SessionRef: ref,
		Input:      input,
	})
	if err != nil || !result.Handled {
		return result.Handled, err
	}
	return true, s.publishCommandOutput(ref.SessionID, result.Output)
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
