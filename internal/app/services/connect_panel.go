package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/session"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type ModelConnectRequest struct {
	SessionRef session.Ref `json:"session_ref,omitempty"`
}

func (s ModelService) ConnectPanel(ctx context.Context, req ModelConnectRequest) (appviewmodel.ModelConnectView, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	choices, err := s.List(ctx)
	if err != nil {
		return appviewmodel.ModelConnectView{}, err
	}
	view := appviewmodel.ModelConnectView{
		Configured: statusModelChoices(choices),
		Providers:  s.connectProviderViews(ctx, choices),
		Wizard:     DefaultConnectWizardFlow(),
	}
	if len(choices) > 0 {
		current, ok, err := s.Current(ctx, req.SessionRef)
		if err != nil {
			return appviewmodel.ModelConnectView{}, err
		}
		if ok {
			choice := statusModelChoice(appsettings.ModelChoiceFromConfig(current, modelChoiceIsDefault(choices, current.ID)))
			view.Current = &choice
		}
	}
	view.Diagnostics = connectPanelDiagnostics(view)
	return view, nil
}

func (s ModelService) connectProviderViews(ctx context.Context, choices []appsettings.ModelChoice) []appviewmodel.ModelConnectProvider {
	counts := connectConfiguredModelCounts(choices)
	templates := ConnectProviderTemplates()
	out := make([]appviewmodel.ModelConnectProvider, 0, len(templates))
	for _, tpl := range templates {
		provider := strings.TrimSpace(tpl.Provider)
		label := strings.TrimSpace(tpl.Label)
		tokenEnv := DefaultConnectTokenEnvName(provider, tpl.DefaultBaseURL)
		count := counts[normalizeModelCatalogKey(provider)]
		out = append(out, appviewmodel.ModelConnectProvider{
			ID:                   firstNonEmpty(label, provider),
			Label:                label,
			Provider:             provider,
			API:                  strings.TrimSpace(string(tpl.API)),
			Description:          strings.TrimSpace(tpl.Description),
			DefaultBaseURL:       strings.TrimSpace(tpl.DefaultBaseURL),
			DefaultEndpointID:    strings.TrimSpace(tpl.DefaultEndpointID),
			TokenEnv:             tokenEnv,
			NoAuthRequired:       tpl.NoAuthRequired,
			Configured:           count > 0,
			ConfiguredModelCount: count,
			CatalogModelCount:    len(s.fallbackConnectModels(tpl)),
			CommonModels:         append([]string(nil), tpl.CommonModels...),
			Endpoints:            s.connectEndpointViews(ctx, tpl),
		})
	}
	return out
}

func (s ModelService) connectEndpointViews(ctx context.Context, tpl ConnectProviderTemplate) []appviewmodel.ModelConnectEndpoint {
	if len(tpl.Endpoints) == 0 {
		return nil
	}
	out := make([]appviewmodel.ModelConnectEndpoint, 0, len(tpl.Endpoints))
	for _, endpoint := range tpl.Endpoints {
		reusable := s.hasReusableConnectAuth(ctx, tpl.Provider, endpoint.BaseURL)
		out = append(out, appviewmodel.ModelConnectEndpoint{
			ID:           strings.TrimSpace(endpoint.ID),
			BaseURL:      strings.TrimSpace(endpoint.BaseURL),
			Display:      strings.TrimSpace(endpoint.Display),
			Detail:       strings.TrimSpace(endpoint.Detail),
			API:          strings.TrimSpace(string(endpoint.API)),
			TokenEnv:     strings.TrimSpace(endpoint.TokenEnv),
			NoAuth:       tpl.NoAuthRequired || reusable,
			ReusableAuth: reusable,
		})
	}
	return out
}

func connectConfiguredModelCounts(choices []appsettings.ModelChoice) map[string]int {
	counts := map[string]int{}
	for _, choice := range choices {
		provider := normalizeModelCatalogKey(choice.Provider)
		if provider == "" {
			continue
		}
		counts[provider]++
	}
	return counts
}

func connectPanelDiagnostics(view appviewmodel.ModelConnectView) []appviewmodel.ModelConnectDiagnostic {
	var out []appviewmodel.ModelConnectDiagnostic
	if view.Current == nil && len(view.Configured) == 0 {
		out = append(out, appviewmodel.ModelConnectDiagnostic{
			Severity: "warning",
			Kind:     "model_configuration",
			Message:  "no model is configured",
		})
	}
	for _, provider := range view.Providers {
		if provider.NoAuthRequired || provider.TokenEnv != "" {
			continue
		}
		out = append(out, appviewmodel.ModelConnectDiagnostic{
			Severity: "info",
			Kind:     "auth_hint",
			Provider: provider.Provider,
			Message:  "provider requires explicit API key or reusable profile auth",
		})
	}
	return out
}

func formatCommandConnectPanel(view appviewmodel.ModelConnectView) string {
	lines := []string{"connect:"}
	if view.Current != nil {
		lines = append(lines, "  current: "+formatModelChoiceRef(*view.Current))
	} else if len(view.Configured) > 0 {
		lines = append(lines, fmt.Sprintf("  configured: %d models", len(view.Configured)))
	} else {
		lines = append(lines, "  current: not configured")
	}
	if len(view.Providers) > 0 {
		lines = append(lines, "  providers:")
		for _, provider := range view.Providers {
			lines = append(lines, "    "+formatConnectProviderLine(provider))
			if len(provider.Endpoints) > 0 {
				lines = append(lines, "      endpoints: "+formatConnectEndpointList(provider.Endpoints))
			}
		}
	}
	if len(view.Wizard.Steps) > 0 {
		lines = append(lines, fmt.Sprintf("  wizard: %s (%d steps)", firstNonEmpty(view.Wizard.DisplayLine, "/connect"), len(view.Wizard.Steps)))
	}
	if len(view.Diagnostics) > 0 {
		lines = append(lines, "  diagnostics:")
		for _, diagnostic := range view.Diagnostics {
			lines = append(lines, "    "+formatConnectDiagnosticLine(diagnostic))
		}
	}
	lines = append(lines, "  usage: /connect provider model [base-url] [timeout] [token] [context] [max-output] [reasoning-levels]")
	return strings.Join(commandNonEmpty(lines), "\n")
}

func formatConnectProviderLine(provider appviewmodel.ModelConnectProvider) string {
	label := firstNonEmpty(provider.Label, provider.Provider, provider.ID)
	parts := []string{label}
	if provider.Description != "" {
		parts = append(parts, "- "+provider.Description)
	}
	var details []string
	if provider.DefaultBaseURL != "" {
		details = append(details, "default="+provider.DefaultBaseURL)
	}
	if provider.TokenEnv != "" {
		details = append(details, "env:"+provider.TokenEnv)
	} else if provider.NoAuthRequired {
		details = append(details, "no auth")
	}
	if provider.ConfiguredModelCount > 0 {
		details = append(details, fmt.Sprintf("%d configured", provider.ConfiguredModelCount))
	}
	if provider.CatalogModelCount > 0 {
		details = append(details, fmt.Sprintf("%d suggested", provider.CatalogModelCount))
	}
	if len(details) > 0 {
		parts = append(parts, "("+strings.Join(details, " · ")+")")
	}
	return strings.Join(parts, " ")
}

func formatConnectEndpointList(endpoints []appviewmodel.ModelConnectEndpoint) string {
	if len(endpoints) == 0 {
		return ""
	}
	parts := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		label := firstNonEmpty(endpoint.Display, endpoint.ID, endpoint.BaseURL)
		detail := endpoint.BaseURL
		if endpoint.TokenEnv != "" {
			detail = strings.Join(commandNonEmpty([]string{detail, "env:" + endpoint.TokenEnv}), " ")
		}
		if endpoint.ReusableAuth {
			detail = strings.Join(commandNonEmpty([]string{detail, "configured auth"}), " ")
		}
		if detail != "" {
			label += " [" + detail + "]"
		}
		parts = append(parts, label)
	}
	return strings.Join(parts, "; ")
}

func formatConnectDiagnosticLine(diagnostic appviewmodel.ModelConnectDiagnostic) string {
	label := firstNonEmpty(diagnostic.Kind, "diagnostic")
	if diagnostic.Provider != "" {
		label = strings.Trim(strings.TrimSpace(diagnostic.Provider)+"/"+label, "/")
	}
	severity := firstNonEmpty(diagnostic.Severity, "info")
	line := "[" + severity + "] " + label
	if diagnostic.Message != "" {
		line += ": " + diagnostic.Message
	}
	return line
}

func formatModelChoiceRef(choice appviewmodel.ModelChoice) string {
	return firstNonEmpty(choice.Alias, strings.Trim(strings.TrimSpace(choice.Provider)+"/"+strings.TrimSpace(choice.Model), "/"), choice.ID)
}
