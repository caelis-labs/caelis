package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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

// admitBotPrompt is the Bot admission step the Control command backend runs
// while it holds botAdmissionMu, before dispatching a Turn. It gives a Bot
// created before notebooks were universal its private notebook exactly once
// through the same confined notebook owner used at creation: the notebook root's
// presence makes the step idempotent, so an existing notebook is never replaced
// and a deleted index.md is never recreated. A failure fails the prompt
// explicitly instead of running a Turn whose tools cannot reach a notebook.
func (b *controlCommandBackend) admitBotPrompt(ctx context.Context, active session.Session) error {
	id, _ := active.Metadata[bot.MetadataID].(string)
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("gatewayapp: Bot conversation is missing its identity")
	}
	notebook, err := bot.NewNotebook(b.composition.authorities.storeDir, id)
	if err != nil {
		return err
	}
	err = notebook.ProvisionInitial(ctx)
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
