package gatewayapp_test

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
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	appstream "github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/control/streamspool"
	spoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
	"github.com/caelis-labs/caelis/control/taskstream"
)

type scriptStreamModel struct {
	calls   atomic.Int32
	entered <-chan struct{}
	settled func(context.Context) error
}

func (*scriptStreamModel) Capabilities() model.Capabilities {
	return model.Capabilities{ToolCalls: true, Streaming: true}
}
func (*scriptStreamModel) Name() string { return "local-task-fixture" }
func (m *scriptStreamModel) Generate(ctx context.Context, _ *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		n := m.calls.Add(1)
		response := &model.Response{Message: model.NewTextMessage(model.RoleAssistant, "done"), TurnComplete: true}
		if n == 1 {
			response = &model.Response{Message: model.NewMessage(model.RoleAssistant, model.NewToolUsePart("script", "js_exec", []byte(`{"code":"return await Hold({});","yield_time_ms":0}`)))}
		}
		if n == 2 {
			// Do not end the model Turn before the yielded worker has entered
			// its controlled Host wait. This is a late-success observation test,
			// not a race against the original TurnComplete admission boundary.
			select {
			case <-m.entered:
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			}
			response = &model.Response{Message: model.NewMessage(model.RoleAssistant, model.NewToolUsePart("release", "Release", []byte(`{}`)))}
		}
		if n == 3 {
			// Keep the original Run open until its producer has persisted the
			// terminal result. TurnComplete may legitimately cancel unfinished
			// work, even after Hold has returned to the bridge.
			if err := m.settled(ctx); err != nil {
				yield(nil, err)
				return
			}
		}
		response.TurnComplete = true
		response.StepComplete = true
		response.Status = model.ResponseStatusCompleted
		response.FinishReason = model.FinishReasonStop
		if n < 3 {
			response.FinishReason = model.FinishReasonToolCalls
		}
		yield(model.StreamEventFromResponse(response), nil)
	}
}

type scriptStreamAuth struct{}

func (scriptStreamAuth) AuthorizeTaskStream(context.Context, taskstream.Principal, string) error {
	return nil
}

func TestScriptRuntimeTaskTerminalWithoutTaskRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root, Attachments: true})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "fixture", Workspace: session.WorkspaceRef{Key: root, CWD: root}})
	if err != nil {
		t.Fatal(err)
	}
	tasks := sessionfile.NewTaskStore(store)
	spoolRoot := t.TempDir()
	spool, err := spoolfile.New(ctx, spoolfile.Config{RootDir: spoolRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	recorder := taskstream.NewRecorder(spool, nil)
	defer recorder.Close(ctx)
	policies, err := policy.NewMemory(policy.NamedMode{ID: "allow", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAllow}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	rt, err := runtime.New(runtime.Config{Sessions: store, TaskStore: tasks, TaskOutput: recorder, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: policies}, DefaultPolicyMode: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatal(err)
	}
	picture := append(encoded.Bytes(), make([]byte, 32<<10)...)
	base64Picture := base64.StdEncoding.EncodeToString(picture)
	release := make(chan struct{})
	entered := make(chan struct{})
	hold := tool.NamedTool{Def: tool.Definition{Name: "Hold", InputSchema: map[string]any{"type": "object"}}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		close(entered)
		select {
		case <-release:
			return tool.Result{Content: []tool.ContentBlock{tool.Image{Data: picture, MIMEType: "image/png"}}}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	next := tool.NamedTool{Def: tool.Definition{Name: "Release", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) { close(release); return tool.Result{}, nil }}
	settled := func(ctx context.Context) error {
		for {
			entries, err := tasks.ListSession(ctx, active.SessionRef)
			if err != nil {
				return err
			}
			if len(entries) == 1 && !entries[0].Running {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	run, err := rt.Run(ctx, agent.RunRequest{SessionRef: active.SessionRef, Input: "fixture", AgentSpec: agent.AgentSpec{Model: &scriptStreamModel{entered: entered, settled: settled}, Tools: []tool.Tool{hold, next, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Handle.WaitCompletion(ctx); err != nil {
		t.Fatal(err)
	}
	entries, err := tasks.ListSession(ctx, active.SessionRef)
	if err != nil || len(entries) != 1 || entries[0].Running {
		t.Fatalf("unsettled original Task: %+v %v", entries, err)
	}
	service, err := taskstream.New(taskstream.Config{Tasks: tasks, Spool: spool, Recorder: recorder, Sessions: store, Authorizer: scriptStreamAuth{}, Secret: []byte(strings.Repeat("x", 32))})
	if err != nil {
		t.Fatal(err)
	}
	// Late observation reads the real producer spool. It is not a Task.read call
	// and must preserve both parent completion and the selected business output.
	delivery := appstream.NewWithAttachments(service, store)
	response, err := delivery.Events(ctx, taskstream.Principal{}, taskstream.ReadRequest{SessionID: active.SessionID, TaskID: entries[0].TaskID})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, delivery := range response.Deliveries {
		for _, env := range delivery.Events {
			if update, ok := env.Update.(eventstream.ToolCallUpdate); ok && update.ToolCallID == "script" && update.Status != nil && *update.Status == "completed" {
				found = true
				if delivery.Source != appstream.SourceExact {
					t.Fatalf("expected the original Task spool record, got %s", delivery.Source)
				}
				if update.Meta["code_mode_script_completion"] != "completed" || !taskDeliveryContains(t, update, base64Picture) {
					t.Fatalf("terminal producer frame lost selected result: %+v", update)
				}
			}
		}
	}
	if !found {
		t.Fatalf("late Control observer never received original script terminal without Task.read: tasks=%+v response=%+v", entries, response)
	}
	key, bounds, err := spool.Resolve(ctx, streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings(active.SessionID, entries[0].TaskID)})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := spool.Reader(ctx, key, bounds.Low)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var storedReference bool
	for offset := bounds.Low; offset < bounds.High; offset++ {
		record, err := reader.Next(ctx)
		if err != nil {
			t.Fatal(err)
		}
		storedReference = storedReference || bytes.Contains(record.Payload, []byte("instance"))
		if bytes.Contains(record.Payload, []byte(base64Picture)) {
			t.Fatal("Task spool record contains inline media")
		}
	}
	var spoolBytes int
	if err := filepath.WalkDir(spoolRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || entry.Name() == "owner.lock" {
			return err
		}
		raw, err := os.ReadFile(path)
		spoolBytes += len(raw)
		if bytes.Contains(raw, []byte(base64Picture)) || bytes.Contains(raw, picture) {
			t.Error("Task spool persisted the image body")
		}
		return err
	}); err != nil || !storedReference || spoolBytes == 0 || spoolBytes >= len(picture) {
		t.Fatalf("spool bytes=%d image bytes=%d err=%v", spoolBytes, len(picture), err)
	}
	t.Logf("Task spool=%d bytes, attachment=%d bytes", spoolBytes, len(picture))
	if _, err := store.PruneAttachments(ctx, active.SessionID, time.Now().Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	response, err = delivery.Events(ctx, taskstream.Principal{}, taskstream.ReadRequest{SessionID: active.SessionID, TaskID: entries[0].TaskID})
	if err != nil || !taskDeliveryContains(t, response, "[Image attachment unavailable]") || taskDeliveryContains(t, response, base64Picture) {
		t.Fatalf("pruned Task replay lost its placeholder, err=%v", err)
	}
}

func taskDeliveryContains(t *testing.T, value any, want string) bool {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Contains(raw, []byte(want))
}
