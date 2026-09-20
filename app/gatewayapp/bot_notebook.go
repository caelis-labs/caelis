package gatewayapp

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/control/bot"
)

func initializeBotNotebook(ctx context.Context, storeDir, id string) error {
	notebook, err := bot.NewNotebook(storeDir, id)
	if err != nil {
		return err
	}
	err = notebook.Init(ctx)
	return errors.Join(err, notebook.Close())
}

const botNotebookPolicy = "bot-notebook"

func notebookPolicyRegistry() (policy.Registry, error) {
	// Assembly admits only the notebook tools. Their rooted filesystem is the
	// mandatory authority ceiling, including reads, searches and symlinks.
	// Workspace policy's approval-based path widening does not apply here.
	return policy.NewMemory(policy.NamedMode{ID: botNotebookPolicy,
		Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
			return policy.Decision{Action: policy.ActionAllow}, nil
		},
	})
}
