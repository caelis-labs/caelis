package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelconfig/credentialstore"
)

func (s *controlCommandBackend) executeHostModelConfigurationCommand(ctx context.Context, action appserver.Action, request any) (appserver.CommandResult, error) {
	var result hostModelMutationResult
	var err error
	switch req := request.(type) {
	case appserver.ConnectModelRequest:
		if action != appserver.ActionModelConnect {
			return configurationCommandResult(0), configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: connect request/action mismatch"),
			)
		}
		candidate, preflightErr := s.preflightHostModelConnect(ctx, req.ExpectedRevision)
		if preflightErr != nil {
			return configurationCommandResult(configurationErrorRevision(preflightErr, 0)), classifyHostModelMutationError(hostModelMutationResult{}, preflightErr)
		}
		releaseAuthentication, gateErr := s.beginHostModelAuthentication(req.Config.Provider)
		if gateErr != nil {
			return configurationCommandResult(expectedConfigurationRevision(req.ExpectedRevision)), classifyHostModelMutationError(hostModelMutationResult{}, gateErr)
		}
		if releaseAuthentication != nil {
			defer releaseAuthentication()
		}
		configs, authenticationStarted, assembleErr := s.assembleHostModelConnect(ctx, req.Config, candidate)
		if assembleErr != nil {
			revision := uint64(0)
			if req.ExpectedRevision != nil {
				revision = *req.ExpectedRevision
			}
			return configurationCommandResult(revision), classifyHostModelMutationError(
				hostModelMutationResult{Revision: revision, EffectStarted: authenticationStarted},
				assembleErr,
			)
		}
		result, err = s.connectModelsAtRevision(ctx, configs, req.ExpectedRevision)
		if authenticationStarted && err != nil {
			result.EffectStarted = true
		}
	case appserver.UseModelRequest:
		if action != appserver.ActionModelUse {
			return configurationCommandResult(0), configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: use-model request/action mismatch"),
			)
		}
		result, err = s.useHostModelAtRevision(ctx, req.Model, req.ReasoningEffort, req.ExpectedRevision)
	case appserver.DeleteModelRequest:
		if action != appserver.ActionModelDelete {
			return configurationCommandResult(0), configurationRejectedError(
				errorcode.New(errorcode.InvalidArgument, "gatewayapp: delete-model request/action mismatch"),
			)
		}
		result, err = s.deleteHostModelAtRevision(ctx, req.Model, req.ExpectedRevision)
	default:
		return configurationCommandResult(0), configurationRejectedError(
			errorcode.New(errorcode.InvalidArgument, "gatewayapp: invalid Host model configuration request"),
		)
	}
	commandResult := configurationCommandResult(result.Revision)
	if result.Warning != nil {
		commandResult.Detail = "model configuration committed; " + result.Warning.Error()
	}
	return commandResult, classifyHostModelMutationError(result, err)
}

func expectedConfigurationRevision(expected *uint64) uint64 {
	if expected == nil {
		return 0
	}
	return *expected
}

func (s *controlCommandBackend) preflightHostModelConnect(ctx context.Context, expected *uint64) (*modelLookup, error) {
	_, candidate, err := s.loadModelConfigurationCandidate(ctx, expected)
	return candidate, err
}

// beginHostModelAuthentication rejects overlapping interactive authentication
// in this Control Host. It deliberately does not create a cross-process lock or
// wait behind another user interaction.
func (s *controlCommandBackend) beginHostModelAuthentication(provider string) (func(), error) {
	template, ok := modelconfig.LookupProvider(provider)
	if !ok {
		return nil, nil
	}
	switch template.AuthFlow {
	case modelconfig.AuthFlowCodexOAuth, modelconfig.AuthFlowGrokOAuth:
	default:
		return nil, nil
	}
	if s == nil {
		return nil, errors.New("gatewayapp: stack is unavailable")
	}
	key := string(template.AuthFlow)
	s.hostAuthenticationMu.Lock()
	defer s.hostAuthenticationMu.Unlock()
	if s.hostAuthentications == nil {
		s.hostAuthentications = map[string]struct{}{}
	}
	if _, running := s.hostAuthentications[key]; running {
		return nil, errorcode.New(errorcode.Conflict, "gatewayapp: provider authentication is already in progress")
	}
	s.hostAuthentications[key] = struct{}{}
	return func() {
		s.hostAuthenticationMu.Lock()
		delete(s.hostAuthentications, key)
		s.hostAuthenticationMu.Unlock()
	}, nil
}

func (s *controlCommandBackend) assembleHostModelConnect(ctx context.Context, cfg appserver.ConnectConfig, candidate *modelLookup) ([]ModelConfig, bool, error) {
	_, ok := modelconfig.LookupProvider(cfg.Provider)
	if !ok {
		return nil, false, fmt.Errorf("gatewayapp: provider %q is not supported", strings.TrimSpace(cfg.Provider))
	}
	authenticationStarted := false
	reusableCredentialRef := ""
	authenticate := func(ctx context.Context, req modelconfig.AuthenticateRequest) error {
		authenticationStarted = true
		return s.authenticateModelProvider(ctx, req)
	}
	assembled, err := modelconfig.AssembleConnect(ctx, modelconfig.ConnectRequest{
		Provider:                       cfg.Provider,
		EndpointID:                     cfg.EndpointID,
		Models:                         hostConnectModelSelections(cfg),
		BaseURL:                        cfg.BaseURL,
		TimeoutSeconds:                 cfg.TimeoutSeconds,
		StreamFirstEventTimeoutSeconds: cfg.StreamFirstEventTimeoutSeconds,
		APIKey:                         cfg.APIKey,
		AuthType:                       cfg.AuthType,
	}, modelconfig.ConnectOptions{
		HasReusableAuth: func(ctx context.Context, provider, baseURL string) bool {
			var reusable bool
			reusableCredentialRef, reusable = s.composition.reusableProviderCredentialRef(ctx, candidate, provider, cfg.EndpointID, baseURL)
			return reusable
		},
		Authenticate: authenticate,
	})
	if err != nil {
		return nil, authenticationStarted, err
	}
	assembled, err = s.bindReusableAPIKeyCredential(ctx, assembled, reusableCredentialRef)
	return assembled, authenticationStarted, err
}

func (s *runtimeComposition) reusableProviderCredentialRef(ctx context.Context, lookup *modelLookup, provider, endpointID, baseURL string) (string, bool) {
	if s == nil || s.authorities.apiKeyCredentials == nil {
		return "", false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return "", false
	}
	provider = strings.TrimSpace(provider)
	baseURL = modelconfig.NormalizeBaseURL(baseURL)
	providerEndpointID := retainedProviderEndpointID(provider, endpointID, baseURL)
	if lookup != nil {
		lookup.mu.RLock()
		endpoint, foundEndpoint := lookup.providerEndpoints[strings.ToLower(providerEndpointID)]
		lookup.mu.RUnlock()
		if foundEndpoint {
			ref := strings.ToLower(strings.TrimSpace(endpoint.CredentialRef))
			if !strings.HasPrefix(ref, "apikey:") {
				return "", false
			}
			source, err := s.authorities.apiKeyCredentials.LookupSource(ctx, ref)
			return ref, err == nil && strings.TrimSpace(source.APIKey) != ""
		}
	}
	ref := credentialstore.BuildReference(provider, providerEndpointID)
	if source, err := s.authorities.apiKeyCredentials.LookupSource(ctx, ref); err == nil && strings.TrimSpace(source.APIKey) != "" {
		return ref, true
	}
	return "", false
}

func (s *controlCommandBackend) bindReusableAPIKeyCredential(ctx context.Context, configs []ModelConfig, reusableRef string) ([]ModelConfig, error) {
	if s == nil || s.composition.authorities.apiKeyCredentials == nil {
		return configs, nil
	}
	bound := append([]ModelConfig(nil), configs...)
	for index := range bound {
		configured := modelconfig.NormalizeConfig(bound[index])
		if strings.TrimSpace(configured.Token) != "" || strings.TrimSpace(configured.CredentialRef) != "" ||
			configured.AuthType == providers.AuthNone {
			continue
		}
		ref := strings.ToLower(strings.TrimSpace(reusableRef))
		if ref == "" {
			ref = credentialstore.BuildReference(configured.Provider, configured.ProviderEndpointID)
		}
		if ref == "" {
			continue
		}
		source, err := s.composition.authorities.apiKeyCredentials.LookupSource(ctx, ref)
		if err != nil {
			return nil, fmt.Errorf("gatewayapp: retained provider credential %q is unavailable: %w", ref, err)
		}
		if strings.TrimSpace(source.APIKey) == "" {
			return nil, fmt.Errorf("gatewayapp: retained provider credential %q is empty", ref)
		}
		bound[index].CredentialRef = ref
	}
	return bound, nil
}

func retainedAPIKeyReference(provider, endpointID, baseURL string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	return credentialstore.BuildReference(provider, retainedProviderEndpointID(provider, endpointID, baseURL))
}

func retainedProviderEndpointID(provider, endpointID, baseURL string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	api := modelconfig.DefaultAPIForProvider(provider)
	endpointID = modelconfig.NormalizeEndpointID(provider, endpointID, baseURL, api)
	return modelconfig.BuildProviderEndpointID(provider, endpointID, baseURL)
}

func hostConnectModelSelections(cfg appserver.ConnectConfig) []modelconfig.ModelSelection {
	names := strings.Split(cfg.Model, ",")
	selections := make([]modelconfig.ModelSelection, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		key := strings.ToLower(name)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		levels := append([]string(nil), cfg.ReasoningLevels...)
		if cfg.ReasoningLevels != nil && levels == nil {
			levels = []string{}
		}
		selections = append(selections, modelconfig.ModelSelection{
			Name:                name,
			ContextWindowTokens: cfg.ContextWindowTokens,
			MaxOutputTokens:     cfg.MaxOutputTokens,
			ReasoningEffort:     cfg.ReasoningEffort,
			ReasoningLevels:     levels,
			ImageInput:          cfg.ImageInput,
		})
	}
	return selections
}

func classifyHostModelMutationError(result hostModelMutationResult, err error) error {
	if err == nil {
		return nil
	}
	if !result.EffectStarted && errors.Is(err, configstore.ErrConfigurationRevisionConflict) {
		coded := errorcode.Wrap(errorcode.Conflict, "gatewayapp: configuration conflict", err)
		return appserver.NewOutcomeError(appserver.OutcomeConflicted, coded)
	}
	if !result.EffectStarted && errorcode.CodeOf(err) == errorcode.Conflict {
		return appserver.NewOutcomeError(appserver.OutcomeConflicted, err)
	}
	if !result.EffectStarted {
		if errorcode.CodeOf(err) == errorcode.Unknown {
			err = errorcode.Wrap(errorcode.FailedPrecondition, err.Error(), err)
		}
		return configurationRejectedError(err)
	}
	return appserver.NewOutcomeError(
		appserver.OutcomeUnknown,
		errorcode.Wrap(errorcode.UnknownOutcome, "gatewayapp: Host model effect outcome cannot be proven", err),
	)
}
