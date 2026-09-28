package application

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/internal/jsonvalue"
)

const callbackPolicyMetadataKey = "caelis.application.callback_policy"

// callbackPolicyMetadata is issued only when an authenticated binding and an
// exact stored catalog revision have been checked by ToolsForConfiguration.
// A private Go type (rather than a tool name or a JSON flag) prevents a model
// call or another tool's untrusted metadata from claiming callback authority.
type callbackPolicyMetadata struct {
	approvalPolicy string
}

// CallbackPolicyDecision handles only callbacks assembled from an authenticated,
// pinned application catalog. Unrecognized definitions are left for the caller's
// ordinary policy mode. Direct callbacks bypass review, preserving the legacy
// dispatch path; required callbacks ask Runtime for per-call approval before the
// callback tool can persist an intent in Store.Invoke.
func CallbackPolicyDecision(input policy.ToolContext) (decision policy.Decision, handled bool, err error) {
	marker, ok := input.Tool.Metadata[callbackPolicyMetadataKey].(callbackPolicyMetadata)
	if !ok {
		return policy.Decision{}, false, nil
	}
	if input.Call.Name != input.Tool.Name {
		return policy.Decision{}, true, fmt.Errorf("%w: callback tool mismatch", ErrUnauthorized)
	}
	switch marker.approvalPolicy {
	case "", "direct":
		return policy.Decision{Action: policy.ActionAllow}, true, nil
	case "required":
		if len(input.Call.Input) > maxCallBytes {
			return policy.Decision{}, true, ErrInvalid
		}
		if !json.Valid(input.Call.Input) {
			return policy.Decision{}, true, fmt.Errorf("%w: malformed callback arguments", ErrInvalid)
		}
		// Review inputs must fit the same numeric contract as public approval
		// and callback delivery. Reject before minting any approval authority;
		// converting numeric arguments to strings would change the invocation.
		if err := jsonvalue.ValidateNumbers(input.Call.Input); err != nil {
			return policy.Decision{}, true, fmt.Errorf("%w: callback arguments: %w", ErrInvalid, err)
		}
		var args map[string]any
		decoder := json.NewDecoder(bytes.NewReader(input.Call.Input))
		decoder.UseNumber()
		if err := decoder.Decode(&args); err != nil {
			return policy.Decision{}, true, fmt.Errorf("%w: malformed callback arguments: %w", ErrInvalid, err)
		}
		validated, err := schemaValue(args)
		if err != nil {
			return policy.Decision{}, true, fmt.Errorf("%w: callback arguments: %w", ErrInvalid, err)
		}
		schema, err := resolveSchema(input.Tool.InputSchema)
		if err != nil {
			return policy.Decision{}, true, fmt.Errorf("%w: callback schema: %w", ErrInvalid, err)
		}
		if err := schema.Validate(validated); err != nil {
			return policy.Decision{}, true, fmt.Errorf("%w: callback arguments: %w", ErrInvalid, err)
		}
		approval := &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID: input.Call.ID, Name: input.Tool.Name, Kind: "other",
				Title:  "Application callback: " + input.Tool.Name,
				Status: "pending", RawInput: args,
			},
			Options: []session.ProtocolApprovalOption{
				{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		}
		// Catalog text is action context, never an instruction or a grant.
		if input.Tool.Description != "" {
			approval.ToolCall.Content = []session.ProtocolToolCallContent{{
				Type: "content", Content: session.ProtocolTextContent(input.Tool.Description),
			}}
		}
		return policy.Decision{
			Action: policy.ActionAskApproval, Reason: "Application callback requires approval",
			Approval: approval,
		}, true, nil
	default:
		return policy.Decision{}, true, fmt.Errorf("%w: unsupported callback approval_policy", ErrInvalid)
	}
}
