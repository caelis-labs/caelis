package chat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/internal/modelinvocation"
	"github.com/caelis-labs/caelis/agent-sdk/internal/runtimeinput"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
)

func cloneResponse(r model.Response) model.Response {
	r.Message = model.CloneMessage(r.Message)
	return r
}
func cloneModelPreparation(p agent.ModelPreparation) agent.ModelPreparation {
	p.Request = model.CloneRequest(p.Request)
	if p.Response != nil {
		r := cloneResponse(*p.Response)
		p.Response = &r
	}
	if p.ContextEdit != nil {
		edit := *p.ContextEdit
		edit.Append = model.CloneParts(edit.Append)
		edit.ReplacePrefix = model.CloneMessages(edit.ReplacePrefix)
		if p.ContextEdit.ReplacePrefix != nil && edit.ReplacePrefix == nil {
			edit.ReplacePrefix = []model.Message{}
		}
		p.ContextEdit = &edit
	}
	return p
}

func (a *Agent) prepareModel(ctx agent.Context, e agent.HookContext, req *model.Request) (*model.Request, *model.Response, error) {
	p := agent.ModelPreparation{Request: req}
	editor, canEdit := ctx.(runtimeinput.ContextEditor)
	if canEdit && len(hookrun.FromContext(ctx).Hooks.ModelPrepare) > 0 {
		var err error
		e.ContextSource, err = editor.ContextEditSource()
		if err != nil {
			return nil, nil, err
		}
	}
	for _, h := range hookrun.FromContext(ctx).Hooks.ModelPrepare {
		next, err := hookrun.Call(ctx, "model_prepare", h, e, p, cloneModelPreparation, cloneModelPreparation)
		if err == nil && next.Request == nil {
			err = fmt.Errorf("model preparation requires a request")
		}
		if err == nil {
			// Hooks can edit this request. Only the unchanged configured prefix
			// retains its provenance, even if a later hook restores old text.
			count := p.Request.BaseInstructionCount
			next.Request.BaseInstructionCount = 0
			if count > 0 && len(next.Request.Instructions) >= count && reflect.DeepEqual(next.Request.Instructions[:count], p.Request.Instructions[:count]) {
				next.Request.BaseInstructionCount = count
			}
		}
		if err == nil && !reflect.DeepEqual(next.Request.Messages, req.Messages) {
			err = fmt.Errorf("messages require durable ContextEdit")
		}
		if err == nil && !validToolPresentation(req.Tools, next.Request.Tools) {
			err = fmt.Errorf("tool names and schema must match configured bindings")
		}
		if err == nil && next.ModelName != "" {
			if selected := a.models[next.ModelName]; selected != nil {
				a.model = selected
			} else if next.ModelName != a.model.Name() {
				err = fmt.Errorf("model selection %q has no admitted configuration binding", next.ModelName)
			}
		}
		if err == nil && next.Response != nil && (next.Response.Message.Role != model.RoleAssistant || len(next.Response.Message.ToolResults()) > 0) {
			err = fmt.Errorf("cached response requires assistant content or tool proposals")
		}
		if err == nil && next.ContextEdit != nil && a.resolveModelRequest != nil && a.validateModelRequest == nil {
			err = fmt.Errorf("dynamic ContextEdit requires the configuration Validate guard")
		}
		if err == nil && next.ContextEdit != nil {
			if !hookrun.FromContext(ctx).RequestEdited(e.LogicalRequestID, h.Name) {
				if !canEdit {
					err = fmt.Errorf("ContextEdit requires the Runtime checkpoint commit capability")
				} else {
					var projected []*session.Event
					projected, err = editor.CommitContextEdit(e.LogicalRequestID, h.Name, *next.ContextEdit, a.validateModelRequest)
					if err == nil {
						next.Request.Messages, err = messagesFromEventsChecked(ctx.Session(), projected)
						req.Messages = model.CloneMessages(next.Request.Messages)
						if err == nil {
							a.summaryID = ""
							if len(projected) > 0 {
								a.summaryID = promptSummaryID(projected[0])
							}
							hookrun.FromContext(ctx).RecordRequestEdit(e.LogicalRequestID, h.Name)
							e.ContextSource, err = editor.ContextEditSource()
						}
					}
				}
			}
			next.ContextEdit = nil
		}
		if err != nil {
			return nil, nil, &agent.HookError{Phase: "model_prepare", Name: h.Name, Cause: err}
		}
		p = next
	}
	// ModelPrepare may select a different model. Apply the native image tool's
	// presentation rule only after the model and tool presentation are final.
	if toolbinding.HideViewImage(a.toolsByName[filesystem.ViewImageToolName], a.model) {
		p.Request.Tools = slices.DeleteFunc(p.Request.Tools, func(spec model.ToolSpec) bool {
			return spec.Function != nil && spec.Function.Name == filesystem.ViewImageToolName
		})
	}
	if err := model.ValidateRequestCapabilities(a.model, p.Request); err != nil {
		return nil, nil, err
	}
	if p.Response != nil {
		if a.validateModelRequest != nil {
			release, err := a.validateModelRequest(ctx)
			if err != nil {
				return nil, nil, err
			}
			if release != nil {
				release()
			}
		}
		// Cached content cannot claim provider work or modify usage accounting.
		r := cloneResponse(*p.Response)
		r.Usage = model.Usage{}
		r.InvocationID = ""
		r.Provider = ""
		r.Model = ""
		r.ContextWindowTokens = modelContextWindowTokens(a.model)
		r.FinishReasonReported = false
		r.RawFinishReason = ""
		p.Response = &r
	}
	return p.Request, p.Response, nil
}

func validToolPresentation(configured, selected []model.ToolSpec) bool {
	used := make([]bool, len(configured))
	for _, want := range selected {
		found := false
		for i, have := range configured {
			if used[i] {
				continue
			}
			a, b := model.CloneToolSpecs([]model.ToolSpec{have})[0], model.CloneToolSpecs([]model.ToolSpec{want})[0]
			if a.Function != nil && b.Function != nil {
				a.Function.Description = ""
				b.Function.Description = ""
			}
			if reflect.DeepEqual(a, b) {
				found = true
				used[i] = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func resultModel(ctx agent.Context, e agent.HookContext, r *model.Response) (*model.Response, error) {
	for _, h := range hookrun.FromContext(ctx).Hooks.ModelResult {
		next, err := hookrun.Call(ctx, "model_result", h, e, *r, cloneResponse, cloneResponse)
		if err != nil {
			return nil, err
		}
		if next.Message.Role != model.RoleAssistant || len(next.Message.ToolResults()) > 0 {
			return nil, &agent.HookError{Phase: "model_result", Name: h.Name, Cause: fmt.Errorf("model result requires assistant content or tool proposals")}
		}
		// Only the semantic Message is editable. Original actual accounting and
		// provider disposition survive arbitrary response replacement.
		original := *r
		original.Message = next.Message
		r = &original
	}
	if r.Message.Role != model.RoleAssistant || len(r.Message.ToolResults()) > 0 {
		return nil, fmt.Errorf("model result requires assistant role")
	}
	return r, nil
}

func modelHookContext(ctx context.Context, e agent.HookContext) context.Context {
	ctx = hookrun.WithRequest(ctx, e)
	entries := hookrun.FromContext(ctx).Hooks.ModelInvoke
	if len(entries) == 0 && hookrun.FromContext(ctx).Trace.TraceSink == nil {
		return ctx
	}
	var ordinal atomic.Uint64
	return context.WithValue(ctx, modelinvocation.MiddlewareKey{}, func(ctx context.Context, in model.Invocation, req *model.Request, next func(context.Context) error) error {
		e.InvocationID = in.ID
		e.AttemptOrdinal = ordinal.Add(1)
		chain := next
		for i := len(entries) - 1; i >= 0; i-- {
			h := entries[i]
			following := chain
			chain = func(ctx context.Context) error {
				_, err := hookrun.Around(ctx, "model_invoke", h.Name, h.Timeout, func(ctx context.Context, next func(context.Context) (struct{}, error)) (struct{}, error) {
					var mu sync.Mutex
					var providerErr error
					err := h.Invoke(ctx, e, model.CloneRequest(req), func(ctx context.Context) error {
						_, err := next(ctx)
						mu.Lock()
						providerErr = err
						mu.Unlock()
						return err
					})
					var request *agent.RetryRequest
					if errors.As(err, &request) {
						mu.Lock()
						cause := providerErr
						mu.Unlock()
						if cause == nil || !errors.Is(request.Cause, cause) || !model.IsRetryableLLMError(cause) {
							return struct{}{}, fmt.Errorf("retry request cannot override the actual attempt outcome: %w", err)
						}
						return struct{}{}, cause
					}
					return struct{}{}, err
				}, func(ctx context.Context) (struct{}, error) { return struct{}{}, following(ctx) })
				return err
			}
		}
		return agent.TraceLifecycle(ctx, agent.LifecycleEvent{Operation: agent.LifecycleModel, SessionRef: e.SessionRef, RunID: e.RunID, TurnID: e.TurnID, StepID: e.InvocationID, Name: in.Model}, hookrun.FromContext(ctx).Trace, chain)
	})
}
