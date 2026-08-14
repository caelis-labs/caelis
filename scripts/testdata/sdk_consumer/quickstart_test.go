package consumer

import (
	"context"
	"fmt"
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type greetingAgent struct{}

func (greetingAgent) Name() string { return "greeting" }

func (greetingAgent) Run(ctx agent.Context) iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		message := model.NewTextMessage(model.RoleAssistant, fmt.Sprintf("received:%d", ctx.Events().Len()))
		yield(&session.Event{Type: session.EventTypeAssistant, Message: &message}, nil)
	}
}

func TestSupportedQuickstart(t *testing.T) {
	user := model.NewTextMessage(model.RoleUser, "hello")
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Session: session.Session{SessionRef: session.SessionRef{
			AppName: "consumer", UserID: "user", SessionID: "session",
		}},
		Events: []*session.Event{{Type: session.EventTypeUser, Message: &user}},
	})
	for event, err := range (greetingAgent{}).Run(ctx) {
		if err != nil {
			t.Fatal(err)
		}
		if got := session.EventText(event); got != "received:1" {
			t.Fatalf("assistant text = %q", got)
		}
		return
	}
	t.Fatal("agent produced no event")
}
