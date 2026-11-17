package hookrun

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func toolEvidence(ctx context.Context, def tool.Definition, call tool.Call) agent.HookContext {
	e := FromContext(ctx).Evidence
	c := tool.CloneCall(call)
	d := tool.CloneDefinition(def)
	e.Tool = &c
	e.ToolDefinition = &d
	return e
}

func clonePreparation(in agent.ToolPreparation) agent.ToolPreparation {
	in.Input = append(json.RawMessage(nil), in.Input...)
	if in.Result != nil {
		r, _ := tool.CloneResult(*in.Result, nil)
		in.Result = &r
	}
	return in
}

// PrepareTool runs after trustworthy occurrence/target assignment and before
// any member of the batch is committed or admitted by Policy.
func PrepareTool(ctx context.Context, def tool.Definition, call tool.Call) (tool.Call, *tool.Result, error) {
	p := agent.ToolPreparation{Kind: agent.ToolPrepareContinue, Input: append(json.RawMessage(nil), call.Input...)}
	e := toolEvidence(ctx, def, call)
	for _, h := range FromContext(ctx).Hooks.ToolPrepare {
		next, err := Call(ctx, "tool_prepare", h, e, p, clonePreparation, clonePreparation)
		if err != nil {
			return call, nil, err
		}
		bad := func(reason string) (tool.Call, *tool.Result, error) {
			return call, nil, &agent.HookError{Phase: "tool_prepare", Name: h.Name, Cause: fmt.Errorf("%s", reason)}
		}
		switch next.Kind {
		case agent.ToolPrepareContinue:
			if next.Result != nil {
				return bad("continue cannot carry a result")
			}
		case agent.ToolPrepareRespond:
			if next.Result == nil {
				return bad("respond requires a result")
			}
		case agent.ToolPrepareBlock:
			if next.Result != nil {
				return bad("block cannot carry a result")
			}
		default:
			return bad("invalid preparation kind")
		}
		p = next
	}
	if len(p.Input) == 0 {
		p.Input = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(p.Input, &object); err != nil || object == nil {
		return call, nil, fmt.Errorf("tool arguments require one JSON object")
	}
	if def.InputSchema != nil {
		if err := tool.ValidateMCPArguments(def, p.Input); err != nil {
			return call, nil, err
		}
	}
	call.Input = append(json.RawMessage(nil), p.Input...)
	if p.Kind == agent.ToolPrepareBlock {
		r := tool.Result{ID: call.ID, Name: call.Name, IsError: true, Content: []model.Part{model.NewTextPart("Tool preparation blocked this call")}}
		return call, &r, nil
	}
	if p.Result != nil {
		p.Result.ID = call.ID
		p.Result.Name = call.Name
		if p.Result.TurnComplete {
			return call, nil, fmt.Errorf("synthetic tool result cannot complete the Turn")
		}
		if err := ValidateResult(def, *p.Result); err != nil {
			return call, nil, err
		}
	}
	return call, p.Result, nil
}

// ValidateResult checks structured provenance, output schema and serializable
// media/data before a transformed value reaches either JS Body or persistence.
func ValidateResult(def tool.Definition, r tool.Result) error {
	if _, err := json.Marshal(r); err != nil {
		return err
	}
	for _, p := range r.Content {
		switch p.Kind {
		case model.PartKindText:
			if p.Text == nil {
				return fmt.Errorf("missing text payload")
			}
		case model.PartKindJSON:
			if p.JSON == nil || !json.Valid(p.JSON.Value) {
				return fmt.Errorf("invalid JSON result")
			}
		case model.PartKindMedia:
			if p.Media == nil || p.Media.Source.Kind == "" {
				return fmt.Errorf("invalid media result")
			}
		case model.PartKindFileRef:
			if p.FileRef == nil {
				return fmt.Errorf("invalid file result")
			}
		default:
			return fmt.Errorf("unsupported business result part %q", p.Kind)
		}
	}
	if s := r.StructuredContent; s != nil {
		if s.ContentIndex < 0 || s.ContentIndex >= len(r.Content) || !json.Valid(s.Value) || r.Content[s.ContentIndex].JSON == nil || string(r.Content[s.ContentIndex].JSON.Value) != string(s.Value) {
			return fmt.Errorf("invalid structured result provenance")
		}
		if !r.IsError && def.OutputSchema != nil {
			if err := tool.ValidateMCPArguments(tool.Definition{InputSchema: def.OutputSchema}, s.Value); err != nil {
				return fmt.Errorf("structured output schema: %w", err)
			}
		}
	}
	return nil
}

func FullResult(ctx context.Context, def tool.Definition, call tool.Call, result tool.Result, cause error) (tool.Result, error) {
	e := toolEvidence(ctx, def, call)
	for _, h := range FromContext(ctx).Hooks.ToolResult {
		input := agent.ToolOutcome{Result: result, Error: cause}
		next, err := Call(ctx, "tool_result", h, e, input, func(o agent.ToolOutcome) agent.ToolOutcome { o.Result, _ = tool.CloneResult(o.Result, nil); return o }, func(r tool.Result) tool.Result { r, _ = tool.CloneResult(r, nil); return r })
		if err == nil && (next.ID != result.ID || next.Name != result.Name || next.TurnComplete != result.TurnComplete || next.IsError != result.IsError || !reflect.DeepEqual(next.Metadata, result.Metadata)) {
			err = fmt.Errorf("result identity, error and system fields cannot change")
		}
		if err == nil {
			err = ValidateResult(def, next)
		}
		if err != nil {
			// Never fall back to raw content after a failed redaction. The error
			// preserves that an effect may have succeeded; callers never replay.
			safe := tool.Result{ID: call.ID, Name: call.Name, IsError: true, Content: []model.Part{model.NewTextPart("Tool completed; result processing failed. Do not repeat the operation.")}}
			return safe, &agent.HookError{Phase: "tool_result", Name: h.Name, Cause: err}
		}
		result = next
	}
	return result, cause
}

func InvokeTool(ctx context.Context, def tool.Definition, call tool.Call, base func(context.Context) (tool.Result, error)) (tool.Result, error) {
	e := toolEvidence(ctx, def, call)
	chain := base
	entries := FromContext(ctx).Hooks.ToolInvoke
	for i := len(entries) - 1; i >= 0; i-- {
		h := entries[i]
		following := chain
		chain = func(ctx context.Context) (tool.Result, error) {
			return Around(ctx, "tool_invoke", h.Name, h.Timeout, func(ctx context.Context, next func(context.Context) (tool.Result, error)) (tool.Result, error) {
				return h.Invoke(ctx, cloneContext(e), agent.ToolNext(next))
			}, following)
		}
	}
	r, err := chain(ctx)
	if err == nil && len(entries) > 0 {
		if r.ID != "" && r.ID != call.ID || r.Name != "" && r.Name != call.Name || r.TurnComplete {
			return tool.Result{}, fmt.Errorf("middleware changed tool identity or completion")
		}
		if check := ValidateResult(def, r); check != nil {
			return tool.Result{}, check
		}
	}
	return r, err
}

func ModelView(ctx context.Context, def tool.Definition, call tool.Call, m model.Message) (model.Message, error) {
	e := toolEvidence(ctx, def, call)
	for _, h := range FromContext(ctx).Hooks.ModelView {
		next, err := Call(ctx, "model_view", h, e, m, model.CloneMessage, model.CloneMessage)
		if err == nil {
			if next.Role != m.Role || len(next.Parts) != 1 || len(m.Parts) != 1 || next.Parts[0].ToolResult == nil || m.Parts[0].ToolResult == nil {
				err = fmt.Errorf("model view must preserve tool result envelope")
			} else {
				a, b := next.Parts[0].ToolResult, m.Parts[0].ToolResult
				if a.ToolUseID != b.ToolUseID || a.Name != b.Name || a.IsError != b.IsError {
					err = fmt.Errorf("model view changed tool identity or error")
				}
			}
		}
		if err != nil {
			return model.Message{}, &agent.HookError{Phase: "model_view", Name: h.Name, Cause: err}
		}
		m = next
	}
	return m, nil
}
