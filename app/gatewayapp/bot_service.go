package gatewayapp

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

const botCreationDigest = "control_bot_creation_digest"

func isReservedBotCreation(req appserver.CreateSessionRequest) bool {
	return sessionvisibility.IsBotSession(session.Session{Metadata: req.Metadata}) ||
		sessionvisibility.IsBotWorkSession(session.Session{Metadata: req.Metadata}) ||
		strings.HasPrefix(strings.TrimSpace(req.PreferredSessionID), "bot-work-") ||
		req.Metadata[bot.MetadataID] != nil ||
		strings.HasPrefix(strings.TrimSpace(req.PreferredSessionID), "bot-chat-")
}

// Bots exposes the focused persistent Bot capability, not a Runtime lease.
func (s *Stack) Bots() appserver.BotService {
	if s == nil {
		return nil
	}
	return s.bots
}

func (b *controlCommandBackend) executeBotCommand(ctx context.Context, principal appserver.Principal, request any) (appserver.CommandResult, error) {
	b.botAdmissionMu.Lock()
	defer b.botAdmissionMu.Unlock()
	switch req := request.(type) {
	case appserver.CreateBotRequest:
		return b.createBot(ctx, principal, req)
	case appserver.UpdateBotRequest:
		return b.updateBot(ctx, req)
	default:
		return appserver.CommandResult{}, sessionConfigurationRejected("invalid Bot request")
	}
}

func (b *controlCommandBackend) createBot(ctx context.Context, principal appserver.Principal, req appserver.CreateBotRequest) (appserver.CommandResult, error) {
	intent, ok := appserver.OperationIntentFromContext(ctx)
	if !ok {
		return appserver.CommandResult{}, errors.New("gatewayapp: Bot creation intent unavailable")
	}
	id := bot.Identity(principal.ID, req.OperationID)
	sessionID, _ := bot.ConversationID(id)
	ref := session.SessionRef{SessionID: sessionID}
	service := &bot.Service{Sessions: b.composition.sessions}
	// Identity is permanently associated with the creation operation, beyond
	// the ledger's bounded terminal retention. Never replace an existing Bot.
	if existing, err := b.composition.sessions.Session(ctx, ref); err == nil {
		if existing.Metadata[botCreationDigest] != intent.Digest || existing.UserID != principal.ID {
			return appserver.CommandResult{}, appserver.NewOutcomeError(appserver.OutcomeConflicted, appserver.ErrOperationConflict)
		}
		value, readErr := service.GetBot(ctx, id)
		if readErr != nil {
			return botCommandResult(id, existing), appserver.NewOutcomeError(appserver.OutcomeUnknown, readErr)
		}
		return botCommandResult(id, session.Session{SessionRef: ref, Revision: value.Revision}), nil
	} else if !errors.Is(err, session.ErrSessionNotFound) {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	config, _, err := b.resolveBotConfig(ctx, req.Config, true)
	if err != nil {
		return appserver.CommandResult{}, sessionConfigurationRejectedError(err)
	}
	// Provision through the confined file owner, not ambient MkdirAll: even
	// a tampered Bot-directory symlink must not create files outside its root.
	if err := initializeBotFiles(ctx, b.composition.authorities.storeDir, id); err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	cwd := filepath.Join(b.composition.authorities.storeDir, "bots", id)
	workspace, err := canonicalWorkspaceRef(session.WorkspaceRef{Key: id, CWD: cwd}, session.WorkspaceRef{})
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	active, err := b.composition.sessions.StartSession(ctx, session.StartSessionRequest{
		AppName: b.composition.authorities.appName, UserID: principal.ID,
		PreferredSessionID: sessionID, Workspace: workspace,
		Controller: initialKernelControllerBinding("bot"),
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent: sessionvisibility.SystemManagedAgentBot,
			bot.MetadataID: id, botCreationDigest: intent.Digest,
		},
	})
	if err != nil {
		return botCommandResult(id, active), classifyControlBackendError(err)
	}
	// Bot creation deliberately does not admit Workspace Memory authority.
	updated, err := service.Save(ctx, active, id, config, nil, req.OperationID, intent.Digest)
	return botMutationResult(id, updated, err)
}

func (b *controlCommandBackend) updateBot(ctx context.Context, req appserver.UpdateBotRequest) (appserver.CommandResult, error) {
	service := &bot.Service{Sessions: b.composition.sessions}
	value, err := service.GetBot(ctx, req.BotID)
	if err != nil {
		return appserver.CommandResult{}, classifyControlPreDispatchError(err)
	}
	if value.SessionID != req.SessionID {
		return appserver.CommandResult{}, sessionConfigurationRejected("Bot and conversation do not match")
	}
	active, err := b.composition.checkControlCommandCAS(ctx, req.WriteBase)
	if err != nil {
		return botCommandResult(value.ID, active), classifyControlBackendError(err)
	}
	// A reserved Turn exists before the asynchronous producer takes its store
	// fence. Admission and this check share botAdmissionMu with Prompt.
	composition := b.composition
	if registry := b.runtimeRegistry(); registry != nil {
		runtime, release, loadErr := registry.acquireLoadedRuntime(active.SessionID)
		if loadErr != nil {
			return botCommandResult(value.ID, active), classifyControlPreDispatchError(loadErr)
		}
		if release != nil {
			defer release()
		}
		if runtime != nil {
			composition = &runtime.instance.runtimeComposition
		}
	}
	if err := composition.rejectSessionConfigurationDuringTurn(active.SessionID); err != nil {
		return botCommandResult(value.ID, active), sessionConfigurationConflict(err)
	}
	config, selected, err := b.resolveBotConfig(ctx, req.Config, false)
	if err != nil {
		return botCommandResult(value.ID, active), sessionConfigurationRejectedError(err)
	}
	var finishPin func(bool)
	if config.Model != "" && composition.activation != nil && composition.activation.modelCatalog != nil {
		finishPin, err = composition.beginPinnedModelSelection(ctx, selected)
		if err != nil {
			return botCommandResult(value.ID, active), sessionConfigurationRejectedError(err)
		}
	}
	intent, ok := appserver.OperationIntentFromContext(ctx)
	if !ok {
		if finishPin != nil {
			finishPin(false)
		}
		return appserver.CommandResult{}, errors.New("gatewayapp: Bot update intent unavailable")
	}
	updated, err := service.Save(ctx, active, value.ID, config, &value.Config, req.OperationID, intent.Digest)
	if finishPin != nil {
		finishPin(err == nil || session.IsCommitted(err))
	}
	if err == nil {
		if feed, feedErr := b.composition.authorities.controlFeeds.Session(active.SessionRef); feedErr == nil {
			publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), controlFeedPublishTimeout)
			_ = feed.Prime(publishCtx)
			cancel()
		}
	}
	return botMutationResult(value.ID, updated, err)
}

func (b *controlCommandBackend) resolveBotConfig(ctx context.Context, config bot.Config, creating bool) (bot.Config, ModelConfig, error) {
	config, err := bot.Normalize(config)
	if err != nil {
		return bot.Config{}, ModelConfig{}, err
	}
	if config.Model == "" && creating && b.composition.lookup != nil {
		config.Model = b.composition.lookup.DefaultID()
		config.Effort = b.composition.lookup.DefaultEffort()
		config.Fast = b.composition.lookup.DefaultFastMode()
	}
	// Identity creation and non-model edits remain available before a provider
	// is configured. Chat fails explicitly until a model is selected.
	if config.Model == "" {
		config.Effort, config.Fast = "", false
		return config, ModelConfig{}, nil
	}
	selected, found, err := b.composition.resolveSessionModelProfile(ctx, config.Model, config.Effort)
	if err != nil {
		return bot.Config{}, ModelConfig{}, err
	}
	if !found || selected.Profile.Kind() != modelprofile.BackendProvider {
		return bot.Config{}, ModelConfig{}, errorcode.New(errorcode.InvalidArgument, "bot: select an existing provider model")
	}
	if selected.Effort != "" && !modelConfigSupportsReasoningEffort(selected.Config, selected.Effort) {
		return bot.Config{}, ModelConfig{}, errorcode.New(errorcode.InvalidArgument, "bot: model does not support the selected reasoning effort")
	}
	if config.Fast && !modelconfig.SupportsSpeedMode(selected.Config, "fast") {
		return bot.Config{}, ModelConfig{}, errorcode.New(errorcode.InvalidArgument, "bot: model does not support fast mode")
	}
	config.Model, config.Effort = selected.Config.ID, selected.Effort
	return config, selected.Config, nil
}

func botMutationResult(id string, active session.Session, err error) (appserver.CommandResult, error) {
	result := botCommandResult(id, active)
	if session.IsCommitted(err) {
		result.Detail = "Bot configuration committed; refresh settings before another edit."
	}
	return result, classifyControlBackendError(err)
}

func botCommandResult(id string, active session.Session) appserver.CommandResult {
	result := sessionCommandResult(active)
	result.Resource = &appserver.CommandResource{Kind: appserver.CommandResourceBot, Ref: id}
	return result
}

func (b *controlCommandBackend) recoverBotCreation(ctx context.Context, principal appserver.Principal, intent appserver.OperationIntent) (appserver.CommandResult, bool, error) {
	id := bot.Identity(strings.TrimSpace(principal.ID), intent.OperationID)
	sessionID, _ := bot.ConversationID(id)
	loaded, err := b.composition.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: sessionID}, Limit: 1})
	if errors.Is(err, session.ErrSessionNotFound) {
		return appserver.CommandResult{}, false, nil
	}
	if err != nil {
		return appserver.CommandResult{}, false, err
	}
	if loaded.Session.Metadata[botCreationDigest] != intent.Digest || loaded.Session.UserID != principal.ID {
		return appserver.CommandResult{}, false, appserver.ErrOperationConflict
	}
	if _, ok := loaded.State[bot.StateKey]; !ok {
		return appserver.CommandResult{}, false, nil
	}
	if _, _, err := bot.ReadState(loaded.State); err != nil {
		return appserver.CommandResult{}, false, err
	}
	return botCommandResult(id, loaded.Session), true, nil
}
