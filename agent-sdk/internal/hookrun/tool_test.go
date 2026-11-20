package hookrun

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestInvokeProducerFailureRemainsOriginal(t *testing.T) {
	for _, around := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-middleware", true: "passthrough"}[around], func(t *testing.T) {
			original := errors.New("producer persistence failed")
			hooks := agent.Hooks{}
			if around {
				hooks.ToolInvoke = []agent.ToolInvokeHook{{Name: "pass", Invoke: func(ctx context.Context, _ agent.HookContext, next agent.ToolNext) (tool.Result, error) {
					return next(ctx)
				}}}
			}
			ctx := WithRun(t.Context(), hooks, agent.HookContext{})
			calls := 0
			result, err := InvokeTool(ctx, tool.Definition{Name: "Effect"}, tool.Call{ID: "one", Name: "Effect"}, func(context.Context) (tool.Result, error) {
				calls++
				return tool.Result{Content: []tool.ContentBlock{tool.Text("original failure")}}, original
			})
			var processing *ProcessingError
			if !errors.Is(err, original) || errors.As(err, &processing) || !strings.Contains(err.Error(), original.Error()) || len(result.Content) != 1 || result.Content[0] != tool.Text("original failure") || calls != 1 {
				t.Fatalf("producer failure hidden: calls=%d result=%+v error=%v", calls, result, err)
			}
		})
	}
}

func TestFullResultRejectsDuplicateBusinessBlocksWithoutLosingEffect(t *testing.T) {
	r := tool.Result{Content: []tool.ContentBlock{
		tool.StructuredContent{Value: json.RawMessage(`1`)},
		tool.StructuredContent{Value: json.RawMessage(`2`)},
	}}
	r, err := FullResult(t.Context(), tool.Definition{}, tool.Call{}, r, nil)
	var processing *ProcessingError
	if !errors.As(err, &processing) || processing.EffectError != nil || len(r.Content) != 1 || r.Content[0].ContentType() != tool.ContentTypeText {
		t.Fatalf("invalid result did not become a processing failure with a known effect: %+v, %v", r, err)
	}
}

func TestInvokeInvalidPostprocessingRetainsKnownEffect(t *testing.T) {
	hooks := agent.Hooks{ToolInvoke: []agent.ToolInvokeHook{{Name: "forged-flag", Invoke: func(ctx context.Context, _ agent.HookContext, next agent.ToolNext) (tool.Result, error) {
		r, e := next(ctx)
		r.TurnComplete = true
		return r, e
	}}}}
	calls := 0
	r, err := InvokeTool(WithRun(t.Context(), hooks, agent.HookContext{}), tool.Definition{Name: "Effect"}, tool.Call{ID: "one", Name: "Effect"}, func(context.Context) (tool.Result, error) {
		calls++
		return tool.Result{Content: []tool.ContentBlock{tool.Text("known committed business value")}}, nil
	})
	var phase *agent.HookError
	var processing *ProcessingError
	actual, actualErr := EffectResult(r, err)
	if !errors.As(err, &phase) || phase.Phase != "tool_invoke" || phase.Name != "forged-flag" || !errors.As(err, &processing) || actualErr != nil || actual.TurnComplete || calls != 1 || len(actual.Content) != 1 || strings.Contains(string(actual.Content[0].(tool.Text)), "known committed business value") {
		t.Fatalf("known effect lost: calls=%d result=%+v err=%v", calls, r, err)
	}
}

func TestInvokeCannotSuppressProducerFailure(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.New("authority revoked"), errors.New("store write failed"), tool.FailedReply("tool reported failure")} {
		t.Run(cause.Error(), func(t *testing.T) {
			hooks := agent.Hooks{ToolInvoke: []agent.ToolInvokeHook{{Name: "suppress", Invoke: func(ctx context.Context, _ agent.HookContext, next agent.ToolNext) (tool.Result, error) {
				r, _ := next(ctx)
				return r, nil
			}}}}
			calls := 0
			r, err := InvokeTool(WithRun(t.Context(), hooks, agent.HookContext{}), tool.Definition{Name: "Effect"}, tool.Call{ID: "one", Name: "Effect"}, func(context.Context) (tool.Result, error) { calls++; return tool.Result{}, cause })
			var phase *agent.HookError
			_, effectErr := EffectResult(r, err)
			if !errors.Is(err, cause) || !errors.As(err, &phase) || phase.Name != "suppress" || !errors.Is(effectErr, cause) || calls != 1 {
				t.Fatalf("producer failure suppressed: calls=%d err=%v effectErr=%v", calls, err, effectErr)
			}
		})
	}
}
