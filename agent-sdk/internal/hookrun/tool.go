package hookrun

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func toolEvidence(ctx context.Context, def tool.Definition, call tool.Call) agent.HookContext {
	e := FromContext(ctx).Evidence
	c := tool.CloneCall(call)
	d := tool.CloneDefinition(def)
	e.ToolExecution = tool.InvocationFromContext(ctx)
	e.ToolParent = tool.ParentFromContext(ctx)
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
	failure := func(err error) (tool.Call, *tool.Result, error) {
		entries := FromContext(ctx).Hooks.ToolPrepare
		if len(entries) > 0 {
			err = &agent.HookError{Phase: "tool_prepare", Name: entries[len(entries)-1].Name, Cause: err}
		}
		return call, nil, err
	}
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
	unchanged := bytes.Equal(call.Input, p.Input)
	if len(p.Input) == 0 {
		p.Input = json.RawMessage(`{}`)
	}
	// A rejected model proposal is a recoverable tool error. A hook that
	// supplied invalid arguments or a synthetic decision fails preparation.
	invalidInput := func(err error) (tool.Call, *tool.Result, error) {
		if p.Kind == agent.ToolPrepareContinue && unchanged {
			return call, nil, err
		}
		return failure(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(p.Input, &object); err != nil || object == nil {
		return invalidInput(tool.NewError(tool.ErrorCodeInvalidInput, "tool arguments require one JSON object"))
	}
	if def.InputSchema != nil {
		if err := tool.ValidateMCPArguments(def, p.Input); err != nil {
			return invalidInput(err)
		}
	}
	call.Input = append(json.RawMessage(nil), p.Input...)
	if p.Kind == agent.ToolPrepareBlock {
		const message = "Tool preparation blocked this call"
		r := tool.Result{ID: call.ID, Name: call.Name, Content: []tool.ContentBlock{tool.Text(message)}}
		return call, &r, tool.FailedReply(message)
	}
	if p.Result != nil {
		if p.Result.ID != "" && p.Result.ID != call.ID || p.Result.Name != "" && p.Result.Name != call.Name {
			return failure(fmt.Errorf("synthetic result changed call identity"))
		}
		p.Result.ID = call.ID
		p.Result.Name = call.Name
		if p.Result.TurnComplete {
			return failure(fmt.Errorf("synthetic tool result cannot complete the Turn"))
		}
		if err := validateResult(def, *p.Result, nil); err != nil {
			return failure(err)
		}
	}
	return call, p.Result, nil
}

func validateResult(def tool.Definition, r tool.Result, cause error) error {
	if _, err := json.Marshal(r); err != nil {
		return err
	}
	if value, ok := r.Structured(); ok && cause == nil && def.OutputSchema != nil {
		if err := tool.ValidateOutput(def.OutputSchema, value); err != nil {
			return fmt.Errorf("structured output schema: %w", err)
		}
	}
	return nil
}

func validateModelViewParts(parts []model.Part) error {
	for _, p := range parts {
		count := 0
		for _, set := range []bool{p.Text != nil, p.JSON != nil, p.Media != nil, p.FileRef != nil, p.ToolUse != nil, p.ToolResult != nil, p.Reasoning != nil} {
			if set {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("result part requires exactly one payload")
		}
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
			if p.Media == nil {
				return fmt.Errorf("invalid media result")
			}
			source := p.Media.Source
			valid := false
			switch source.Kind {
			case model.MediaSourceInline:
				valid = source.Data != ""
			case model.MediaSourceURL:
				valid = source.URI != ""
			case model.MediaSourceLocalRef:
				valid = source.LocalRef != ""
			case model.MediaSourceProviderFile:
				valid = source.FileID != ""
			}
			if !valid {
				return fmt.Errorf("invalid media source")
			}
		case model.PartKindFileRef:
			if p.FileRef == nil || p.FileRef.URI == "" && p.FileRef.FileID == "" && p.FileRef.LocalRef == "" {
				return fmt.Errorf("invalid file result")
			}
		default:
			return fmt.Errorf("unsupported business result part %q", p.Kind)
		}
	}
	return nil
}

func FullResult(ctx context.Context, def tool.Definition, call tool.Call, result tool.Result, cause error) (tool.Result, error) {
	var processing *ProcessingError
	if errors.As(cause, &processing) {
		return failedProcessingResult(call, result), cause
	}
	if err := tool.ValidateContent(result.Content); err != nil {
		return failedProcessingResult(call, result), &ProcessingError{Cause: err, EffectError: cause}
	}
	e := toolEvidence(ctx, def, call)
	for _, h := range FromContext(ctx).Hooks.ToolResult {
		input := agent.ToolOutcome{Result: result, Error: cause}
		next, err := Call(ctx, "tool_result", h, e, input, func(o agent.ToolOutcome) agent.ToolOutcome { o.Result, _ = tool.CloneResult(o.Result, nil); return o }, func(r tool.Result) tool.Result { r, _ = tool.CloneResult(r, nil); return r })
		if err == nil && (next.ID != result.ID || next.Name != result.Name || next.TurnComplete != result.TurnComplete || !reflect.DeepEqual(next.Metadata, result.Metadata) || !reflect.DeepEqual(next.Meta, result.Meta)) {
			err = fmt.Errorf("result identity and system fields cannot change")
		}
		if err == nil {
			_, hadStructured := result.Structured()
			_, hasStructured := next.Structured()
			if cause == nil && hadStructured && !hasStructured && def.OutputSchema != nil {
				err = fmt.Errorf("structured business result cannot evade its output schema")
			}
			if err == nil {
				err = validateResult(def, next, cause)
			}
		}
		if err != nil {
			// Never fall back to raw content after a failed redaction. The error
			// preserves that an effect may have succeeded; callers never replay.
			return failedProcessingResult(call, result), &ProcessingError{Cause: &agent.HookError{Phase: "tool_result", Name: h.Name, Cause: err}, EffectError: cause}
		}
		result = next
	}
	return result, cause
}

// failedProcessingResult retains system disposition without retaining business
// content, structured values, media or open metadata after failed processing.
func failedProcessingResult(call tool.Call, result tool.Result) tool.Result {
	return tool.Result{ID: call.ID, Name: call.Name, TurnComplete: result.TurnComplete, Content: []tool.ContentBlock{tool.Text("Tool completed; result processing failed. Do not repeat the operation.")}}
}

func InvokeTool(ctx context.Context, def tool.Definition, call tool.Call, base func(context.Context) (tool.Result, error)) (tool.Result, error) {
	e := toolEvidence(ctx, def, call)
	var actual *tool.Result
	var actualErr error
	chain := func(ctx context.Context) (tool.Result, error) {
		r, err := base(ctx)
		snapshot, _ := tool.CloneResult(r, nil)
		actual = &snapshot
		actualErr = err
		return r, err
	}
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
	if err != nil && actual != nil && (!errors.Is(err, actualErr) || !errors.Is(actualErr, err)) {
		return failedProcessingResult(call, *actual), &ProcessingError{Cause: err, EffectError: actualErr}
	}
	if err == nil && len(entries) > 0 {
		if r.ID != "" && r.ID != call.ID || r.Name != "" && r.Name != call.Name || actual == nil && r.TurnComplete || actual != nil && (r.TurnComplete != actual.TurnComplete || !reflect.DeepEqual(r.Meta, actual.Meta) || !reflect.DeepEqual(r.Metadata, actual.Metadata)) {
			err = fmt.Errorf("middleware changed tool identity or completion")
		}
		if err == nil {
			err = validateResult(def, r, err)
		}
		if err != nil {
			err = &agent.HookError{Phase: "tool_invoke", Name: entries[0].Name, Cause: err}
			if actual != nil {
				return failedProcessingResult(call, *actual), &ProcessingError{Cause: err, EffectError: actualErr}
			}
			return tool.Result{}, err
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
				} else {
					err = validateModelViewParts(a.Content)
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

// ProcessingError separates a failed presentation from the producer's known outcome.
type ProcessingError struct {
	Cause, EffectError error
}

func (e *ProcessingError) Error() string {
	var h *agent.HookError
	if errors.As(e.Cause, &h) {
		return fmt.Sprintf("hook %s/%s: result processing failed; effect outcome retained", h.Phase, h.Name)
	}
	return "result processing failed; effect outcome retained"
}
func (e *ProcessingError) Unwrap() error   { return e.Cause }
func (e *ProcessingError) Retryable() bool { return false }
func EffectResult(result tool.Result, err error) (tool.Result, error) {
	var e *ProcessingError
	if errors.As(err, &e) {
		return result, e.EffectError
	}
	return result, err
}
func FailedView(message model.Message) model.Message {
	message = model.CloneMessage(message)
	r := message.Parts[0].ToolResult
	r.Content = []model.Part{model.NewTextPart("Tool completed; model projection failed. Do not repeat the operation.")}
	return message
}

func ProcessingFailure(err error) *session.ToolProcessingFailure {
	var h *agent.HookError
	if errors.As(err, &h) {
		return &session.ToolProcessingFailure{Phase: h.Phase, Name: h.Name, Error: err.Error()}
	}
	return nil
}
