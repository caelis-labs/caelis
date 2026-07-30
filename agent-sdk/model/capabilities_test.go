package model_test

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestValidateCapabilitiesIsConservativeAndDeterministic(t *testing.T) {
	t.Parallel()

	err := model.ValidateCapabilities("third-party", model.Capabilities{}, model.Capabilities{
		Streaming:        true,
		StructuredOutput: true,
		ToolCalls:        true,
	})
	var capabilityErr *model.CapabilityError
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("error = %v, want *model.CapabilityError", err)
	}
	if got, want := capabilityErr.Capability, model.CapabilityToolCalls; got != want {
		t.Fatalf("capability = %q, want deterministic first %q", got, want)
	}
}

func TestMergeCapabilitiesUnionsRequirements(t *testing.T) {
	t.Parallel()

	got := model.MergeCapabilities(
		model.Capabilities{Streaming: true, HostedTools: true, ImageInput: true},
		model.Capabilities{StructuredOutput: true, Streaming: true},
	)
	want := model.Capabilities{Streaming: true, StructuredOutput: true, HostedTools: true, ImageInput: true}
	if got != want {
		t.Fatalf("MergeCapabilities() = %+v, want %+v", got, want)
	}
}

func TestDeriveContentRequiredCapabilitiesFindsDirectAndNestedImages(t *testing.T) {
	t.Parallel()

	image := model.NewMediaPart(
		model.MediaModalityImage,
		model.MediaSource{Kind: model.MediaSourceInline, Data: "aW1n"},
		"image/png",
		"pixel.png",
	)
	tests := []struct {
		name    string
		request *model.Request
	}{
		{
			name: "user image",
			request: &model.Request{Messages: []model.Message{
				model.NewMessage(model.RoleUser, image),
			}},
		},
		{
			name: "tool result image",
			request: &model.Request{Messages: []model.Message{
				model.NewMessage(model.RoleTool, model.Part{
					Kind: model.PartKindToolResult,
					ToolResult: &model.ToolResultPart{
						ToolUseID: "call_image",
						Name:      "ViewImage",
						Content:   []model.Part{image},
					},
				}),
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			required := model.DeriveContentRequiredCapabilities(model.Capabilities{}, test.request)
			if !required.ImageInput {
				t.Fatalf("required capabilities = %+v, want image input", required)
			}
		})
	}
	if required := model.DeriveContentRequiredCapabilities(model.Capabilities{}, &model.Request{
		Messages: []model.Message{model.NewTextMessage(model.RoleUser, "text")},
	}); required.ImageInput {
		t.Fatalf("text-only required capabilities = %+v", required)
	}
}

func TestValidateOutputSpecRejectsUnsupportedOrIncompleteContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		spec *model.OutputSpec
	}{
		{name: "unknown mode", spec: &model.OutputSpec{Mode: model.OutputMode("yaml")}},
		{name: "unimplemented tool-only mode", spec: &model.OutputSpec{Mode: model.OutputMode("tool_only")}},
		{name: "schema without schema", spec: &model.OutputSpec{Mode: model.OutputModeSchema}},
		{name: "negative token limit", spec: &model.OutputSpec{Mode: model.OutputModeText, MaxOutputTokens: -1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var outputErr *model.OutputSpecError
			if err := model.ValidateOutputSpec(tt.spec); !errors.As(err, &outputErr) {
				t.Fatalf("ValidateOutputSpec(%+v) error = %v, want *model.OutputSpecError", tt.spec, err)
			}
		})
	}
}

func TestValidateOutputSpecAcceptsSupportedContracts(t *testing.T) {
	t.Parallel()

	for _, spec := range []*model.OutputSpec{
		nil,
		{},
		{Mode: model.OutputModeText},
		{Mode: model.OutputModeJSON},
		{Mode: model.OutputModeSchema, JSONSchema: map[string]any{"type": "object"}},
	} {
		if err := model.ValidateOutputSpec(spec); err != nil {
			t.Fatalf("ValidateOutputSpec(%+v) error = %v", spec, err)
		}
	}
}
