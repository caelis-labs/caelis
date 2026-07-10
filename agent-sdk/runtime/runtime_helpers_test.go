package runtime

import (
	"context"
	"iter"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func runnerError(handle agent.Runner) error {
	var out error
	for _, err := range handle.Events() {
		if err != nil {
			out = err
		}
	}
	return out
}

type scriptedTestModel struct {
	generate func(context.Context, *model.Request) *model.Response
}

func (scriptedTestModel) Name() string { return "scripted-test" }

func (scriptedTestModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, ParallelToolCalls: true}
}

func (m scriptedTestModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		if err := ctx.Err(); err != nil {
			yield(nil, err)
			return
		}
		yield(model.StreamEventFromResponse(m.generate(ctx, req)), nil)
	}
}

func toolCallResponse(id, name string) *model.Response {
	return &model.Response{
		Message:      model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: id, Name: name, Args: `{}`}}, ""),
		TurnComplete: true,
	}
}

func textResponse(text string, usage model.Usage) *model.Response {
	return &model.Response{
		Message:      model.NewTextMessage(model.RoleAssistant, text),
		TurnComplete: true,
		Usage:        usage,
	}
}
