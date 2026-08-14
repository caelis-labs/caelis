package runtime

import (
	"context"
	"encoding/base64"
	"iter"
	"os"
	"path/filepath"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
)

const runtimeViewImagePNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestRuntimeWrappedViewImageIsHiddenFromTextOnlyModel(t *testing.T) {
	t.Parallel()

	sessions, active := newTestSessionService(t, "view-image-hidden")
	core, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	viewImage, err := filesystem.NewViewImage(hostRuntimeForTest(t, active.CWD))
	if err != nil {
		t.Fatalf("NewViewImage() error = %v", err)
	}
	probe := &viewImageRuntimeModel{}

	run, err := core.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		Input:      "inspect if possible",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: probe,
			Tools: []tool.Tool{viewImage},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := runnerError(run.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if len(probe.requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(probe.requests))
	}
	if modelToolSpecNamesContain(probe.requests[0].Tools, filesystem.ViewImageToolName) {
		t.Fatalf("text-only model tools = %#v, ViewImage survived production wrappers", probe.requests[0].Tools)
	}
}

func TestRuntimeWorkspacePolicyExecutesViewImageForCapableModel(t *testing.T) {
	t.Parallel()

	sessions, active := newTestSessionService(t, "view-image-executes")
	data, err := base64.StdEncoding.DecodeString(runtimeViewImagePNG)
	if err != nil {
		t.Fatalf("DecodeString(test image) error = %v", err)
	}
	imagePath := filepath.Join(active.CWD, "pixel.png")
	if err := os.WriteFile(imagePath, data, 0o644); err != nil {
		t.Fatalf("WriteFile(pixel.png) error = %v", err)
	}
	viewImage, err := filesystem.NewViewImage(hostRuntimeForTest(t, active.CWD))
	if err != nil {
		t.Fatalf("NewViewImage() error = %v", err)
	}
	core, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	probe := &viewImageRuntimeModel{
		imageInput:    true,
		callViewImage: true,
		imagePath:     "pixel.png",
	}

	run, err := core.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		Input:      "inspect pixel.png",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: probe,
			Tools: []tool.Tool{viewImage},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if err := runnerError(run.Handle); err != nil {
		t.Fatalf("runner error = %v", err)
	}
	if len(probe.requests) != 2 {
		t.Fatalf("model requests = %d, want tool call and final response", len(probe.requests))
	}
	if !modelToolSpecNamesContain(probe.requests[0].Tools, filesystem.ViewImageToolName) {
		t.Fatalf("image-capable model tools = %#v, missing ViewImage", probe.requests[0].Tools)
	}
	if !requestContainsViewImageMedia(probe.requests[1]) {
		t.Fatalf("second request messages = %#v, want successful ViewImage media result", probe.requests[1].Messages)
	}
}

type viewImageRuntimeModel struct {
	imageInput    bool
	callViewImage bool
	imagePath     string
	requests      []*model.Request
}

func (*viewImageRuntimeModel) Name() string { return "view-image-runtime" }

func (m *viewImageRuntimeModel) Capabilities() model.Capabilities {
	return model.Capabilities{
		ToolCalls:         true,
		ParallelToolCalls: true,
		ImageInput:        m.imageInput,
	}
}

func (m *viewImageRuntimeModel) Generate(_ context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	snapshot := cloneViewImageRequest(request)
	m.requests = append(m.requests, snapshot)
	step := len(m.requests)
	return func(yield func(*model.StreamEvent, error) bool) {
		response := &model.Response{
			Message:      model.NewTextMessage(model.RoleAssistant, "done"),
			TurnComplete: true,
			StepComplete: true,
			Status:       model.ResponseStatusCompleted,
		}
		if step == 1 && m.callViewImage {
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID:   "call_image",
				Name: filesystem.ViewImageToolName,
				Args: `{"path":"` + m.imagePath + `"}`,
			}}, "")
			response.FinishReason = model.FinishReasonToolCalls
		}
		yield(model.StreamEventFromResponse(response), nil)
	}
}

func cloneViewImageRequest(request *model.Request) *model.Request {
	if request == nil {
		return nil
	}
	out := *request
	out.Instructions = model.CloneParts(request.Instructions)
	out.Messages = model.CloneMessages(request.Messages)
	out.Tools = model.CloneToolSpecs(request.Tools)
	return &out
}

func modelToolSpecNamesContain(specs []model.ToolSpec, name string) bool {
	for _, spec := range specs {
		if spec.Function != nil && spec.Function.Name == name {
			return true
		}
	}
	return false
}

func requestContainsViewImageMedia(request *model.Request) bool {
	if request == nil {
		return false
	}
	for _, message := range request.Messages {
		for _, result := range message.ToolResults() {
			if result.Name != filesystem.ViewImageToolName || result.IsError {
				continue
			}
			for _, part := range result.Content {
				if part.Media != nil &&
					part.Media.Modality == model.MediaModalityImage &&
					part.Media.Source.Kind == model.MediaSourceInline &&
					part.Media.Source.Data != "" {
					return true
				}
			}
		}
	}
	return false
}
