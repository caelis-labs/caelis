package application

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// Tools binds immutable callbacks to authoritative stored scope and profile.
// Source is trusted Control provenance for the current input; Runtime supplies
// each invocation's Session, Turn and item identity through tool.Call.Execution.
func (s *Store) Tools(ctx context.Context, binding Binding, source Source) ([]tool.Tool, error) {
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
	out := make([]tool.Tool, 0, len(stored.Profile.Tools))
	for _, def := range stored.Profile.Tools {
		out = append(out, callbackTool{store: s, binding: stored, source: source, definition: tool.Definition{Name: def.Name, Description: tool.ExternalCapabilityDescriptionPrefix + "\n" + def.Description, InputSchema: def.InputSchema, EffectClass: tool.EffectNonIdempotent, Metadata: map[string]any{tool.MetadataExternalCapability: true, tool.MetadataDescriptionAuthority: tool.MetadataAuthorityNonAuthorizing}}})
	}
	return out, nil
}

type callbackTool struct {
	store      *Store
	binding    Binding
	source     Source
	definition tool.Definition
}

func (t callbackTool) Definition() tool.Definition { return tool.CloneDefinition(t.definition) }
func (t callbackTool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	identity := call.Execution
	if identity.SessionID != t.binding.SessionID || !validID(identity.TurnID) || !validID(identity.ItemID) || call.Name != t.definition.Name {
		return tool.Result{}, ErrUnauthorized
	}
	result, err := t.store.Invoke(ctx, CallContext{Scope: t.binding.Scope, SessionID: identity.SessionID, TurnID: identity.TurnID, ItemID: identity.ItemID, CallID: call.ID, ToolsVersion: t.binding.Profile.ToolsVersion, Source: t.source}, t.definition.Name, call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{ID: call.ID, Name: t.definition.Name, Content: []model.Part{model.NewTextPart(string(raw))}, IsError: result.Outcome != "succeeded"}, nil
}
