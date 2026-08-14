package controladapter

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

func (d *assembler) hasReusableConnectAuth(ctx context.Context, provider string, baseURL string) bool {
	if d == nil || d.stack == nil {
		return false
	}
	if d.stack.Model.HasReusableAuthFn != nil {
		return d.stack.Model.HasReusableAuthFn(ctx, provider, baseURL)
	}
	normalizedBaseURL := modelconfig.NormalizeBaseURL(baseURL)
	if normalizedBaseURL == "" {
		return false
	}
	ref := session.SessionRef{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.SessionRef
	}
	choices, err := listModelChoices(ctx, d.stack.Model, ref)
	if err != nil {
		return false
	}
	for _, choice := range choices {
		if !strings.EqualFold(strings.TrimSpace(choice.Provider), strings.TrimSpace(provider)) {
			continue
		}
		if modelconfig.NormalizeBaseURL(choice.BaseURL) == normalizedBaseURL {
			return true
		}
	}
	return false
}
