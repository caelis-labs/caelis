package controladapter

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func completeConnectACPInstall(ctx context.Context, agentID string) ([]controlprompt.SlashArgCandidate, error) {
	agent, ok := agentregistry.LookupConnectableAgent(agentID)
	if !ok || agent.Installation == nil {
		return nil, errors.New("this agent requires manual setup")
	}
	setup, err := agent.Installation.Plan(ctx)
	if err != nil {
		return nil, errorcode.New(errorcode.InvalidArgument, err.Error())
	}
	return []controlprompt.SlashArgCandidate{{Value: "confirm", Display: "Install and continue", RuntimeSetup: &setup}}, nil
}
