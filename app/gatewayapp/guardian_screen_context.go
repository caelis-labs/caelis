package gatewayapp

import (
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/internal/kernel"
)

// Screening uses only user messages and the current approval ticket. It neither
// selects historical observations nor waits for command Tasks. Agent fallback
// retains its independent, complete source projection.
func guardianScreenState(req kernel.ApprovalReviewRequest, events []*session.Event) (map[string]any, error) {
	payload := req.Approval
	name := firstNonEmpty(payload.ToolName, req.RuntimeRequest.Call.Name, req.RuntimeRequest.Tool.Name)
	input := payload.RawInput
	if len(input) == 0 {
		input = rawJSONMap(req.RuntimeRequest.Call.Input)
	}
	if name == "" || len(input) == 0 {
		return nil, &guardianScreenError{reason: "input_invalid", cause: fmt.Errorf("classifier requires the exact tool and arguments")}
	}
	action := map[string]any{"tool": name, "arguments": input}
	if source := guardianMCPSource(req); source != nil {
		action["mcp_source"] = source
	}
	if payload.Reason != "" {
		action["reason"] = payload.Reason
	}
	if payload.Justification != "" {
		action["justification"] = payload.Justification
	}
	if payload.SandboxPermissions != "" {
		action["sandbox_permissions"] = payload.SandboxPermissions
	}
	if origin := req.RuntimeRequest.Origin; origin != nil && origin.WorkingDirectory != "" {
		action["working_directory"] = origin.WorkingDirectory
	}
	if !guardianActionWithinStructuralLimits(action) {
		return nil, &guardianScreenError{reason: "input_budget", cause: fmt.Errorf("exact approval request exceeds classifier input budget")}
	}
	var users []string
	for _, event := range events {
		if guardianIsUser(event) {
			users = append(users, session.EventText(event))
		}
	}
	return map[string]any{"user_messages": users, "action": action}, nil
}

func guardianMCPSource(req kernel.ApprovalReviewRequest) map[string]any {
	def := req.RuntimeRequest.Tool
	if !tool.IsMCPDefinition(def) {
		return nil
	}
	source := map[string]any{
		"plugin":             def.Metadata[tool.MetadataPluginID],
		"server":             def.Metadata[tool.MetadataMCPServer],
		"remote_tool":        def.Metadata[tool.MetadataMCPTool],
		"source_fingerprint": def.Metadata[tool.MetadataMCPSourceFingerprint],
	}
	if resource := guardianMCPResourceScope(req); resource != nil {
		source["resource_scope"] = resource
		source["scope_options"] = map[string]any{
			"allow_session": "this exact resource in this real Session, across Turns; no other resource or Session",
			"cancel":        "no remote call and no resource grant",
		}
	} else if guardianNativeMCPScopeOptions(req) {
		source["scope_options"] = map[string]any{
			"allow_once":    "this call only",
			"allow_session": "this real Session only, across Turns",
			"allow_always":  "this workspace and exact MCP source, across Sessions and restarts until revoked",
			"cancel":        "no remote call",
		}
	}
	return source
}

// Resource scope is a Host-assembled rule over the exact original argument.
// Remote metadata, approval prose and arbitrary option IDs cannot establish it.
func guardianMCPResourceScope(req kernel.ApprovalReviewRequest) map[string]any {
	def := req.RuntimeRequest.Tool
	resourceGate, _ := req.RuntimeRequest.Metadata[approval.MetadataMCPResourceGate].(bool)
	if !resourceGate || req.Approval == nil || !tool.IsMCPDefinition(def) || len(req.Approval.Options) != 2 {
		return nil
	}
	parameter, _ := def.Metadata[tool.MetadataMCPResourceParameter].(string)
	if parameter == "" {
		return nil
	}
	want := [2]struct{ id, kind string }{{"allow_session", "allow_once"}, {"cancel", "reject_once"}}
	for index, option := range req.Approval.Options {
		if option.ID != want[index].id || option.Kind != want[index].kind {
			return nil
		}
	}
	value, ok := rawJSONMap(req.RuntimeRequest.Call.Input)[parameter].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return nil
	}
	return map[string]any{"parameter": parameter, "value": value}
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
