package controladapter

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func (d *assembler) hasReusableConnectAuth(ctx context.Context, provider string, baseURL string) bool {
	if d == nil || d.deps == nil {
		return false
	}
	if d.deps.Model.HasReusableAuthFn != nil {
		return d.deps.Model.HasReusableAuthFn(ctx, provider, baseURL)
	}
	normalizedBaseURL := modelconfig.NormalizeBaseURL(baseURL)
	if normalizedBaseURL == "" {
		return false
	}
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := listModelChoices(ctx, d.deps.Model, ref)
	if err != nil {
		return false
	}
	for _, choice := range choices {
		canonical := provider
		api := modelconfig.DefaultAPIForProvider(provider)
		if template, ok := modelconfig.LookupProvider(provider); ok {
			canonical, api = template.Provider, template.API
		}
		if !strings.EqualFold(strings.TrimSpace(choice.Provider), strings.TrimSpace(canonical)) {
			continue
		}
		if modelconfig.NormalizeEndpointID(choice.Provider, choice.EndpointID, choice.BaseURL, api) != modelconfig.NormalizeEndpointID(canonical, "", baseURL, api) {
			continue
		}
		if modelconfig.NormalizeBaseURL(choice.BaseURL) == normalizedBaseURL {
			return true
		}
	}
	return false
}
