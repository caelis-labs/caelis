package chat

import (
	"context"
	"fmt"
	"reflect"
	"sync/atomic"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/internal/hookrun"
	"github.com/caelis-labs/caelis/agent-sdk/model"
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
		p.ContextEdit = &edit
	}
	return p
}

func (a *Agent) prepareModel(ctx agent.Context, e agent.HookContext, req *model.Request) (*model.Request, *model.Response, error) {
	p := agent.ModelPreparation{Request: req}
	for _, h := range hookrun.FromContext(ctx).Hooks.ModelPrepare {
		next, err := hookrun.Call(ctx, "model_prepare", h, e, p, cloneModelPreparation, cloneModelPreparation)
		if err == nil && next.Request == nil {
			err = fmt.Errorf("model preparation requires a request")
		}
		if err == nil && !reflect.DeepEqual(next.Request.Messages, req.Messages) {
			err = fmt.Errorf("messages require durable ContextEdit")
		}
		if err == nil && !validToolPresentation(req.Tools, next.Request.Tools) {
			err = fmt.Errorf("tool names and schema must match configured bindings")
		}
		if err == nil && next.ModelName != "" && next.ModelName != a.model.Name() {
			selected := a.models[next.ModelName]
			if selected == nil {
				err = fmt.Errorf("model selection %q has no admitted configuration binding", next.ModelName)
			} else {
				a.model = selected
			}
		}
		if err == nil && next.ContextEdit != nil {
			err = fmt.Errorf("ContextEdit requires the Runtime checkpoint commit capability")
		}
		if err != nil {
			return nil, nil, &agent.HookError{Phase: "model_prepare", Name: h.Name, Cause: err}
		}
		p = next
	}
	if err := model.ValidateRequestCapabilities(a.model, p.Request); err != nil {
		return nil, nil, err
	}
	if p.Response != nil {
		// Cached content cannot claim provider work or modify usage accounting.
		r := cloneResponse(*p.Response)
		r.Usage = model.Usage{}
		r.InvocationID = ""
		r.Provider = ""
		r.Model = ""
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
		// Only the semantic Message is editable. Original actual accounting and
		// provider disposition survive arbitrary response replacement.
		original := *r
		original.Message = next.Message
		r = &original
	}
	if r.Message.Role != model.RoleAssistant {
		return nil, fmt.Errorf("model result requires assistant role")
	}
	return r, nil
}

func modelHookContext(ctx context.Context, e agent.HookContext) context.Context {
	entries := hookrun.FromContext(ctx).Hooks.ModelInvoke
	if len(entries) == 0 {
		return ctx
	}
	var ordinal atomic.Uint64
	return model.WithInvocationMiddleware(ctx, func(ctx context.Context, in model.Invocation, req *model.Request, next func(context.Context) error) error {
		e.InvocationID = in.ID
		e.AttemptOrdinal = ordinal.Add(1)
		chain := next
		for i := len(entries) - 1; i >= 0; i-- {
			h := entries[i]
			following := chain
			chain = func(ctx context.Context) error {
				_, err := hookrun.Around(ctx, "model_invoke", h.Name, h.Timeout, func(ctx context.Context, next func(context.Context) (struct{}, error)) (struct{}, error) {
					return struct{}{}, h.Invoke(ctx, e, model.CloneRequest(req), func(ctx context.Context) error { _, err := next(ctx); return err })
				}, func(ctx context.Context) (struct{}, error) { return struct{}{}, following(ctx) })
				return err
			}
		}
		return chain(ctx)
	})
}
