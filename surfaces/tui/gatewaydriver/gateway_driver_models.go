package gatewaydriver

import (
	"context"
	"strings"

	coresession "github.com/OnslaughtSnail/caelis/core/session"
)

func (d *GatewayDriver) hasReusableConnectAuth(ctx context.Context, provider string, baseURL string) bool {
	if d == nil || d.stack == nil {
		return false
	}
	normalizedBaseURL := normalizedConnectBaseURL(baseURL)
	if normalizedBaseURL == "" {
		return false
	}
	ref := coresession.Ref{}
	if activeSession, ok := d.currentSession(); ok {
		ref = activeSession.Ref
	}
	choices, err := d.stack.ListModelChoices(ctx, ref)
	if err != nil {
		return false
	}
	for _, choice := range choices {
		if !strings.EqualFold(strings.TrimSpace(choice.Provider), strings.TrimSpace(provider)) {
			continue
		}
		if normalizedConnectBaseURL(choice.BaseURL) == normalizedBaseURL {
			return true
		}
	}
	return false
}
