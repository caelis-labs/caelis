package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
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

func TestRuntimeViewImageUsesModelSelectedByPrepareHook(t *testing.T) {
	for _, imageInput := range []bool{false, true} {
		name := "text"
		if imageInput {
			name = "image"
		}
		t.Run(name, func(t *testing.T) {
			sessions, active := newTestSessionService(t, "view-image-prepare")
			viewImage, err := filesystem.NewViewImage(hostRuntimeForTest(t, active.CWD))
			if err != nil {
				t.Fatal(err)
			}
			// The configured ID must win even when the default provider model
			// reports the same name as that binding.
			defaultModel := &viewImageRuntimeModel{name: name, imageInput: !imageInput}
			selected := &viewImageRuntimeModel{name: name, imageInput: imageInput}
			tools := []tool.Tool{viewImage, tool.NamedTool{Def: tool.Definition{Name: "ordinary"}}}
			core, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Hooks: agent.Hooks{
				ModelPrepare: []agent.Hook[agent.ModelPreparation, agent.ModelPreparation]{{Name: "select", Apply: func(_ context.Context, _ agent.HookContext, p agent.ModelPreparation) (agent.ModelPreparation, error) {
					p.ModelName = name
					p.Request.Tools = tool.ModelSpecs(tools)
					return p, nil
				}}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			run, err := core.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "inspect if possible", AgentSpec: agent.AgentSpec{
				Model: defaultModel, Models: map[string]model.LLM{name: selected},
				Tools: tools,
			}})
			if err != nil {
				t.Fatal(err)
			}
			if err := runnerError(run.Handle); err != nil {
				t.Fatal(err)
			}
			if len(defaultModel.requests) != 0 || len(selected.requests) != 1 {
				t.Fatalf("requests to default/selected model = %d/%d; want 0/1", len(defaultModel.requests), len(selected.requests))
			}
			specs := selected.requests[0].Tools
			if !modelToolSpecNamesContain(specs, "ordinary") || modelToolSpecNamesContain(specs, filesystem.ViewImageToolName) != imageInput {
				t.Fatalf("selected model (ImageInput=%v) received tools %+v", imageInput, specs)
			}
		})
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

func TestRuntimeDynamicViewImageVisibilityIncludesCodeMode(t *testing.T) {
	store, active, root := codeModeStore(t)
	viewImage, err := filesystem.NewViewImage(hostRuntimeForTest(t, root))
	if err != nil {
		t.Fatal(err)
	}
	var routesByStep [][]codemode.ToolRoute
	var hiddenSearchErr, hiddenCallErr error
	executor := codeExecutorFunc(func(ctx context.Context, _ codemode.Source, host codemode.Host) (json.RawMessage, error) {
		routes, routeErr := host.(codemode.NamedHost).ToolNames(ctx)
		if routeErr != nil {
			return nil, routeErr
		}
		routesByStep = append(routesByStep, routes)
		if len(routesByStep) == 2 {
			_, hiddenSearchErr = host.(codemode.DiscoveryHost).Search(ctx, codemode.SearchRequest{Name: filesystem.ViewImageToolName})
			_, hiddenCallErr = host.Call(ctx, filesystem.ViewImageToolName, json.RawMessage(`{"path":"missing.png"}`))
		}
		return json.RawMessage(`true`), nil
	})
	imageModel := &viewImageRuntimeModel{imageInput: true, firstCalls: h1Script(`return true;`)}
	textModel := &viewImageRuntimeModel{firstCalls: h1Script(`return true;`)}
	textModel.firstCalls[0].ID = "text-code-mode"
	tools := []tool.Tool{viewImage, &codemode.Tool{Name: codemode.ToolName, Executor: executor}}
	runCodeMode(t, store, active, agent.AgentSpec{ResolveModelRequest: func(context.Context) (agent.ModelRequestSnapshot, error) {
		var llm model.LLM = imageModel
		if len(imageModel.requests) > 0 {
			llm = textModel
		}
		return agent.ModelRequestSnapshot{Model: llm, Tools: tools, Validate: func(context.Context) (func(), error) {
			return func() {}, nil
		}}, nil
	}}, nil, nil)
	if len(imageModel.requests) != 1 || len(textModel.requests) != 2 || len(routesByStep) != 2 {
		t.Fatalf("image/text requests = %d/%d, Code Mode invocations = %d", len(imageModel.requests), len(textModel.requests), len(routesByStep))
	}
	for i, request := range []*model.Request{imageModel.requests[0], textModel.requests[0]} {
		want := i == 0
		if got := modelToolSpecNamesContain(request.Tools, filesystem.ViewImageToolName); got != want {
			t.Errorf("step %d: ViewImage advertised = %v, want %v", i, got, want)
		}
		found := false
		for _, route := range routesByStep[i] {
			found = found || route.Name == filesystem.ViewImageToolName
		}
		if found != want {
			t.Errorf("step %d: ViewImage Code Mode route = %v, want %v", i, found, want)
		}
	}
	if !errorcode.Is(hiddenSearchErr, errorcode.NotFound) || !errorcode.Is(hiddenCallErr, errorcode.NotFound) {
		t.Fatalf("text-model Code Mode search/call errors = %v / %v, want not found", hiddenSearchErr, hiddenCallErr)
	}
}

type viewImageRuntimeModel struct {
	name          string
	imageInput    bool
	callViewImage bool
	imagePath     string
	requests      []*model.Request
	firstCalls    []model.ToolCall
}

func (m *viewImageRuntimeModel) Name() string {
	if m.name != "" {
		return m.name
	}
	return "view-image-runtime"
}

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
		if step == 1 && len(m.firstCalls) > 0 {
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, m.firstCalls, "")
			response.FinishReason = model.FinishReasonToolCalls
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
