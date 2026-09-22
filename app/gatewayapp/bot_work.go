package gatewayapp

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func (b *controlCommandBackend) executeBotWork(ctx context.Context, p appserver.Principal, action appserver.Action, req appserver.BotWorkRequest) (result appserver.CommandResult, commandErr error) {
	b.botWorkAdmissionMu.Lock()
	defer b.botWorkAdmissionMu.Unlock()
	owner, err := (&bot.Service{Sessions: b.composition.sessions}).GetBot(ctx, req.BotID)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	active, err := b.composition.sessions.Session(ctx, session.SessionRef{SessionID: owner.SessionID})
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	if active.UserID != p.ID || owner.SessionID != req.SessionID || (!owner.Config.ManagedWork && action != appserver.ActionBotWorkCancel && action != appserver.ActionBotCompletionAck) {
		return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeRejected, appserver.ErrUnauthorized)
	}
	store := b.composition.authorities.botWork
	if store == nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(errors.New("bot: work store unavailable"))
	}
	if action == appserver.ActionBotCompletionAck {
		err := store.AcknowledgeCompletion(ctx, p.ID, req.BotID, req.WorkID)
		return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, SessionID: req.SessionID, Resource: &appserver.CommandResource{Kind: "bot_completion", Ref: req.WorkID}}, err
	}
	if p.ClientID != "" {
		if _, err := store.ActiveClient(ctx, p.ID, req.BotID, p.ClientID); err != nil {
			return appserver.CommandResult{}, classifyControlPreDispatchError(err)
		}
	}
	var source bot.RequestSource
	if action != appserver.ActionBotWorkCancel {
		source, err = store.Request(ctx, p.ID, req.BotID, req.SourceID)
		if err != nil {
			return appserver.CommandResult{}, classifyControlPreDispatchError(err)
		}
		if p.ClientID != "" && source.ClientID != p.ClientID {
			return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeRejected, appserver.ErrUnauthorized)
		}
		if source.Execution.TurnID == "" {
			return appserver.CommandResult{}, classifyControlPreDispatchError(errors.New("bot: source admission is unknown"))
		}
	}
	intent, ok := appserver.OperationIntentFromContext(ctx)
	if !ok {
		return appserver.CommandResult{}, classifyControlPreDispatchError(errors.New("bot: work operation intent unavailable"))
	}

	workID := req.WorkID
	if action == appserver.ActionBotWorkCreate {
		workID = bot.WorkID(req.BotID, req.OperationID)
	}
	anchor, fresh, anchorErr := store.ReserveOperation(ctx, bot.WorkOperation{PrincipalID: p.ID, BotID: req.BotID, WorkID: workID, OperationID: req.OperationID, SourceID: source.ID, Digest: intent.Digest})
	if anchorErr != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(anchorErr)
	}
	if !fresh {
		out := appserver.CommandResult{OperationID: req.OperationID, SessionID: workID, Outcome: appserver.Outcome(anchor.Outcome), Target: appserver.TurnTarget{HandleID: anchor.Execution.HandleID, RunID: anchor.Execution.RunID, TurnID: anchor.Execution.TurnID}, Resource: &appserver.CommandResource{Kind: "bot_work", Ref: workID}}
		if !out.Outcome.Valid() {
			out.Outcome = appserver.OutcomeUnknown
		}
		return out, nil
	}
	defer func() {
		anchor.Execution = bot.Execution{InstanceID: b.composition.authorities.botWork.InstanceID, SessionID: workID, HandleID: result.Target.HandleID, RunID: result.Target.RunID, TurnID: result.Target.TurnID}
		anchor.Outcome = string(result.Outcome)
		var classified *appserver.OutcomeError
		if errors.As(commandErr, &classified) && classified.Outcome.Valid() {
			anchor.Outcome = string(classified.Outcome)
		} else if !result.Outcome.Valid() {
			anchor.Outcome = string(appserver.OutcomeUnknown)
		}
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := store.CompleteOperation(saveCtx, anchor); err != nil {
			result.Outcome = appserver.OutcomeUnknown
			commandErr = errors.Join(commandErr, appserver.NewOutcomeError(appserver.OutcomeUnknown, err))
		}
	}()
	var work bot.Work
	if action == appserver.ActionBotWorkCreate {
		id := bot.WorkID(req.BotID, req.OperationID)
		work = bot.Work{ID: id, BotID: req.BotID, PrincipalID: p.ID, ClientID: source.ClientID, SessionID: id, WorkspaceKey: id, SourceID: req.SourceID, Assignment: req.Assignment, CreationOperationID: req.OperationID, CreationDigest: intent.Digest, Config: owner.Config, Status: "reserved"}
		fresh, reserveErr := store.ReserveWork(ctx, work)
		if reserveErr != nil {
			return appserver.CommandResult{}, classifyControlPreDispatchError(reserveErr)
		}
		if !fresh {
			old, readErr := store.GetWork(ctx, p.ID, req.BotID, id)
			if readErr != nil {
				return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeUnknown, readErr)
			}
			return workCommandResult(old, req.OperationID), nil
		}
		files, createErr := bot.NewWorkFiles(b.composition.authorities.storeDir, req.BotID, id)
		if createErr != nil {
			return workCommandResult(work, req.OperationID), appserver.NewOutcomeError(appserver.OutcomeUnknown, createErr)
		}
		createErr = files.Init(ctx)
		cwd, cwdErr := files.FileSystem().Getwd()
		createErr = errors.Join(createErr, cwdErr, files.Close())
		if createErr != nil {
			return workCommandResult(work, req.OperationID), appserver.NewOutcomeError(appserver.OutcomeUnknown, createErr)
		}
		_, createErr = b.composition.sessions.StartSession(ctx, session.StartSessionRequest{
			AppName: b.composition.authorities.appName, UserID: p.ID, PreferredSessionID: id, Workspace: session.WorkspaceRef{Key: id, CWD: cwd}, Title: req.Assignment,
			Controller: initialKernelControllerBinding("bot-work"), Metadata: map[string]any{sessionvisibility.MetadataSystemManagedAgent: sessionvisibility.SystemManagedAgentBotWork, sessionvisibility.MetadataSystemManagedParent: owner.SessionID, bot.MetadataID: owner.ID},
		})
		if createErr != nil {
			return workCommandResult(work, req.OperationID), appserver.NewOutcomeError(appserver.OutcomeUnknown, createErr)
		}
	} else {
		work, err = store.GetWork(ctx, p.ID, req.BotID, req.WorkID)
		if err != nil {
			return appserver.CommandResult{}, classifyControlPreDispatchError(err)
		}
	}
	registry := b.runtimeRegistry()
	if registry == nil {
		return workCommandResult(work, req.OperationID), appserver.NewOutcomeError(appserver.OutcomeUnknown, errors.New("bot: work Runtime registry unavailable"))
	}
	if action == appserver.ActionBotWorkSteer || action == appserver.ActionBotWorkCancel {
		if req.Target != work.Execution || req.Target.InstanceID != b.composition.authorities.botWork.InstanceID {
			return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("bot: work execution target is stale"))
		}
		runtime, release, loadErr := registry.acquireLoadedRuntime(work.SessionID)
		if loadErr != nil {
			return appserver.CommandResult{}, classifyControlPreDispatchError(loadErr)
		}
		if release != nil {
			defer release()
		}
		if runtime == nil {
			return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("bot: work execution is not active"))
		}
		base := appserver.WriteBase{OperationID: req.OperationID, SessionID: work.SessionID}
		target := appserver.TurnTarget{HandleID: req.Target.HandleID, RunID: req.Target.RunID, TurnID: req.Target.TurnID}
		if action == appserver.ActionBotWorkCancel {
			result, err := runtime.instance.executeControlCommand(ctx, p, appserver.ActionCancel, appserver.CancelRequest{WriteBase: base, Target: target, Reason: "Bot work explicitly interrupted"})
			result.Target, result.Resource = target, &appserver.CommandResource{Kind: "bot_work", Ref: work.ID}
			return result, err
		}
		// Only the authenticated user's source is admitted as steering. Assignment
		// text stays untrusted and is not promoted to a user message.
		result, err := runtime.instance.executeControlCommand(ctx, p, appserver.ActionSteer, appserver.SteerRequest{WriteBase: base, Target: target, Input: source.Text, ContentParts: source.ContentParts})
		result.Target, result.Resource = target, &appserver.CommandResource{Kind: "bot_work", Ref: work.ID}
		return result, err
	}
	runtime, _, release, _, err := registry.acquireActivatedControlRuntime(ctx, work.SessionID)
	if err != nil {
		return workCommandResult(work, req.OperationID), appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
	}
	defer func() { _ = release(context.Background()) }()
	composition := &runtime.instance.runtimeComposition
	if _, busy := composition.currentGateway().ActiveTurn(work.SessionID); busy {
		return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeConflicted, errors.New("bot: work is already active; use steer"))
	}
	current, err := composition.sessions.Session(ctx, session.SessionRef{SessionID: work.SessionID})
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	userMessage := model.MessageFromTextAndContentParts(model.RoleUser, source.Text, source.ContentParts)
	_, err = composition.sessions.AppendEvent(ctx, session.AppendEventRequest{SessionRef: current.SessionRef, ExpectedRevision: &current.Revision, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeConfiguration), Event: &session.Event{ID: "bot-source-" + req.OperationID, IdempotencyKey: "bot-source-" + req.OperationID, MessageID: source.ID, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Actor: session.ActorRef{Kind: session.ActorKindUser, ID: p.ID}, Message: &userMessage}})
	if err != nil {
		return workCommandResult(work, req.OperationID), appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
	}
	observer, retain := composition.controlTurnObserver(current.SessionRef)
	turn, err := composition.currentGateway().BeginTurn(ctx, kernel.BeginTurnRequest{SessionRef: current.SessionRef, RuntimeContext: composition.controlRuntimeContext(ctx, current), InputKind: kernel.SubmissionKindAgentCommunication, Input: req.Assignment, InputActor: session.ActorRef{Kind: session.ActorKindController, ID: owner.ID, Name: owner.Config.Name}, Surface: "bot-work", Observer: observer, Metadata: map[string]any{"operation_id": req.OperationID}})
	if turn.Handle == nil {
		retain()
	}
	if turn.Handle != nil {
		work.Execution = bot.Execution{InstanceID: b.composition.authorities.botWork.InstanceID, SessionID: work.SessionID, HandleID: turn.Handle.HandleID(), RunID: turn.Handle.RunID(), TurnID: turn.Handle.TurnID()}
		work.Status = "running"
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		saveErr := store.SaveWork(saveCtx, work)
		cancel()
		if saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}
	if turn.Handle != nil {
		b.retainBotWorkTurn(turn.Handle, retain, work)
	}
	out := workCommandResult(work, req.OperationID)
	if err != nil {
		return out, appserver.NewOutcomeError(appserver.OutcomeUnknown, err)
	}
	return out, nil
}

func workCommandResult(work bot.Work, operationID string) appserver.CommandResult {
	outcome := appserver.OutcomeAccepted
	if work.Execution.TurnID == "" {
		outcome = appserver.OutcomeUnknown
	}
	return appserver.CommandResult{OperationID: operationID, Outcome: outcome, SessionID: work.SessionID, Target: appserver.TurnTarget{HandleID: work.Execution.HandleID, RunID: work.Execution.RunID, TurnID: work.Execution.TurnID}, Resource: &appserver.CommandResource{Kind: "bot_work", Ref: work.ID}, Detail: strings.TrimSpace(work.Status)}
}

// BotWork exposes managed Bot work through the focused AppServer service.
func (s *Stack) BotWork() *appserver.BotWorkService {
	if s == nil {
		return nil
	}
	return &appserver.BotWorkService{Bots: s.bots, Store: s.composition.authorities.botWork, Commands: s.composition.authorities.botWorkCommands, Wake: s.composition.authorities.botReportReady}
}
