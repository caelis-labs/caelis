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

func initializeBotFiles(ctx context.Context, storeDir, id string) error {
	files, err := bot.NewFiles(storeDir, id)
	if err != nil {
		return err
	}
	err = files.Init(ctx)
	return errors.Join(err, files.Close())
}

// admitBotPrompt provisions the private file area under the Bot admission lock.
// File contents and paths never grant execution or workspace authority.
func (b *controlCommandBackend) admitBotPrompt(ctx context.Context, active session.Session) error {
	id, _ := active.Metadata[bot.MetadataID].(string)
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("gatewayapp: Bot conversation is missing its identity")
	}
	files, err := bot.NewFiles(b.composition.authorities.storeDir, id)
	if err != nil {
		return err
	}
	err = files.Ensure(ctx)
	return errors.Join(err, files.Close())
}

const botPolicy = "bot"

func botPolicyRegistry() (policy.Registry, error) {
	// Each admitted file tool is protected by the private filesystem as the
	// mandatory authority ceiling, including reads, searches and symlinks.
	// Workspace policy's approval-based path widening does not apply here.
	return policy.NewMemory(policy.NamedMode{ID: botPolicy,
		Decide: func(_ context.Context, input policy.ToolContext) (policy.Decision, error) {
			switch input.Tool.Name {
			case "Read", "Write", "Patch", "Glob", "Grep", "ListWork", "ReadWork", "CreateWork", "ContinueWork", "SteerWork", "InterruptWork", "DesktopClock", "DesktopReminders", "DesktopGesture":
				return policy.Decision{Action: policy.ActionAllow}, nil
			default:
				return policy.Decision{Action: policy.ActionDeny, Reason: "tool is not admitted by the Bot capability policy"}, nil
			}
		},
	})
}
