package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// These helpers seed low-level model state for package tests without
// reintroducing a production mutation surface beside the AppServer command
// capability. Product-path tests should prefer ConfigurationCommands.
func (s *Stack) connectTestModel(cfg ModelConfig) (modelprofile.ModelProfile, error) {
	profiles, err := s.connectTestModels([]ModelConfig{cfg})
	if err != nil {
		return modelprofile.ModelProfile{}, err
	}
	if len(profiles) == 0 {
		return modelprofile.ModelProfile{}, fmt.Errorf("gatewayapp: test connect produced no model profile")
	}
	return profiles[0], nil
}

func (s *Stack) connectTestModels(configs []ModelConfig) ([]modelprofile.ModelProfile, error) {
	result, err := s.connectModelsAtRevision(context.Background(), configs, nil)
	resultErr := errors.Join(err, result.Warning)
	doc, loadErr := s.composition.authorities.store.Load()
	if loadErr != nil {
		return nil, errors.Join(resultErr, loadErr)
	}
	profiles := make([]modelprofile.ModelProfile, 0, len(configs))
	for _, raw := range configs {
		configured := normalizeModelConfig(raw)
		current, ok := s.composition.lookup.Config(configured.ID)
		if !ok {
			current, ok = s.composition.lookup.Config(configured.Alias)
		}
		if !ok {
			continue
		}
		profile, ok := modelprofile.Lookup(doc.ModelProfiles, modelprofile.BuildProviderID(current.ID))
		if ok {
			profiles = append(profiles, profile)
		}
	}
	if len(profiles) == 0 && resultErr == nil {
		resultErr = fmt.Errorf("gatewayapp: test connect committed without observable model profiles")
	}
	return profiles, resultErr
}

func (s *Stack) useTestHostModel(ctx context.Context, ref session.SessionRef, alias string, reasoningEffort ...string) error {
	if strings.TrimSpace(ref.SessionID) != "" {
		return errors.New("gatewayapp: Host model selection must not address a Session")
	}
	reasoning := ""
	if len(reasoningEffort) > 0 {
		reasoning = strings.TrimSpace(reasoningEffort[0])
	}
	result, err := s.useHostModelAtRevision(ctx, alias, reasoning, nil)
	return errors.Join(err, result.Warning)
}

func (s *Stack) deleteTestHostModel(ctx context.Context, ref session.SessionRef, alias string) error {
	if strings.TrimSpace(ref.SessionID) != "" {
		return errors.New("gatewayapp: Host model deletion must not address a Session")
	}
	result, err := s.deleteHostModelAtRevision(ctx, alias, nil)
	return errors.Join(err, result.Warning)
}
