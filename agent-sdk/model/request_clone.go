package model

import (
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
)

// CloneRequest returns a deep copy of one provider-neutral model request.
func CloneRequest(in *Request) *Request {
	if in == nil {
		return nil
	}
	out := *in
	if in.Source != nil {
		source := *in.Source
		out.Source = &source
	}
	out.Instructions = CloneParts(in.Instructions)
	out.Messages = CloneMessages(in.Messages)
	out.Tools = CloneToolSpecs(in.Tools)
	out.Output = CloneOutputSpec(in.Output)
	return &out
}

// CloneOutputSpec returns a deep copy of one output spec.
func CloneOutputSpec(in *OutputSpec) *OutputSpec {
	if in == nil {
		return nil
	}
	out := *in
	out.JSONSchema = jsonvalue.CloneMap(in.JSONSchema)
	return &out
}

// CloneToolSpecs returns a deep copy of model-visible tool declarations.
func CloneToolSpecs(in []ToolSpec) []ToolSpec {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolSpec, 0, len(in))
	for _, spec := range in {
		cp := spec
		if spec.Function != nil {
			fn := *spec.Function
			fn.Parameters = jsonvalue.CloneMap(spec.Function.Parameters)
			cp.Function = &fn
		}
		if spec.ProviderExecuted != nil {
			executed := *spec.ProviderExecuted
			executed.ProviderDetails = cloneRawMessageMap(spec.ProviderExecuted.ProviderDetails)
			cp.ProviderExecuted = &executed
		}
		out = append(out, cp)
	}
	return out
}

func cloneRawMessageMap(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}
