package runtime_test

import (
	"context"
	"fmt"
	"iter"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// greetingAgent is deliberately host-local: the supported SDK contracts do
// not require a bundled model provider, session store, or Agent factory.
type greetingAgent struct{}

func (greetingAgent) Name() string { return "greeting" }

func (greetingAgent) Run(ctx agent.Context) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, fmt.Sprintf(
			"Hello from Caelis. I received %d event.", ctx.Events().Len(),
		))
		yield(&session.Event{Type: session.EventTypeAssistant, Message: &message}, nil)
	}
}

func Example() {
	userMessage := model.NewTextMessage(model.RoleUser, "Say hello.")
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{
			AppName: "quickstart", UserID: "local-user", SessionID: "hello",
		}},
		Events: []*session.Event{{Type: session.EventTypeUser, Message: &userMessage}},
	})

	for event, err := range (greetingAgent{}).Run(ctx) {
		if err != nil {
			panic(err)
		}
		fmt.Println(session.EventText(event))
	}

	// Output:
	// Hello from Caelis. I received 1 event.
}
