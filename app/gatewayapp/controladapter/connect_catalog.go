package controladapter

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/control/modelcatalog"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/ports/controlprompt/connectwizard"
)

type connectModelChoice struct {
	Name             string
	Display          string
	Detail           string
	MetadataComplete bool
}

type connectWizardPayload = connectwizard.ConnectWizardState

func completeConnectArgs(ctx context.Context, driver *Adapter, command string, query string, limit int) ([]SlashArgCandidate, error) {
	switch {
	case command == "connect":
		return completeConnectProviders(query, limit), nil
	case strings.HasPrefix(command, "connect-baseurl:"):
		return completeConnectBaseURL(ctx, driver, strings.TrimPrefix(command, "connect-baseurl:"), query, limit), nil
	case strings.HasPrefix(command, "connect-timeout:"):
		return completeConnectTimeout(strings.TrimPrefix(command, "connect-timeout:"), query, limit), nil
	case strings.HasPrefix(command, "connect-apikey:"):
		return nil, nil
	case strings.HasPrefix(command, "connect-model:"):
		return completeConnectModels(ctx, driver, connectwizard.ParseConnectWizardStatePayload(strings.TrimPrefix(command, "connect-model:")), query, limit)
	case strings.HasPrefix(command, "connect-context:"):
		return completeConnectContext(ctx, driver, connectwizard.ParseConnectWizardStatePayload(strings.TrimPrefix(command, "connect-context:")), query, limit)
	case strings.HasPrefix(command, "connect-maxout:"):
		return completeConnectMaxOutput(ctx, driver, connectwizard.ParseConnectWizardStatePayload(strings.TrimPrefix(command, "connect-maxout:")), query, limit)
	case strings.HasPrefix(command, "connect-reasoning-levels:"):
		return completeConnectReasoningLevels(ctx, driver, connectwizard.ParseConnectWizardStatePayload(strings.TrimPrefix(command, "connect-reasoning-levels:")), query, limit)
	default:
		return nil, nil
	}
}

func completeConnectProviders(query string, limit int) []SlashArgCandidate {
	templates := modelconfig.ProviderTemplates()
	out := make([]SlashArgCandidate, 0, len(templates))
	for _, template := range templates {
		if query != "" && !strings.Contains(strings.ToLower(template.Label+" "+template.DefaultBaseURL), strings.ToLower(strings.TrimSpace(query))) {
			continue
		}
		detailParts := []string{strings.TrimSpace(template.Description), strings.TrimSpace(template.DefaultBaseURL)}
		if template.AuthFlow != "" {
			detailParts = append(detailParts, "browser oauth")
		} else if template.NoAuthRequired {
			detailParts = append(detailParts, "no auth")
		} else if env := modelconfig.DefaultTokenEnv(template.Provider, template.DefaultBaseURL); env != "" {
			detailParts = append(detailParts, "env:"+env)
		}
		out = append(out, SlashArgCandidate{
			Value:   template.Label,
			Display: template.Label,
			Detail:  strings.Join(compactNonEmpty(detailParts), " · "),
			NoAuth:  template.NoAuthRequired,
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func completeConnectBaseURL(ctx context.Context, driver *Adapter, provider string, query string, limit int) []SlashArgCandidate {
	template, ok := modelconfig.LookupProvider(provider)
	if !ok {
		return nil
	}
	candidates := connectEndpointCandidates(template)
	if len(candidates) == 0 {
		candidates = append(candidates, SlashArgCandidate{Value: template.DefaultBaseURL, Display: template.DefaultBaseURL, Detail: "default base URL"})
	}
	for i := range candidates {
		if driver != nil && driver.hasReusableConnectAuth(ctx, template.Provider, candidates[i].Value) {
			candidates[i].NoAuth = true
			candidates[i].Detail = strings.Join(compactNonEmpty([]string{strings.TrimSpace(candidates[i].Detail), "configured auth"}), " · ")
		}
	}
	return filterSlashArgCandidates(candidates, query, limit)
}

func connectEndpointCandidates(template modelconfig.ProviderTemplate) []SlashArgCandidate {
	if len(template.Endpoints) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, 0, len(template.Endpoints))
	for _, endpoint := range template.Endpoints {
		detail := strings.TrimSpace(endpoint.Detail)
		if endpoint.TokenEnv != "" {
			detail = strings.Join(compactNonEmpty([]string{detail, "env:" + endpoint.TokenEnv}), " · ")
		}
		out = append(out, SlashArgCandidate{
			Value:   endpoint.BaseURL,
			Display: endpoint.Display,
			Detail:  detail,
		})
	}
	return out
}

func completeConnectTimeout(provider string, query string, limit int) []SlashArgCandidate {
	values := []string{"60", "120", "180"}
	out := make([]SlashArgCandidate, 0, len(values))
	for _, value := range values {
		out = append(out, SlashArgCandidate{Value: value, Display: value, Detail: fmt.Sprintf("%ss", value)})
	}
	_ = provider
	return filterSlashArgCandidates(out, query, limit)
}

func completeConnectModels(ctx context.Context, driver *Adapter, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	template, ok := modelconfig.LookupProvider(payload.Provider)
	if !ok {
		return nil, nil
	}
	var authenticate modelconfig.AuthenticateFunc
	if driver != nil && driver.stack != nil {
		authenticate = driver.stack.Model.AuthenticateFn
	}
	models, err := modelconfig.SelectableModels(ctx, template.Provider, payload.BaseURL, authenticate)
	if err != nil {
		return nil, err
	}
	choices := buildConnectModelChoices(template.Provider, models)
	out := make([]SlashArgCandidate, 0, len(choices))
	for _, choice := range choices {
		if query != "" && !strings.Contains(strings.ToLower(choice.Name+" "+choice.Display+" "+choice.Detail), strings.ToLower(strings.TrimSpace(query))) {
			continue
		}
		out = append(out, SlashArgCandidate{
			Value:                 choice.Name,
			Display:               choice.Display,
			Detail:                choice.Detail,
			ModelMetadataComplete: choice.MetadataComplete,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func completeConnectContext(ctx context.Context, driver *Adapter, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	_ = ctx
	_ = driver
	defaults, err := modelconfig.ResolveModelDefaults(payload.Provider, payload.Model)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: strconv.Itoa(defaults.ContextWindowTokens), Display: strconv.Itoa(defaults.ContextWindowTokens), Detail: "context window tokens"}}, query, limit), nil
}

func completeConnectMaxOutput(ctx context.Context, driver *Adapter, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	_ = ctx
	_ = driver
	defaults, err := modelconfig.ResolveModelDefaults(payload.Provider, payload.Model)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]SlashArgCandidate{{Value: strconv.Itoa(defaults.MaxOutputTokens), Display: strconv.Itoa(defaults.MaxOutputTokens), Detail: "max output tokens"}}, query, limit), nil
}

func completeConnectReasoningLevels(ctx context.Context, driver *Adapter, payload connectWizardPayload, query string, limit int) ([]SlashArgCandidate, error) {
	_ = ctx
	_ = driver
	defaults, err := modelconfig.ResolveModelDefaults(payload.Provider, payload.Model)
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

func buildConnectModelChoices(provider string, fallbackModels []modelconfig.SelectableModel) []connectModelChoice {
	seen := map[string]struct{}{}
	out := make([]connectModelChoice, 0, len(fallbackModels))
	add := func(modelChoice modelconfig.SelectableModel, detail string) {
		name := modelChoice.Name
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if strings.TrimSpace(detail) == "" {
			detail = describeConnectModel(provider, name)
		}
		out = append(out, connectModelChoice{
			Name:             name,
			Display:          connectDisplayModelRef(provider, name),
			Detail:           strings.TrimSpace(detail),
			MetadataComplete: modelChoice.MetadataComplete,
		})
	}
	for _, item := range fallbackModels {
		add(item, "")
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Display) < strings.ToLower(out[j].Display)
	})
	return out
}

func connectDisplayModelRef(provider, modelName string) string {
	provider = strings.TrimSpace(provider)
	modelName = strings.TrimSpace(modelName)
	if provider == "" {
		return modelName
	}
	if modelName == "" {
		return provider
	}
	if strings.HasPrefix(strings.ToLower(modelName), strings.ToLower(provider)+"/") {
		return modelName
	}
	return provider + "/" + modelName
}

func describeConnectModel(provider string, modelName string) string {
	caps, ok := modelcatalog.LookupModelCapabilities(provider, modelName)
	if !ok {
		return "suggested model"
	}
	parts := []string{"catalog preset"}
	if template, found := modelconfig.LookupProvider(provider); found && template.UseModelDirectory {
		parts[0] = "model directory"
	}
	if caps.SupportsReasoning {
		parts = append(parts, "reasoning")
	}
	if caps.SupportsToolCalls {
		parts = append(parts, "tools")
	}
	return strings.Join(parts, " · ")
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
