package providers

import "github.com/caelis-labs/caelis/agent-sdk/model"

var (
	_ model.CapabilityProvider = (*openAICompatLLM)(nil)
	_ model.CapabilityProvider = (*openAICodexLLM)(nil)
	_ model.CapabilityProvider = (*codeFreeLLM)(nil)
	_ model.CapabilityProvider = (*anthropicSDKLLM)(nil)
	_ model.CapabilityProvider = (*geminiLLM)(nil)
	_ model.CapabilityProvider = (*ollamaLLM)(nil)
)

func (l *openAICodexLLM) Capabilities() model.Capabilities {
	if l == nil {
		return model.Capabilities{}
	}
	return model.Capabilities{
		ToolCalls:             true,
		Streaming:             true,
		ParallelToolCalls:     true,
		ReasoningContinuation: true,
	}
}

func (l *openAICompatLLM) Capabilities() model.Capabilities {
	if l == nil {
		return model.Capabilities{}
	}
	return model.Capabilities{
		ToolCalls:             true,
		StructuredOutput:      l.options.StructuredOutput != "",
		Streaming:             true,
		ParallelToolCalls:     true,
		ReasoningContinuation: l.options.IncludeReasoningContent,
		HostedTools:           l.options.ProviderTools != nil,
	}
}

func (l *codeFreeLLM) Capabilities() model.Capabilities {
	if l == nil {
		return model.Capabilities{}
	}
	return model.Capabilities{
		ToolCalls:         true,
		StructuredOutput:  l.options.StructuredOutput != "",
		Streaming:         true,
		ParallelToolCalls: true,
		HostedTools:       l.options.ProviderTools != nil,
	}
}

func (l *anthropicSDKLLM) Capabilities() model.Capabilities {
	return model.Capabilities{
		ToolCalls:             true,
		StructuredOutput:      true,
		Streaming:             true,
		ParallelToolCalls:     true,
		ReasoningContinuation: true,
		HostedTools:           true,
	}
}

func (l *geminiLLM) Capabilities() model.Capabilities {
	return model.Capabilities{
		ToolCalls:             true,
		StructuredOutput:      true,
		Streaming:             true,
		ParallelToolCalls:     true,
		ReasoningContinuation: true,
		HostedTools:           true,
	}
}

func (l *ollamaLLM) Capabilities() model.Capabilities {
	return model.Capabilities{
		ToolCalls:             true,
		StructuredOutput:      true,
		Streaming:             true,
		ParallelToolCalls:     true,
		ReasoningContinuation: true,
	}
}
