package model

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// Capability identifies one provider-neutral model feature that must be
// declared before assembly may rely on it.
type Capability string

const (
	CapabilityToolCalls             Capability = "tool_calls"
	CapabilityStructuredOutput      Capability = "structured_output"
	CapabilityStreaming             Capability = "streaming"
	CapabilityParallelToolCalls     Capability = "parallel_tool_calls"
	CapabilityReasoningContinuation Capability = "reasoning_continuation"
	CapabilityHostedTools           Capability = "hosted_tools"
)

// Capabilities is the explicit feature contract of one LLM implementation.
// False is conservative: unknown support is not treated as support.
type Capabilities struct {
	ToolCalls             bool `json:"tool_calls,omitempty"`
	StructuredOutput      bool `json:"structured_output,omitempty"`
	Streaming             bool `json:"streaming,omitempty"`
	ParallelToolCalls     bool `json:"parallel_tool_calls,omitempty"`
	ReasoningContinuation bool `json:"reasoning_continuation,omitempty"`
	HostedTools           bool `json:"hosted_tools,omitempty"`
}

// CapabilityProvider declares model features independently of provider name
// or catalog heuristics.
type CapabilityProvider interface {
	Capabilities() Capabilities
}

// CapabilityError reports a required model feature that is not declared.
type CapabilityError struct {
	Model      string
	Capability Capability
}

func (e *CapabilityError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("model: %q does not declare required capability %q", strings.TrimSpace(e.Model), e.Capability)
}

func (e *CapabilityError) ErrorCode() errorcode.Code { return errorcode.Unsupported }

// CapabilitiesOf returns an LLM's explicit declaration. The boolean is false
// when the implementation has no declaration at all.
func CapabilitiesOf(llm LLM) (Capabilities, bool) {
	provider, ok := llm.(CapabilityProvider)
	if !ok || provider == nil {
		return Capabilities{}, false
	}
	return provider.Capabilities(), true
}

// ValidateCapabilities rejects the first required feature that actual does not
// declare. The check order is stable so assembly failures are deterministic.
func ValidateCapabilities(modelName string, actual, required Capabilities) error {
	checks := []struct {
		capability Capability
		actual     bool
		required   bool
	}{
		{CapabilityToolCalls, actual.ToolCalls, required.ToolCalls},
		{CapabilityStructuredOutput, actual.StructuredOutput, required.StructuredOutput},
		{CapabilityStreaming, actual.Streaming, required.Streaming},
		{CapabilityParallelToolCalls, actual.ParallelToolCalls, required.ParallelToolCalls},
		{CapabilityReasoningContinuation, actual.ReasoningContinuation, required.ReasoningContinuation},
		{CapabilityHostedTools, actual.HostedTools, required.HostedTools},
	}
	for _, check := range checks {
		if check.required && !check.actual {
			return &CapabilityError{Model: strings.TrimSpace(modelName), Capability: check.capability}
		}
	}
	return nil
}

// MergeCapabilities returns the union of two capability requirements.
func MergeCapabilities(left, right Capabilities) Capabilities {
	return Capabilities{
		ToolCalls:             left.ToolCalls || right.ToolCalls,
		StructuredOutput:      left.StructuredOutput || right.StructuredOutput,
		Streaming:             left.Streaming || right.Streaming,
		ParallelToolCalls:     left.ParallelToolCalls || right.ParallelToolCalls,
		ReasoningContinuation: left.ReasoningContinuation || right.ReasoningContinuation,
		HostedTools:           left.HostedTools || right.HostedTools,
	}
}

// DeriveRequiredCapabilities expands declared model requirements with the
// features implied by one assembled invocation (stream, output mode, tools).
// Control preflight and Runtime gates must share this derivation.
func DeriveRequiredCapabilities(declared Capabilities, stream bool, output *OutputSpec, toolCount int) Capabilities {
	required := declared
	if stream {
		required.Streaming = true
	}
	if output != nil && output.Mode != "" && output.Mode != OutputModeText {
		required.StructuredOutput = true
	}
	if toolCount > 0 {
		required.ToolCalls = true
	}
	return required
}

// OutputSpecError reports an invalid or unsupported output contract before a
// provider request is attempted.
type OutputSpecError struct {
	Mode   OutputMode
	Detail string
}

func (e *OutputSpecError) Error() string {
	if e == nil {
		return "<nil>"
	}
	detail := strings.TrimSpace(e.Detail)
	if detail == "" {
		detail = "unsupported output contract"
	}
	return fmt.Sprintf("model: output mode %q: %s", e.Mode, detail)
}

func (e *OutputSpecError) ErrorCode() errorcode.Code { return errorcode.Unsupported }

// ValidateOutputSpec rejects output contracts that no provider-neutral
// adapter can faithfully enforce.
func ValidateOutputSpec(spec *OutputSpec) error {
	if spec == nil {
		return nil
	}
	if spec.MaxOutputTokens < 0 {
		return &OutputSpecError{Mode: spec.Mode, Detail: "max_output_tokens must be non-negative"}
	}
	switch spec.Mode {
	case "", OutputModeText, OutputModeJSON:
		return nil
	case OutputModeSchema:
		if len(spec.JSONSchema) == 0 {
			return &OutputSpecError{Mode: spec.Mode, Detail: "json_schema is required"}
		}
		return nil
	default:
		return &OutputSpecError{Mode: spec.Mode, Detail: "unsupported output mode"}
	}
}
