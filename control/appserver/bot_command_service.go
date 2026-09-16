package appserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// BotCreateTarget is the stable operation target for Host Bot creation. The
// Bot identity is derived from the principal and operation ID, so creation
// needs no per-name identity key.
const BotCreateTarget = "bot/create"

func (s *CommandService) CreateBot(ctx context.Context, principal Principal, req CreateBotRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionBotCreate, req.WriteBase, BotCreateTarget, req)
}

func (s *CommandService) UpdateBot(ctx context.Context, principal Principal, req UpdateBotRequest) (CommandResult, error) {
	return s.execute(ctx, principal, ActionBotUpdate, req.WriteBase, "bot/"+strings.TrimSpace(req.BotID), req)
}

// validateCreateBotCommandRequest rejects a Host Bot creation that addresses an
// existing resource. Creation has no current revision and must not carry a CAS
// expectation, exactly like Session creation.
func validateCreateBotCommandRequest(action Action, request CreateBotRequest) error {
	if action != ActionBotCreate {
		return fmt.Errorf("controlclient: unsupported Bot create action %q", action)
	}
	if strings.TrimSpace(request.SessionID) != "" {
		return errors.New("controlclient: Host Bot create must not address a Session")
	}
	if request.ExpectedRevision != nil {
		return errors.New("controlclient: Host Bot create must not carry an expected revision")
	}
	if strings.TrimSpace(request.ExpectedControllerEpoch) != "" {
		return errors.New("controlclient: Host Bot create must not address a controller epoch")
	}
	if strings.TrimSpace(request.Config.Name) == "" {
		return errors.New("controlclient: Bot name is required")
	}
	return nil
}

func validateUpdateBotCommandRequest(action Action, request UpdateBotRequest) error {
	if action != ActionBotUpdate {
		return fmt.Errorf("controlclient: unsupported Bot update action %q", action)
	}
	if err := requireSession(request.SessionID); err != nil {
		return err
	}
	if request.ExpectedRevision == nil {
		return errors.New("controlclient: Bot update expected_revision is required")
	}
	if strings.TrimSpace(request.BotID) == "" {
		return errors.New("controlclient: Bot id is required")
	}
	if strings.TrimSpace(request.Config.Name) == "" {
		return errors.New("controlclient: Bot name is required")
	}
	return nil
}

var _ BotCommandService = (*CommandService)(nil)
