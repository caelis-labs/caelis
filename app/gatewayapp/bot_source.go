package gatewayapp

import (
	"context"
	"errors"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
)

type botSourceContextKey struct{}
type botTurnAdmission struct {
	source bot.RequestSource
	ready  chan struct{}
}

func (b *controlCommandBackend) admitBotRequest(ctx context.Context, p appserver.Principal, active session.Session, req appserver.PromptRequest) (context.Context, *botTurnAdmission, error) {
	store := b.composition.authorities.botWork
	if store == nil {
		return ctx, nil, errors.New("gatewayapp: Bot authority store unavailable")
	}
	intent, ok := appserver.OperationIntentFromContext(ctx)
	if !ok {
		return ctx, nil, errors.New("gatewayapp: authenticated Bot request intent unavailable")
	}
	id, _ := active.Metadata[bot.MetadataID].(string)
	// The shared receipt can expire; the permanent source still fences model
	// dispatch. This runs under Bot admission serialization, before BeginTurn.
	previous, readErr := store.RequestForOperation(ctx, p.ID, id, req.OperationID)
	if readErr == nil {
		if previous.Digest != intent.Digest || previous.ClientID != p.ClientID {
			return ctx, nil, errorcode.New(errorcode.Conflict, "bot: request operation conflicts")
		}
		return ctx, nil, errorcode.New(errorcode.UnknownOutcome, "bot: request was already recorded; recover its source and native execution without replay")
	}
	if errorcode.CodeOf(readErr) != errorcode.NotFound {
		return ctx, nil, readErr
	}
	source, err := store.RecordRequest(ctx, p.ID, id, p.ClientID, req.OperationID, intent.Digest, req.Input, req.ContentParts...)
	if err != nil {
		return ctx, nil, err
	}
	admission := &botTurnAdmission{source: source, ready: make(chan struct{})}
	return context.WithValue(ctx, botSourceContextKey{}, admission), admission, nil
}

func (b *controlCommandBackend) finishBotRequest(ctx context.Context, admission *botTurnAdmission, result appserver.CommandResult) error {
	defer close(admission.ready)
	if result.Target.TurnID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := b.composition.authorities.botWork.BindRequest(ctx, admission.source, bot.Execution{
		InstanceID: b.composition.authorities.botWork.InstanceID, SessionID: result.SessionID,
		HandleID: result.Target.HandleID, RunID: result.Target.RunID, TurnID: result.Target.TurnID,
	})
	if err != nil {
		return err
	}
	return b.composition.authorities.botWork.PauseReports(ctx, admission.source.PrincipalID, admission.source.BotID, false)
}
