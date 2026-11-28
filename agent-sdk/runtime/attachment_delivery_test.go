package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

type attachmentWireTransport func(*http.Request) (*http.Response, error)

func (f attachmentWireTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAttachmentCodeModeWireAndSameGenerationHandles(t *testing.T) {
	root := t.TempDir()
	store := newLogicalFileStore(t, sessionfile.Config{RootDir: root, Attachments: true})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	images := make([][]byte, 3)
	for i := range images {
		pixel := image.NewNRGBA(image.Rect(0, 0, 1, 1))
		pixel.Set(0, 0, color.NRGBA{R: uint8(i + 1), A: 255})
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, pixel); err != nil {
			t.Fatal(err)
		}
		images[i] = append(encoded.Bytes(), make([]byte, 32<<10)...)
	}
	effects := 0
	picture := tool.NamedTool{Def: tool.Definition{Name: "Picture", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		image := images[effects%len(images)]
		effects++
		return tool.Result{Content: []tool.ContentBlock{tool.Image{Data: image, MIMEType: "image/png", Name: "screen"}}}, nil
	}}
	var bodies [][]byte
	transport := attachmentWireTransport(func(req *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		bodies = append(bodies, data)
		n := len(bodies)
		output := []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": "done"}}}}
		code := map[int]string{
			1: `globalThis.saved=[]; for(let i=0;i<3;i++) saved.push((await tools.call("Picture",{})).media[0]); return 42;`,
			3: `return {selected:saved[2]};`,
		}[n]
		if code != "" {
			args, _ := json.Marshal(map[string]string{"code": code})
			output = []any{map[string]any{"type": "function_call", "name": codemode.ToolName, "call_id": fmt.Sprintf("script-%d", n), "arguments": string(args)}}
		}
		raw, err := json.Marshal(map[string]any{"id": fmt.Sprintf("fixture-%d", n), "object": "response", "model": "fixture", "status": "completed", "output": output, "usage": map[string]int{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}})
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(raw)), Request: req}, err
	})
	llm, err := providers.New(providers.Config{Provider: "fixture", API: model.APIOpenAIResponses, Model: "fixture", BaseURL: "https://example.invalid", HTTPClient: &http.Client{Transport: transport}, Auth: providers.AuthConfig{Type: model.AuthNone}, ImageInput: true, ContextWindowTokens: 200000})
	if err != nil {
		t.Fatal(err)
	}
	spec := agent.AgentSpec{Model: llm, Tools: []tool.Tool{picture, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}, Limits: codemode.Limits{RetainedBytes: 6}}}}
	cache := model.NewImagePreparationCache()
	newRuntime := func(store *sessionfile.Store) *Runtime {
		t.Helper()
		rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), ImageCache: cache, AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
		return rt
	}
	run := func(rt *Runtime, input string) {
		t.Helper()
		run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: input, AgentSpec: spec})
		if err != nil {
			t.Fatal(err)
		}
		if err = run.Handle.WaitCompletion(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	rt := newRuntime(store)
	run(rt, "three screenshots with numeric output")
	// Put the original lookup into the file Store's bounded append index,
	// then exceed its eight-Session capacity before returning the saved
	// image. The language generation and the old object stay alive.
	page, err := store.EventsPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable})
	if err != nil {
		t.Fatal(err)
	}
	var original string
	for _, event := range page.Events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == "Picture" {
			original = event.ID
		}
	}
	if receipt, err := store.EventReceipt(t.Context(), active.SessionRef, original); err != nil || receipt == nil {
		t.Fatalf("could not warm the original receipt index: %v", err)
	}
	for i := range 9 {
		ff, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", PreferredSessionID: fmt.Sprintf("index-eviction-%d", i), Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
		if err != nil {
			t.Fatal(err)
		}
		msg := model.NewTextMessage(model.RoleUser, "fill the receipt lookup")
		written, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: ff.SessionRef, Event: &session.Event{Type: session.EventTypeUser, Message: &msg}})
		if err != nil {
			t.Fatal(err)
		}
		if receipt, err := store.EventReceipt(t.Context(), ff.SessionRef, written.ID); err != nil || receipt == nil {
			t.Fatalf("append index fixture %d: %v", i, err)
		}
	}
	run(rt, "return an old selected image")
	reopened := newLogicalFileStore(t, sessionfile.Config{RootDir: root, Attachments: true})
	run(newRuntime(reopened), "read the same history without tool effects")
	imageMarker := []byte(`"image_url":"data:image/png;base64,`)
	if len(bodies) != 5 || bytes.Count(bodies[1], imageMarker) != 0 || bytes.Count(bodies[3], imageMarker) != 1 || bytes.Count(bodies[4], imageMarker) != 1 || effects != 3 {
		for i, b := range bodies {
			var req struct{ Input []map[string]any }
			_ = json.Unmarshal(b, &req)
			for _, item := range req.Input {
				if item["type"] == "function_call_output" {
					t.Logf("request %d output %.500s", i, fmt.Sprint(item["output"]))
				}
			}
		}
		t.Fatalf("requests=%d effects=%d, expected 0/1/1 provider images", len(bodies), effects)
	}
	encoded := base64.StdEncoding.EncodeToString(images[2])
	if !bytes.Contains(bodies[3], []byte(encoded)) || !bytes.Contains(bodies[4], []byte(encoded)) {
		t.Fatal("selected and replayed images were not the original bytes")
	}
	tasks, err := sessionfile.NewTaskStore(reopened).ListSession(t.Context(), active.SessionRef)
	if err != nil || len(tasks) != 2 {
		t.Fatalf("stored Tasks=%d err=%v", len(tasks), err)
	}
	var resultBytes int
	for _, task := range tasks {
		raw := taskStringValue(task.Result["tool_result_json"])
		resultBytes += len(raw)
		if strings.Contains(raw, encoded) {
			t.Fatal("Task contains inline image")
		}
	}
	var logBytes int
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".events.jsonl") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(raw, []byte(active.SessionID)) {
			return nil
		}
		logBytes += len(raw)
		for _, img := range images {
			if bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(img))) {
				t.Error("Session JSONL contains inline image")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if logBytes == 0 || logBytes+resultBytes >= 48<<10 {
		t.Fatalf("unexpected Session/Task size %d/%d", logBytes, resultBytes)
	}
	t.Logf("Session JSONL=%d bytes, Task results=%d bytes, unique original images=%d bytes", logBytes, resultBytes, len(images[0])+len(images[1])+len(images[2]))
}

func TestAttachmentCodeModeNativeThirtyMiBImage(t *testing.T) {
	store := newLogicalFileStore(t, sessionfile.Config{RootDir: t.TempDir(), Attachments: true})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 30<<20)
	copy(data, pngBytes.Bytes())
	picture := tool.NamedTool{Def: tool.Definition{Name: "NativeImage", InputSchema: map[string]any{"type": "object"}}, Invoke: func(context.Context, tool.Call) (tool.Result, error) {
		return tool.Result{Content: []tool.ContentBlock{tool.Image{Data: data, MIMEType: "image/png"}}}, nil
	}}
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "native", Name: codemode.ToolName, Args: `{"code":"return await tools.call('NativeImage',{});"}`}}
		}
		return nil
	}}
	rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), ImageCache: model.NewImagePreparationCache(), AgentFactory: chat.Factory{}, Hooks: hook.Set{ToolPolicy: mcpFixturePolicy()}, DefaultPolicyMode: "mcp-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.ReleaseCodeModeEnvironments() }()
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "native only", AgentSpec: agent.AgentSpec{Model: codeModeImageModel{driver}, Tools: []tool.Tool{picture, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(driver.requests) != 2 {
		t.Fatalf("model requests=%d", len(driver.requests))
	}
	visible := errorDeliveryModelResult(t, driver.requests[1], "native")
	images := 0
	for _, part := range visible.Content {
		if part.Media != nil && part.Media.Source.Kind == model.MediaSourceInline {
			images++
		}
	}
	if visible.IsError || images != 1 {
		t.Fatalf("native result error=%v, model images=%d, content %.800s", visible.IsError, images, errorDeliveryJSON(t, visible.Content))
	}
	page, err := store.EventsPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageAllDurable})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range page.Events {
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.Name == "NativeImage" {
			if event.Tool.Outcome != session.ToolOutcomeSucceeded {
				t.Fatal("original native receipt not successful")
			}
			image := event.Tool.Result.Content[0].(tool.Image)
			if image.Attachment.Size != int64(len(data)) || len(image.Data) != 0 {
				t.Fatal("full native receipt was not referenced")
			}
			return
		}
	}
	t.Fatal("native nested receipt missing")
}

func TestAttachmentScriptYieldWaitAndReopen(t *testing.T) {
	root := t.TempDir()
	config := sessionfile.Config{RootDir: root, Attachments: true}
	store := newLogicalFileStore(t, config)
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user", Workspace: session.WorkspaceRef{CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	picture := tool.NamedTool{Def: tool.Definition{Name: "Picture"}, Invoke: func(ctx context.Context, _ tool.Call) (tool.Result, error) {
		select {
		case <-release:
			return tool.Result{Content: []tool.ContentBlock{tool.Image{Data: pngBytes.Bytes(), MIMEType: "image/png"}}}, nil
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}}
	var handle string
	driver := &mcpScriptModel{steps: func(i int, req *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: `{"code":"return await tools.call('Picture',{});","yield_time_ms":0}`}}
		case 1:
			handle = jsExecTaskHandle(req)
			if handle == "" {
				t.Error("missing yielded Task handle")
				return nil
			}
			close(release)
			return []model.ToolCall{{ID: "wait", Name: tasktool.ToolName, Args: fmt.Sprintf(`{"action":"wait","handle":%q,"yield_time_ms":5000}`, handle)}}
		}
		return nil
	}}
	runNativeOrdering(t, store, active, codeModeImageModel{driver}, []tool.Tool{picture, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, Config{})
	if len(driver.requests) != 3 {
		t.Fatalf("got %d model requests", len(driver.requests))
	}
	assertImage := func(req *model.Request, call string) {
		t.Helper()
		result := errorDeliveryModelResult(t, req, call)
		for _, part := range result.Content {
			if part.Media != nil && part.Media.Source.Data == base64.StdEncoding.EncodeToString(pngBytes.Bytes()) && !result.IsError {
				return
			}
		}
		t.Fatalf("missing image for %s: %.500s", call, errorDeliveryJSON(t, result.Content))
	}
	assertImage(driver.requests[2], "wait")
	reopened := newLogicalFileStore(t, config)
	tasks, err := sessionfile.NewTaskStore(reopened).ListSession(t.Context(), active.SessionRef)
	if err != nil || len(tasks) != 1 {
		t.Fatalf("reopened Tasks: %v, %v", tasks, err)
	}
	raw := taskStringValue(tasks[0].Result["tool_result_json"])
	var result tool.Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		t.Fatal(err)
	}
	img, ok := result.Content[0].(tool.Image)
	if !ok || len(img.Data) != 0 || img.Attachment.Instance == "" {
		t.Fatal("Task did not retain a lightweight image result")
	}
	readDriver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		if i == 0 {
			return []model.ToolCall{{ID: "read", Name: tasktool.ToolName, Args: fmt.Sprintf(`{"action":"wait","handle":%q,"yield_time_ms":0}`, handle)}}
		}
		return nil
	}}
	runNativeOrdering(t, reopened, active, codeModeImageModel{readDriver}, []tool.Tool{&codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}, Config{})
	if len(readDriver.requests) != 2 {
		t.Fatalf("reopened model requests=%d", len(readDriver.requests))
	}
	assertImage(readDriver.requests[0], "wait")
	next := errorDeliveryModelResult(t, readDriver.requests[1], "read")
	for _, part := range next.Content {
		if part.Media != nil {
			t.Fatal("reopened wait repeated already consumed media")
		}
	}
}
