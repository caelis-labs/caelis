package gatewaydriver

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	appservices "github.com/OnslaughtSnail/caelis/internal/app/services"
)

type connectWizardPayload = appservices.ConnectWizardState

func completeConnectArgs(ctx context.Context, driver *GatewayDriver, command string, query string, limit int) ([]SlashArgCandidate, error) {
	stack := stackForDriver(driver)
	switch {
	case command == "connect":
		candidates, handled, err := stack.ConnectProviderCandidates(ctx, query, limit)
		return requireConnectCandidates(candidates, handled, err, "provider candidates")
	case strings.HasPrefix(command, "connect-baseurl:"):
		candidates, handled, err := stack.ConnectBaseURLCandidates(ctx, strings.TrimPrefix(command, "connect-baseurl:"), query, limit)
		return requireConnectCandidates(candidates, handled, err, "base URL candidates")
	case strings.HasPrefix(command, "connect-timeout:"):
		candidates, handled, err := stack.ConnectTimeoutCandidates(ctx, query, limit)
		return requireConnectCandidates(candidates, handled, err, "timeout candidates")
	case strings.HasPrefix(command, "connect-apikey:"):
		return nil, nil
	case strings.HasPrefix(command, "connect-model:"):
		return completeConnectModels(ctx, driver, appservices.ParseConnectWizardPayload(strings.TrimPrefix(command, "connect-model:")), query, limit)
	case strings.HasPrefix(command, "connect-context:"):
		return completeConnectContext(ctx, driver, appservices.ParseConnectWizardPayload(strings.TrimPrefix(command, "connect-context:")), query, limit)
	case strings.HasPrefix(command, "connect-maxout:"):
		return completeConnectMaxOutput(ctx, driver, appservices.ParseConnectWizardPayload(strings.TrimPrefix(command, "connect-maxout:")), query, limit)
	case strings.HasPrefix(command, "connect-reasoning-levels:"):
		return completeConnectReasoningLevels(ctx, driver, appservices.ParseConnectWizardPayload(strings.TrimPrefix(command, "connect-reasoning-levels:")), query, limit)
	default:
		return nil, nil
	}
}

func requireConnectCandidates(candidates []SlashArgCandidate, handled bool, err error, dependency string) ([]SlashArgCandidate, error) {
	if err != nil {
		return nil, err
	}
	if !handled {
		return nil, fmt.Errorf("surfaces/tui/gatewaydriver: connect %s dependency is unavailable", dependency)
	}
	return candidates, nil
}

func completeConnectModels(ctx context.Context, driver *GatewayDriver, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	cfg, ok := connectModelConfigFromPayload(payload)
	if !ok {
		return nil, nil
	}
	candidates, handled, err := stackForDriver(driver).ConnectModelCandidates(ctx, cfg, query, limit)
	return requireConnectCandidates(candidates, handled, err, "model candidates")
}

func completeConnectContext(ctx context.Context, driver *GatewayDriver, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	defaults, err := connectDefaultsForPayload(ctx, stackForDriver(driver), payload)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: strconv.Itoa(defaults.ContextWindow), Display: strconv.Itoa(defaults.ContextWindow), Detail: "context window tokens"}}, query, limit), nil
}

func completeConnectMaxOutput(ctx context.Context, driver *GatewayDriver, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	defaults, err := connectDefaultsForPayload(ctx, stackForDriver(driver), payload)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: strconv.Itoa(defaults.MaxOutput), Display: strconv.Itoa(defaults.MaxOutput), Detail: "max output tokens"}}, query, limit), nil
}

func completeConnectReasoningLevels(ctx context.Context, driver *GatewayDriver, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	defaults, err := connectDefaultsForPayload(ctx, stackForDriver(driver), payload)
	if err != nil {
		return nil, err
	}
	value := "-"
	detail := "no reasoning levels"
	if len(defaults.ReasoningLevels) > 0 {
		value = strings.Join(defaults.ReasoningLevels, ",")
		detail = "suggested reasoning levels"
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: value, Display: value, Detail: detail}}, query, limit), nil
}

func connectDefaultsForPayload(ctx context.Context, stack *DriverStack, payload connectWizardPayload) (appservices.ConnectModelDefaults, error) {
	cfg, ok := connectModelConfigFromPayload(payload)
	if !ok {
		return appservices.ConnectModelDefaults{}, nil
	}
	defaults, handled, err := stack.ConnectDefaults(ctx, cfg)
	if err != nil {
		return appservices.ConnectModelDefaults{}, err
	}
	if !handled {
		return appservices.ConnectModelDefaults{}, fmt.Errorf("surfaces/tui/gatewaydriver: connect defaults dependency is unavailable")
	}
	return defaults, nil
}

func filterSlashArgCandidates(candidates []SlashArgCandidate, query string, limit int) []SlashArgCandidate {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]SlashArgCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if query != "" && !hasConnectCandidatePrefix(query, candidate.Value, candidate.Display, candidate.Detail) {
			continue
		}
		out = append(out, candidate)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func hasConnectCandidatePrefix(query string, values ...string) bool {
	if query == "" {
		return true
	}
	for _, value := range values {
		normalized := strings.ToLower(strings.TrimSpace(value))
		if normalized == "" {
			continue
		}
		if strings.HasPrefix(normalized, query) {
			return true
		}
	}
	return false
}

func normalizedConnectBaseURL(baseURL string) string {
	return appservices.NormalizeConnectBaseURL(baseURL)
}

func connectModelConfigFromPayload(payload connectWizardPayload) (ModelConfig, bool) {
	cfg, ok := appservices.ConnectModelConfigFromWizardState(payload)
	if !ok {
		return ModelConfig{}, false
	}
	return modelConfigFromApp(cfg), true
}

func stackForDriver(driver *GatewayDriver) *DriverStack {
	if driver == nil {
		return nil
	}
	return driver.stack
}

func reasoningLevelsForModel(stack *DriverStack, provider string, modelName string) []string {
	if stack == nil {
		return nil
	}
	return stack.ReasoningLevelsForModel(provider, modelName)
}

func compactNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func normalizeReasoningLevels(levels []string) []string {
	if len(levels) == 0 {
		return nil
	}
	out := make([]string, 0, len(levels))
	seen := map[string]struct{}{}
	for _, level := range levels {
		trimmed := strings.ToLower(strings.TrimSpace(level))
		if trimmed == "" || trimmed == "-" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}
