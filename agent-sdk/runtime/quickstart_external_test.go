package runtime_test

import (
	"context"
	"fmt"
	"iter"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

// staticModel keeps the quickstart offline. Production hosts can replace it
// with any model.LLM and declare the capabilities they rely on.
type staticModel struct{}

func (staticModel) Name() string { return "static" }

func (staticModel) Capabilities() model.Capabilities { return model.Capabilities{} }

func (staticModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(model.StreamEventFromResponse(&model.Response{
			Message:      model.NewTextMessage(model.RoleAssistant, "Hello from Caelis."),
			Status:       model.ResponseStatusCompleted,
			FinishReason: model.FinishReasonStop,
			TurnComplete: true,
		}), nil)
	}
}

func ExampleRuntime_Run() {
	ctx := context.Background()
	sessions := inmemory.NewService(inmemory.NewStore(inmemory.Config{}))
	active, err := sessions.StartSession(ctx, session.StartSessionRequest{
		AppName:            "quickstart",
		UserID:             "local-user",
		PreferredSessionID: "hello",
	})
	if err != nil {
		panic(err)
	}

	rt, err := runtime.New(runtime.Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be concise."},
	})
	if err != nil {
		panic(err)
	}
	result, err := rt.Run(ctx, agent.RunRequest{
		SessionRef: active.SessionRef,
		Input:      "Say hello.",
		AgentSpec: agent.AgentSpec{
			Name:  "assistant",
			Model: staticModel{},
		},
	})
	if err != nil {
		panic(err)
	}
	defer result.Handle.Close()

	for event, eventErr := range result.Handle.Events() {
		if eventErr != nil {
			panic(eventErr)
		}
		if session.EventTypeOf(event) == session.EventTypeAssistant {
			fmt.Printf("assistant: %s\n", session.EventText(event))
		}
	}

	// Output:
	// assistant: Hello from Caelis.
}
