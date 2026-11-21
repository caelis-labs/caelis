package sessionprofile

import (
	"bytes"
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ToolsForConfiguration binds callbacks to an exact stored historical revision,
// not the latest profile. Binding remains the immutable creation ownership record.
// Runtime supplies invocation Session, Turn and item through the call context.
func (s *Store) ToolsForConfiguration(ctx context.Context, binding Binding, configuration Configuration, source Source) ([]tool.Tool, error) {
	if err := ValidateSource(source); err != nil {
		return nil, err
	}
	if err := s.CheckActive(ctx, binding.Scope); err != nil {
		return nil, err
	}
	stored, err := s.GetBinding(ctx, binding.Scope, binding.SessionID)
	if err != nil {
		return nil, err
	}
	if stored.Archived {
		return nil, ErrRevoked
	}
	a, err := encode(stored)
	if err != nil {
		return nil, err
	}
	b, err := encode(binding)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(a, b) {
		return nil, ErrConflict
	}
	if configuration.Revision == 0 || configuration.SessionID != stored.SessionID {
		return nil, ErrConflict
	}
	pinned, err := s.ConfigurationRevision(ctx, stored.Scope, stored.SessionID, configuration.Revision)
	if err != nil {
		return nil, err
	}
	given, err := encode(configuration.Profile)
	if err != nil {
		return nil, err
	}
	actual, err := encode(pinned.Profile)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(given, actual) {
		return nil, ErrConflict
	}
	out := make([]tool.Tool, 0, len(pinned.Profile.Tools))
	for _, def := range pinned.Profile.Tools {
		definition := tool.Definition{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
			Metadata: map[string]any{
				callbackPolicyMetadataKey: callbackPolicyMetadata{approvalPolicy: def.ApprovalPolicy},
			},
			ResultIncludesReceipt: def.ResultFormat == ResultFormatContentV1,
		}
		out = append(out, callbackTool{store: s, binding: stored, configuration: pinned, source: source, resultFormat: def.ResultFormat, definition: definition})
	}
	return out, nil
}

type callbackTool struct {
	store         *Store
	binding       Binding
	configuration Configuration
	source        Source
	resultFormat  string
	definition    tool.Definition
}

func (t callbackTool) Definition() tool.Definition { return tool.CloneDefinition(t.definition) }
func (t callbackTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	return t.invoke(ctx, call, tool.InvocationFromContext(ctx))
}

func (t callbackTool) invoke(ctx context.Context, call tool.Call, identity tool.InvocationContext) (tool.Result, error) {
	if identity.SessionID != t.binding.SessionID || !validID(identity.TurnID) || !validID(identity.ItemID) || call.Name != t.definition.Name {
		return tool.Result{}, ErrUnauthorized
	}
	provenance := CallContext{Scope: t.binding.Scope, SessionID: identity.SessionID, TurnID: identity.TurnID, ItemID: identity.ItemID, CallID: call.ID, ToolsVersion: t.configuration.Profile.ToolsVersion, ConfigurationRevision: t.configuration.Revision, Source: t.source}
	result, receiptID, err := t.store.invoke(ctx, provenance, t.definition.Name, call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	return t.store.projectResult(ctx, provenance, receiptID, t.resultFormat, result)
}
