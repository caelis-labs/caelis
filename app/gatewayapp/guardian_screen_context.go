package gatewayapp

import (
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/internal/kernel"
)

type guardianDialogueMessage struct {
	Role model.Role `json:"role"`
	Text string     `json:"text"`
}

func guardianScreenState(req kernel.ApprovalReviewRequest, events []*session.Event) (map[string]any, error) {
	action := guardianPlannedAction(req)
	input, _ := action["arguments"].(map[string]any)
	if action["tool"] == "unknown" || len(input) == 0 {
		return nil, &guardianScreenError{reason: "input_invalid", cause: fmt.Errorf("classifier requires the exact tool and arguments")}
	}
	if origin := req.RuntimeRequest.Origin; origin != nil && origin.WorkingDirectory != "" {
		action["working_directory"] = origin.WorkingDirectory
	}
	if !guardianActionWithinStructuralLimits(action) {
		return nil, &guardianScreenError{reason: "input_budget", cause: fmt.Errorf("exact approval request exceeds classifier input budget")}
	}
	messages := make([]guardianDialogueMessage, 0, len(events))
	for _, event := range events {
		switch {
		case guardianIsUser(event):
			messages = append(messages, guardianDialogueMessage{Role: model.RoleUser, Text: session.EventText(event)})
		case guardianIsAssistantContext(event):
			messages = append(messages, guardianDialogueMessage{Role: model.RoleAssistant, Text: session.EventText(event)})
		}
	}
	return map[string]any{"conversation": messages, "action": action}, nil
}

func guardianMCPSource(req kernel.ApprovalReviewRequest) map[string]any {
	def := req.RuntimeRequest.Tool
	if !mcp.IsDefinition(def) {
		return nil
	}
	source := map[string]any{
		"plugin":             def.Metadata[mcp.MetadataPluginID],
		"server":             def.Metadata[mcp.MetadataServer],
		"remote_tool":        def.Metadata[mcp.MetadataTool],
		"source_fingerprint": def.Metadata[mcp.MetadataSourceFingerprint],
	}
	if authorization := req.RuntimeRequest.Authorization; authorization != nil {
		scope := authorization.Scope
		source["authorization_scope"] = scope
		choices := map[string]any{}
		for _, choice := range authorization.Choices {
			choices[choice.OptionID] = string(choice.Persistence)
		}
		source["scope_options"] = choices
	}

	return source
}

type guardianScreenError struct {
	reason string
	cause  error
}

func (e *guardianScreenError) Error() string { return e.reason + ": " + e.cause.Error() }
func (e *guardianScreenError) Unwrap() error { return e.cause }

// Reasons are fixed labels. Provider errors may contain sensitive response data.
func guardianScreenReason(err error) string {
	var failure *guardianScreenError
	if errors.As(err, &failure) {
		return failure.reason
	}
	return "evidence_unavailable"
}
