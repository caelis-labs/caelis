package agentsdk_test

import (
	"fmt"

	agentsdk "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func ExampleNewContext() {
	message := model.NewTextMessage(model.RoleUser, "hello")
	ctx := agentsdk.NewContext(agentsdk.ContextSpec{
		Session: session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}},
		Events: []*session.Event{{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Message:    &message,
			Text:       "hello",
		}},
		State: map[string]any{"mode": "default"},
	})

	for event := range ctx.Events().All() {
		fmt.Printf("%s: %s\n", session.EventTypeOf(event), session.EventText(event))
	}

	// Output:
	// user: hello
}
