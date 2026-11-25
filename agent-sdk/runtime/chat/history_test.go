package chat

import (
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func messagesFromContext(ctx agent.Context) []model.Message {
	messages, _ := messagesFromContextChecked(ctx)
	return messages
}
