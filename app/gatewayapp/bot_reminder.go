package gatewayapp

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func (b *controlCommandBackend) fireBotReminder(ctx context.Context, p appserver.Principal, req appserver.BotReminderRequest) (appserver.CommandResult, error) {
	if p.ClientID == "" || p.BotID != req.BotID {
		return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeRejected, appserver.ErrUnauthorized)
	}
	store := b.composition.authorities.botWork
	client, err := store.ActiveClient(ctx, p.ID, p.BotID, p.ClientID)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	fire, err := store.QueueReminderOccurrence(ctx, client, req.GrantID, req.Version, req.Due)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	b.deliverBotActivity(ctx, p.ID, p.BotID)
	return appserver.CommandResult{Outcome: appserver.OutcomeAccepted, SessionID: req.SessionID, Resource: &appserver.CommandResource{Kind: "bot_reminder_fire", Ref: fire.ID}}, nil
}

func (b *controlCommandBackend) deliverBotActivity(ctx context.Context, principalID, botID string) {
	b.deliverBotReminder(ctx, principalID, botID)
	b.deliverBotReports(ctx, principalID, botID)
}

func (b *controlCommandBackend) deliverBotReminder(ctx context.Context, principalID, botID string) {
	b.botAdmissionMu.Lock()
	defer b.botAdmissionMu.Unlock()
	if b.composition.isClosing() {
		return
	}
	registry := b.runtimeRegistry()
	if registry == nil {
		return
	}
	id, err := bot.ConversationID(botID)
	if err != nil {
		return
	}
	loaded, releaseUse, err := registry.acquireLoadedRuntime(id)
	if err != nil {
		return
	}
	if loaded != nil {
		busy := loaded.instance.currentGateway().SessionRunning(id)
		releaseUse()
		if busy {
			return
		}
	}
	store := b.composition.authorities.botWork
	paused, pauseErr := store.ReportsPaused(ctx, principalID, botID)
	if pauseErr != nil || paused {
		return
	}
	pending, err := store.PendingReminders(ctx, principalID, botID)
	if err != nil || len(pending) == 0 {
		return
	}
	runtime, _, release, _, err := registry.acquireActivatedControlRuntime(ctx, id)
	if err != nil {
		return
	}
	defer func() { _ = release(context.Background()) }()
	composition := &runtime.instance.runtimeComposition
	if composition.currentGateway().SessionRunning(id) {
		return
	}
	for _, fire := range pending {
		grant, claimed, err := store.ClaimReminder(ctx, fire)
		if err != nil || !claimed {
			continue
		}
		source, err := store.ReminderRequest(ctx, grant, fire)
		if err != nil {
			return
		}
		admission := &botTurnAdmission{source: source, ready: make(chan struct{})}
		defer close(admission.ready)
		ref := session.SessionRef{SessionID: id}
		active, err := composition.sessions.Session(ctx, ref)
		if err != nil {
			return
		}
		observer, retain := composition.controlTurnObserver(ref)
		turn, err := composition.currentGateway().BeginTurn(context.WithValue(ctx, botSourceContextKey{}, admission), kernel.BeginTurnRequest{SessionRef: ref, RuntimeContext: composition.controlRuntimeContext(ctx, active), InputKind: kernel.SubmissionKindAgentCommunication, Input: grant.Arguments.Prompt, InputActor: session.ActorRef{Kind: session.ActorKindSystem, ID: grant.ID, Name: "Authorized resident reminder"}, Surface: "bot-reminder", Observer: observer})
		if turn.Handle == nil {
			retain()
			return
		}
		b.retainBotActivity(turn.Handle, retain, principalID, botID)
		if err != nil {
			return
		}
		target := bot.Execution{InstanceID: store.InstanceID, SessionID: id, HandleID: turn.Handle.HandleID(), RunID: turn.Handle.RunID(), TurnID: turn.Handle.TurnID()}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := store.BindRequest(saveCtx, source, target); err != nil {
			return
		}
		_ = store.AdmitReminder(saveCtx, fire, target)
		return
	}
}

func (b *controlCommandBackend) retainBotActivity(handle kernel.TurnHandle, release func(), principalID, botID string) {
	if handle == nil {
		release()
		return
	}
	go func() {
		defer release()
		_ = handle.WaitCompletion(context.Background())
		if b.composition.authorities.lifecycleCtx.Err() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b.deliverBotActivity(ctx, principalID, botID)
	}()
}
