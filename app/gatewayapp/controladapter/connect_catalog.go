package controladapter

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
)

type connectModelChoice struct {
	Name             string
	Display          string
	Detail           string
	MetadataComplete bool
	ImageInputKnown  bool
}

type connectWizardPayload = connectwizard.ConnectWizardState

func completeConnectArgs(ctx context.Context, driver *assembler, command string, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
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
	case strings.HasPrefix(command, "connect-baseurl:"):
		return completeConnectBaseURL(ctx, driver, strings.TrimPrefix(command, "connect-baseurl:"), query, limit), nil
	case strings.HasPrefix(command, "connect-timeout:"):
		return completeConnectTimeout(strings.TrimPrefix(command, "connect-timeout:"), query, limit), nil
	case strings.HasPrefix(command, "connect-apikey:"):
		return nil, nil
	case strings.HasPrefix(command, "connect-model:"):
		return completeConnectModels(ctx, driver, connectwizard.ParseConnectWizardStatePayload(strings.TrimPrefix(command, "connect-model:")), query, limit)
	case strings.HasPrefix(command, "connect-image-input:"):
		return completeConnectImageInput(query, limit), nil
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

func completeConnectSources(ctx context.Context, driver *assembler, query string, limit int) []controlprompt.SlashArgCandidate {
	candidates := []controlprompt.SlashArgCandidate{
		{Value: "model", Display: "Model provider", Detail: "Connect an API or local model provider"},
		{Value: "acp", Display: "Local ACP Agent", Detail: "Connect an ACP Registry Agent or another local ACP command"},
	}
	if driver != nil {
		if connected, err := driver.DisconnectCandidates(ctx); err == nil && len(connected) > 0 {
			candidates = append(candidates, controlprompt.SlashArgCandidate{
				Value: "disconnect", Display: "Disconnect local ACP Agent", Detail: "Remove one connected Agent from the Caelis roster",
			})
		}
	}
	return filterSlashArgCandidates(candidates, query, limit)
}

func completeConnectDisconnectAgents(ctx context.Context, driver *assembler, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	if driver == nil {
		return nil, missingRuntimeDependency("ACP Agent disconnect")
	}
	connected, err := driver.DisconnectCandidates(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]controlprompt.SlashArgCandidate, 0, len(connected))
	for _, candidate := range connected {
		detail := firstNonEmpty(candidate.Name, candidate.ConnectionID, "local ACP Agent")
		if candidate.LastOnConnection {
			detail += " · last Agent on this connection; keeps the installed adapter"
		} else {
			detail += fmt.Sprintf(" · %d other %s will remain", candidate.SiblingCount, pluralAgent(candidate.SiblingCount))
		}
		candidates = append(candidates, controlprompt.SlashArgCandidate{
			Value: candidate.AgentID, Display: "/" + candidate.AgentID, Detail: detail,
		})
	}
	return filterSlashArgCandidates(candidates, query, limit), nil
}

func completeConnectDisconnectConfirmation(ctx context.Context, driver *assembler, agentID string, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
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
		return filterSlashArgCandidates([]controlprompt.SlashArgCandidate{{
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

func completeConnectACPAgents(query string, limit int) []controlprompt.SlashArgCandidate {
	agents := agentregistry.ConnectableAgents()
	candidates := make([]controlprompt.SlashArgCandidate, 0, len(agents))
	for _, agent := range agents {
		candidates = append(candidates, controlprompt.SlashArgCandidate{
			Value: agent.ID, Display: agent.DisplayName, Detail: connectableACPAgentDetail(agent),
		})
	}
	return filterSlashArgCandidates(candidates, query, limit)
}

func completeConnectACPLaunchers(agent string, query string, limit int) []controlprompt.SlashArgCandidate {
	entry, ok := agentregistry.LookupConnectableAgent(agent)
	if !ok {
		return nil
	}
	candidates := make([]controlprompt.SlashArgCandidate, 0, len(entry.Launchers))
	for _, launcher := range entry.Launchers {
		candidate, ok := connectACPLauncherCandidate(entry, launcher)
		if ok {
			candidates = append(candidates, candidate)
		}
	}
	return filterSlashArgCandidates(candidates, query, limit)
}

func connectACPLauncherCandidate(
	agent agentregistry.ConnectableAgent,
	launcher controlagents.LauncherChoice,
) (controlprompt.SlashArgCandidate, bool) {
	recommended := launcher == agent.Preferred
	display := func(label string) string {
		if recommended {
			return label + " · Recommended"
		}
		return label
	}
	switch launcher {
	case controlagents.LauncherChoiceManaged:
		return controlprompt.SlashArgCandidate{
			Value: string(launcher), Display: display("Managed by Caelis"),
			Detail: "Isolated, verified install; safe to cancel or retry. The first runtime download can be several hundred MB",
		}, true
	case controlagents.LauncherChoiceNPX:
		return controlprompt.SlashArgCandidate{
			Value: string(launcher), Display: display("npx cache"),
			Detail: "Run the pinned ACP Registry package through npx",
		}, true
	case controlagents.LauncherChoiceGlobal:
		return controlprompt.SlashArgCandidate{
			Value: string(launcher), Display: display("Global npm install"),
			Detail: "Use or modify the adapter in your global npm environment",
		}, true
	case controlagents.LauncherChoiceInstalled:
		installed, ok := agentregistry.LookupInstalledAgent(agent.ID)
		if !ok {
			return controlprompt.SlashArgCandidate{}, false
		}
		return controlprompt.SlashArgCandidate{
			Value: string(launcher), Display: display("Installed command"),
			Detail: fmt.Sprintf("Use %q from PATH", installed.Command),
		}, true
	case controlagents.LauncherChoiceCommand:
		return controlprompt.SlashArgCandidate{
			Value: string(launcher), Display: display("Custom command"),
			Detail: "Use an executable and arguments you provide",
		}, true
	default:
		return controlprompt.SlashArgCandidate{}, false
	}
}

func connectableACPAgentDetail(agent agentregistry.ConnectableAgent) string {
	detail := strings.TrimSpace(agent.Description)
	if agent.RegistryID == "" {
		return detail
	}
	source := "ACP Registry"
	if version := strings.TrimSpace(agent.Version); version != "" {
		source += " v" + version
	}
	return firstNonEmpty(detail+" · "+source, source)
}

func completeConnectProviders(query string, limit int) []controlprompt.SlashArgCandidate {
	templates := modelconfig.ProviderTemplates()
	out := make([]controlprompt.SlashArgCandidate, 0, len(templates))
	for _, template := range templates {
		if query != "" && !strings.Contains(strings.ToLower(template.Label+" "+template.Description), strings.ToLower(strings.TrimSpace(query))) {
			continue
		}
		out = append(out, controlprompt.SlashArgCandidate{
			Value:   template.Label,
			Display: template.Label,
			Detail:  strings.TrimSpace(template.Description),
			NoAuth:  template.NoAuthRequired || template.AuthFlow != "",
		})
		if len(out) >= limit {
			break
		}
	}
	return out
}

func completeConnectBaseURL(ctx context.Context, driver *assembler, provider string, query string, limit int) []controlprompt.SlashArgCandidate {
	template, ok := modelconfig.LookupProvider(provider)
	if !ok {
		return nil
	}
	candidates := connectEndpointCandidates(template)
	if len(candidates) == 0 {
		candidates = append(candidates, controlprompt.SlashArgCandidate{Value: template.DefaultBaseURL, Display: template.DefaultBaseURL, Detail: "default base URL"})
	}
	for i := range candidates {
		if driver != nil && driver.hasReusableConnectAuth(ctx, template.Provider, candidates[i].Value) {
			candidates[i].NoAuth = true
			candidates[i].Detail = strings.Join(compactNonEmpty([]string{strings.TrimSpace(candidates[i].Detail), "configured auth"}), " · ")
		}
	}
	return filterSlashArgCandidates(candidates, query, limit)
}

func connectEndpointCandidates(template modelconfig.ProviderTemplate) []controlprompt.SlashArgCandidate {
	if len(template.Endpoints) == 0 {
		return nil
	}
	out := make([]controlprompt.SlashArgCandidate, 0, len(template.Endpoints))
	for _, endpoint := range template.Endpoints {
		out = append(out, controlprompt.SlashArgCandidate{
			Value:   endpoint.BaseURL,
			Display: endpoint.Display,
			Detail:  strings.TrimSpace(endpoint.Detail),
			NoAuth:  endpoint.NoAuthRequired,
		})
	}
	return out
}

func completeConnectTimeout(provider string, query string, limit int) []controlprompt.SlashArgCandidate {
	values := []string{"60", "120", "180"}
	out := make([]controlprompt.SlashArgCandidate, 0, len(values))
	for _, value := range values {
		out = append(out, controlprompt.SlashArgCandidate{Value: value, Display: value, Detail: fmt.Sprintf("%ss", value)})
	}
	_ = provider
	return filterSlashArgCandidates(out, query, limit)
}

func completeConnectModels(ctx context.Context, driver *assembler, payload connectWizardPayload, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	template, ok := modelconfig.LookupProvider(payload.Provider)
	if !ok {
		return nil, nil
	}
	models, err := modelconfig.MaintainedSelectableModels(ctx, template.Provider, payload.BaseURL)
	if err != nil {
		return nil, err
	}
	choices := buildConnectModelChoices(template.Provider, models)
	out := make([]controlprompt.SlashArgCandidate, 0, len(choices))
	for _, choice := range choices {
		if query != "" && !strings.Contains(strings.ToLower(choice.Name+" "+choice.Display+" "+choice.Detail), strings.ToLower(strings.TrimSpace(query))) {
			continue
		}
		out = append(out, controlprompt.SlashArgCandidate{
			Value:                 choice.Name,
			Display:               choice.Display,
			Detail:                choice.Detail,
			ModelMetadataComplete: choice.MetadataComplete,
			ModelImageInputKnown:  choice.ImageInputKnown,
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func completeConnectImageInput(query string, limit int) []controlprompt.SlashArgCandidate {
	return filterSlashArgCandidates([]controlprompt.SlashArgCandidate{
		{
			Value:   "false",
			Display: "Text only",
			Detail:  "Keep image input disabled · conservative default",
		},
		{
			Value:   "true",
			Display: "Supports images",
			Detail:  "Enable image attachments and ViewImage",
		},
	}, query, limit)
}

func completeConnectContext(ctx context.Context, driver *assembler, payload connectWizardPayload, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	_ = ctx
	_ = driver
	defaults, err := modelconfig.ResolveModelDefaultsForEndpoint(payload.Provider, payload.BaseURL, payload.Model)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]controlprompt.SlashArgCandidate{{Value: strconv.Itoa(defaults.ContextWindowTokens), Display: strconv.Itoa(defaults.ContextWindowTokens), Detail: "context window tokens"}}, query, limit), nil
}

func completeConnectMaxOutput(ctx context.Context, driver *assembler, payload connectWizardPayload, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	_ = ctx
	_ = driver
	defaults, err := modelconfig.ResolveModelDefaultsForEndpoint(payload.Provider, payload.BaseURL, payload.Model)
	if err != nil {
		return nil, err
	}
	return filterSlashArgCandidates([]controlprompt.SlashArgCandidate{{Value: strconv.Itoa(defaults.MaxOutputTokens), Display: strconv.Itoa(defaults.MaxOutputTokens), Detail: "max output tokens"}}, query, limit), nil
}

func completeConnectReasoningLevels(ctx context.Context, driver *assembler, payload connectWizardPayload, query string, limit int) ([]controlprompt.SlashArgCandidate, error) {
	_ = ctx
	_ = driver
	defaults, err := modelconfig.ResolveModelDefaultsForEndpoint(payload.Provider, payload.BaseURL, payload.Model)
	if err != nil {
		return nil, err
	}
	value := "-"
	detail := "no reasoning levels"
	if len(defaults.ReasoningLevels) > 0 {
		value = strings.Join(defaults.ReasoningLevels, ",")
		detail = "suggested reasoning levels"
	}
	return filterSlashArgCandidates([]controlprompt.SlashArgCandidate{{Value: value, Display: value, Detail: detail}}, query, limit), nil
}

func filterSlashArgCandidates(candidates []controlprompt.SlashArgCandidate, query string, limit int) []controlprompt.SlashArgCandidate {
	query = strings.ToLower(strings.TrimSpace(query))
	out := make([]controlprompt.SlashArgCandidate, 0, len(candidates))
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
	add := func(modelChoice modelconfig.SelectableModel) {
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
		detail := strings.TrimSpace(modelChoice.Detail)
		if detail == "" {
			detail = "suggested model"
		}
		out = append(out, connectModelChoice{
			Name:             name,
			Display:          connectDisplayModelRef(provider, name),
			Detail:           strings.TrimSpace(detail),
			MetadataComplete: modelChoice.MetadataComplete,
			ImageInputKnown:  modelChoice.ImageInputKnown,
		})
	}
	for _, item := range fallbackModels {
		add(item)
	}
	template, maintainedProvider := modelconfig.LookupProvider(provider)
	if !maintainedProvider || !template.PreserveModelOrder {
		sort.SliceStable(out, func(i, j int) bool {
			return strings.ToLower(out[i].Display) < strings.ToLower(out[j].Display)
		})
	}
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
