package gatewayapp

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

// systemAgentReasoningModel applies the Control-selected fixed-handle effort at
// the final model boundary. Placement is the single system-Agent effort owner,
// so its explicit or model-default value wins over generic request metadata.
type systemAgentReasoningModel struct {
	inner  model.LLM
	effort string
	fast   bool
}

func withSystemAgentReasoningEffort(resolved kernelimpl.ModelResolution) model.LLM {
	effort := strings.TrimSpace(resolved.ReasoningEffort)
	if resolved.Model == nil || (effort == "" && !resolved.FastMode) {
		return resolved.Model
	}
	return &systemAgentReasoningModel{inner: resolved.Model, effort: effort, fast: resolved.FastMode}
}

func (m *systemAgentReasoningModel) Name() string {
	if m == nil || m.inner == nil {
		return ""
	}
	return m.inner.Name()
}

func (*systemAgentReasoningModel) TracksInvocations() {}

func (m *systemAgentReasoningModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if m == nil || m.inner == nil {
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(nil, fmt.Errorf("gatewayapp: system Agent model is unavailable"))
		}
	}
	return model.GenerateWithRequestTransform(ctx, m.inner, req, func(request *model.Request) {
		if m.effort != "" {
			request.Reasoning.Effort = m.effort
		}
		request.ServiceTier = ""
		if m.fast {
			request.ServiceTier = model.ServiceTierPriority
		}
	})
}

func (m *systemAgentReasoningModel) Capabilities() model.Capabilities {
	if m == nil || m.inner == nil {
		return model.Capabilities{}
	}
	capabilities, _ := model.CapabilitiesOf(m.inner)
	return capabilities
}

func (m *systemAgentReasoningModel) ProviderName() string {
	if m == nil || m.inner == nil {
		return ""
	}
	provider, _ := m.inner.(interface{ ProviderName() string })
	if provider == nil {
		return ""
	}
	return strings.TrimSpace(provider.ProviderName())
}

func (m *systemAgentReasoningModel) ContextWindowTokens() int {
	if m == nil || m.inner == nil {
		return 0
	}
	provider, _ := m.inner.(interface{ ContextWindowTokens() int })
	if provider == nil {
		return 0
	}
	return provider.ContextWindowTokens()
}
