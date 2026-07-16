package controladapter

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	controlagents "github.com/caelis-labs/caelis/control/agents"
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
		return completeConnectSources(ctx, driver, query, limit), nil
	case command == "connect-provider":
		return completeConnectProviders(query, limit), nil
	case command == "connect-disconnect-agent":
		return completeConnectDisconnectAgents(ctx, driver, query, limit)
	case strings.HasPrefix(command, "connect-disconnect-confirm:"):
		return completeConnectDisconnectConfirmation(ctx, driver, strings.TrimPrefix(command, "connect-disconnect-confirm:"), query, limit)
	case command == "connect-acp-agent":
		return completeConnectACPAgents(query, limit), nil
	case strings.HasPrefix(command, "connect-acp-launcher:"):
		return completeConnectACPLaunchers(strings.TrimPrefix(command, "connect-acp-launcher:"), query, limit), nil
	case strings.HasPrefix(command, "connect-acp-model:"):
		return completeConnectACPModels(ctx, driver, strings.TrimPrefix(command, "connect-acp-model:"), query, limit)
	case strings.HasPrefix(command, "connect-acp-config:"):
		return completeConnectACPConfig(ctx, driver, strings.TrimPrefix(command, "connect-acp-config:"), query, limit)
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

func completeConnectSources(ctx context.Context, driver *Adapter, query string, limit int) []SlashArgCandidate {
	candidates := []SlashArgCandidate{
		{Value: "model", Display: "Model provider", Detail: "Connect an API or local model provider"},
		{Value: "acp", Display: "Local ACP Agent", Detail: "Connect Codex, Claude, or another local ACP command"},
	}
	if driver != nil {
		if connected, err := driver.DisconnectCandidates(ctx); err == nil && len(connected) > 0 {
			candidates = append(candidates, SlashArgCandidate{
				Value: "disconnect", Display: "Disconnect local ACP Agent", Detail: "Remove one connected Agent from the Caelis roster",
			})
		}
	}
	return filterSlashArgCandidates(candidates, query, limit)
}

func completeConnectDisconnectAgents(ctx context.Context, driver *Adapter, query string, limit int) ([]SlashArgCandidate, error) {
	if driver == nil {
		return nil, missingRuntimeDependency("ACP Agent disconnect")
	}
	connected, err := driver.DisconnectCandidates(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]SlashArgCandidate, 0, len(connected))
	for _, candidate := range connected {
		detail := firstNonEmpty(candidate.Name, candidate.ConnectionID, "local ACP Agent")
		if candidate.LastOnConnection {
			detail += " · last Agent on this connection; keeps the installed adapter"
		} else {
			detail += fmt.Sprintf(" · %d other %s will remain", candidate.SiblingCount, pluralAgent(candidate.SiblingCount))
		}
		candidates = append(candidates, SlashArgCandidate{
			Value: candidate.AgentID, Display: "/" + candidate.AgentID, Detail: detail,
		})
	}
	return filterSlashArgCandidates(candidates, query, limit), nil
}

func completeConnectDisconnectConfirmation(ctx context.Context, driver *Adapter, agentID string, query string, limit int) ([]SlashArgCandidate, error) {
	if driver == nil {
		return nil, missingRuntimeDependency("ACP Agent disconnect")
	}
	agentID = controlagents.NormalizeName(agentID)
	connected, err := driver.DisconnectCandidates(ctx)
	if err != nil {
		return nil, err
	}
	for _, candidate := range connected {
		if candidate.AgentID != agentID {
			continue
		}
		detail := fmt.Sprintf("Keep the installed adapter and %d sibling %s", candidate.SiblingCount, pluralAgent(candidate.SiblingCount))
		if candidate.LastOnConnection {
			detail = "Remove the Caelis connection settings and keep the installed adapter"
		}
		return filterSlashArgCandidates([]SlashArgCandidate{{
			Value: "confirm", Display: "Disconnect /" + candidate.AgentID, Detail: detail,
		}}, query, limit), nil
	}
	return nil, fmt.Errorf("app/gatewayapp/controladapter: ACP Agent %q is no longer connected", agentID)
}

func pluralAgent(count int) string {
	if count == 1 {
		return "Agent"
	}
	return "Agents"
}

func completeConnectACPAgents(query string, limit int) []SlashArgCandidate {
	agents := agentregistry.ConnectableBuiltInAgents()
	candidates := make([]SlashArgCandidate, 0, len(agents)+1)
	for _, agent := range agents {
		candidates = append(candidates, SlashArgCandidate{
			Value: agent.Name, Display: acpAgentDisplayName(agent.Name), Detail: agent.Description,
		})
	}
	candidates = append(candidates, SlashArgCandidate{Value: "custom", Display: "Custom command", Detail: "Run another local ACP stdio command"})
	return filterSlashArgCandidates(candidates, query, limit)
}

func completeConnectACPLaunchers(agent string, query string, limit int) []SlashArgCandidate {
	agent = strings.ToLower(strings.TrimSpace(agent))
	if agent == "custom" {
		return filterSlashArgCandidates([]SlashArgCandidate{{
			Value: "command", Display: "Custom command", Detail: "Use an executable and arguments you provide",
		}}, query, limit)
	}
	if _, ok := agentregistry.LookupBuiltInAgent(agent); !ok {
		return nil
	}
	if _, managed := agentregistry.BuiltinAdapterPackageFor(agent); !managed {
		return filterSlashArgCandidates([]SlashArgCandidate{{
			Value: "installed", Display: "Installed command", Detail: "Use the ACP Agent executable already installed on PATH",
		}}, query, limit)
	}
	return filterSlashArgCandidates([]SlashArgCandidate{
		{Value: "managed", Display: "Managed by Caelis · Recommended", Detail: "Isolated, verified install; safe to cancel or retry. The first runtime download can be several hundred MB"},
		{Value: "npx", Display: "npx cache", Detail: "Let npx download and cache the curated adapter on first use"},
		{Value: "global", Display: "Global npm install", Detail: "Use or modify the adapter in your global npm environment"},
	}, query, limit)
}

func acpAgentDisplayName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude Code"
	case "opencode":
		return "OpenCode"
	case "codefree-o":
		return "CodeFree-O"
	case "grok":
		return "Grok"
	default:
		return strings.TrimSpace(name)
	}
}

func completeConnectACPModels(ctx context.Context, driver *Adapter, raw string, query string, limit int) ([]SlashArgCandidate, error) {
	payload, snapshot, err := discoverConnectACPState(ctx, driver, raw)
	if err != nil {
		return nil, err
	}
	if len(snapshot.Models) == 0 {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: ACP agent %q did not advertise any models", strings.TrimSpace(payload.Agent))
	}
	candidates := make([]SlashArgCandidate, 0, len(snapshot.Models))
	for _, model := range snapshot.Models {
		candidates = append(candidates, SlashArgCandidate{
			Value: model.ID, Display: firstNonEmpty(model.Name, model.ID),
			Detail: firstNonEmpty(model.Description, "remote ACP model"), ModelMetadataComplete: true,
		})
	}
	return filterSlashArgCandidates(candidates, query, limit), nil
}

func completeConnectACPConfig(ctx context.Context, driver *Adapter, raw string, query string, limit int) ([]SlashArgCandidate, error) {
	payload, snapshot, err := discoverConnectACPState(ctx, driver, raw)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Model) == "" {
		return nil, fmt.Errorf("app/gatewayapp/controladapter: ACP model is required before selecting defaults")
	}
	candidates := []SlashArgCandidate{{Value: "default", Display: "Agent default", Detail: "Keep the ACP Agent's default session options"}}
	for _, option := range snapshot.ConfigOptions {
		if strings.EqualFold(strings.TrimSpace(option.ID), strings.TrimSpace(snapshot.ModelControl.ConfigID)) {
			continue
		}
		for _, choice := range option.Options {
			if strings.TrimSpace(choice.Value) == "" {
				continue
			}
			candidates = append(candidates, SlashArgCandidate{
				Value:   option.ID + "=" + choice.Value,
				Display: firstNonEmpty(option.Name, option.ID) + ": " + firstNonEmpty(choice.Name, choice.Value),
				Detail:  firstNonEmpty(choice.Description, option.Description, "remote ACP session default"),
			})
		}
	}
	return filterSlashArgCandidates(candidates, query, limit), nil
}

func discoverConnectACPState(ctx context.Context, driver *Adapter, raw string) (controlagents.ConnectState, controlagents.DiscoverySnapshot, error) {
	if driver == nil || driver.stack == nil || driver.stack.Agent.DiscoverConnectionFn == nil {
		return controlagents.ConnectState{}, controlagents.DiscoverySnapshot{}, missingRuntimeDependency("ACP agent discovery")
	}
	payload, err := controlagents.DecodeConnectState(raw)
	if err != nil {
		return controlagents.ConnectState{}, controlagents.DiscoverySnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: parse ACP connect state: %w", err)
	}
	snapshot, err := driver.DiscoverACPConnection(ctx, payload.ConnectRequest(driver.WorkspaceDir()))
	return payload, snapshot, err
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
