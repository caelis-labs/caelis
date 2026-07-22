package gatewayapp

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	kernelimpl "github.com/caelis-labs/caelis/internal/kernel"
)

// systemAgentReasoningModel applies a Control-selected system-Agent effort at
// the final model boundary. System scenes may have their own fallback effort,
// so this wrapper deliberately wins over request metadata when a binding
// supplies an explicit or model-default effort.
type systemAgentReasoningModel struct {
	inner  model.LLM
	effort string
}

func withSystemAgentReasoningEffort(resolved kernelimpl.ModelResolution) model.LLM {
	effort := strings.TrimSpace(resolved.ReasoningEffort)
	if resolved.Model == nil || effort == "" {
		return resolved.Model
	}
	return &systemAgentReasoningModel{inner: resolved.Model, effort: effort}
}

func (m *systemAgentReasoningModel) Name() string {
	if m == nil || m.inner == nil {
		return ""
	}
	return m.inner.Name()
}

func (m *systemAgentReasoningModel) Generate(ctx context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	if m == nil || m.inner == nil {
		return func(yield func(*model.StreamEvent, error) bool) {
			yield(nil, fmt.Errorf("gatewayapp: system Agent model is unavailable"))
		}
	}
	if req == nil {
		return m.inner.Generate(ctx, nil)
	}
	cloned := *req
	cloned.Reasoning.Effort = m.effort
	return m.inner.Generate(ctx, &cloned)
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
