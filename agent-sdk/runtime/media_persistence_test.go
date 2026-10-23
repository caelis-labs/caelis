package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"iter"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestSharedMediaRebuildsRuntimeProducedContextWithoutToolReplay(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	var pngData bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&pngData, image.NewNRGBA(image.Rect(0, 0, 32, 12249))); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(pngData.Bytes())
	content := []model.Part{model.NewTextPart("screenshot"), model.NewMediaPart(model.MediaModalityImage, model.MediaSource{Kind: model.MediaSourceInline, Data: data}, "image/png", "screen")}
	calls := 0
	screen := tool.NamedTool{Def: tool.Definition{Name: "ECHO", EffectClass: tool.EffectReadOnly}, Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
		calls++
		return tool.Result{ID: call.ID, Name: call.Name, Content: content}, nil
	}}
	allow := staticPolicyRegistry{mode: policy.NamedMode{ID: "allow", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	run := func(sessions session.Service, probe *mediaPersistenceModel, input string) {
		t.Helper()
		rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, PolicyRegistry: allow, DefaultPolicyMode: "allow"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: input,
			AgentSpec: agent.AgentSpec{Name: "chat", Model: probe, Tools: []tool.Tool{screen}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := drainRunnerEvents(t, result.Handle); err != nil {
			t.Fatal(err)
		}
	}
	first := &mediaPersistenceModel{}
	run(store, first, "take a screenshot")
	if len(first.requests) != 2 || calls != 1 {
		t.Fatalf("requests=%d tool calls=%d", len(first.requests), calls)
	}
	images := 0
	for _, message := range first.requests[1] {
		for _, result := range message.ToolResults() {
			for _, part := range result.Content {
				if part.Media == nil {
					continue
				}
				images++
				decoded, err := base64.StdEncoding.DecodeString(part.Media.Source.Data)
				if err != nil {
					t.Fatal(err)
				}
				cfg, _, err := image.DecodeConfig(bytes.NewReader(decoded))
				if err != nil || cfg.Width != 32 || cfg.Height != 2048 {
					t.Fatalf("Runtime did not prepare historical tool image: %v (%dx%d)", err, cfg.Width, cfg.Height)
				}
			}
		}
	}
	if images != 7 {
		t.Fatalf("model image count=%d, want seven readable crops", images)
	}
	var logPath string
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && strings.HasSuffix(path, ".events.jsonl") {
			logPath = path
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil || bytes.Count(raw, []byte(data)) != 1 {
		t.Fatalf("runtime media was not shared on disk: %v", err)
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, event := range events {
		if event.Journal == nil || event.Journal.ToolExecution == nil || event.Journal.ToolExecution.Status != session.ToolExecutionSucceeded {
			continue
		}
		terminal++
		var receipt tool.Result
		if err := json.Unmarshal(event.Journal.ToolExecution.Result, &receipt); err != nil || !reflect.DeepEqual(receipt.Content, content) {
			t.Fatalf("recovery receipt lost original media: %v", err)
		}
	}
	if terminal != 1 {
		t.Fatalf("terminal tool receipts=%d, want 1", terminal)
	}
	// Continue with a new Runtime and Store. The model's next request must match
	// its actual pre-restart context plus the completed answer and new input.
	second := &mediaPersistenceModel{toolLoopRuntimeModel: toolLoopRuntimeModel{calls: 1}}
	run(sessionfile.NewStore(sessionfile.Config{RootDir: root}), second, "continue")
	want := append(model.CloneMessages(first.requests[1]), model.NewTextMessage(model.RoleAssistant, "pong"), model.NewTextMessage(model.RoleUser, "continue"))
	if len(second.requests) != 1 || !reflect.DeepEqual(second.requests[0], want) {
		t.Fatal("rebuilt model context differs from Runtime-produced context")
	}
	if calls != 1 {
		t.Fatalf("completed tool was replayed: %d calls", calls)
	}
}

type mediaPersistenceModel struct {
	toolLoopRuntimeModel
	requests [][]model.Message
}

func (*mediaPersistenceModel) Capabilities() model.Capabilities {
	caps := runtimeTestModelCapabilities()
	caps.ImageInput = true
	return caps
}

func (m *mediaPersistenceModel) Generate(ctx context.Context, request *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.requests = append(m.requests, model.CloneMessages(request.Messages))
	return m.toolLoopRuntimeModel.Generate(ctx, request)
}
