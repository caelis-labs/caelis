package model

import "encoding/json"

// CloneRequest returns a deep copy of one provider-neutral model request.
func CloneRequest(in *Request) *Request {
	if in == nil {
		return nil
	}
	out := *in
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
	out.JSONSchema = cloneJSONMap(in.JSONSchema)
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
			fn.Parameters = cloneJSONMap(spec.Function.Parameters)
			cp.Function = &fn
		}
		if spec.ProviderDefined != nil {
			defined := *spec.ProviderDefined
			defined.ProviderDetails = cloneRawMessageMap(spec.ProviderDefined.ProviderDetails)
			cp.ProviderDefined = &defined
		}
		if spec.ProviderExecuted != nil {
			executed := *spec.ProviderExecuted
			executed.ProviderDetails = cloneRawMessageMap(spec.ProviderExecuted.ProviderDetails)
			cp.ProviderExecuted = &executed
		}
		if spec.MCP != nil {
			mcp := *spec.MCP
			cp.MCP = &mcp
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

func cloneJSONMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneJSONValue(value)
	}
	return out
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONMap(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneJSONValue(item)
		}
		return out
	default:
		return typed
	}
}
