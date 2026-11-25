package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/hook"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox/host"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/filesystem"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/plan"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode/gojaexec"
)

func TestRuntimePlanPointerPublishesPlan(t *testing.T) {
	store, active, _ := codeModeStore(t)
	driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
		switch i {
		case 0:
			return []model.ToolCall{{ID: "plan", Name: plan.ToolName, Args: `{"entries":[{"content":"Check the output","status":"in_progress"}],"explanation":"Verify the result"}`}}
		case 1:
			return []model.ToolCall{{ID: "invalid", Name: plan.ToolName, Args: `{"entries":[{"content":"First","status":"in_progress"},{"content":"Second","status":"in_progress"}]}`}}
		case 2:
			return []model.ToolCall{{ID: "clear", Name: plan.ToolName, Args: `{"entries":[],"explanation":"Clear the list"}`}}
		}
		return nil
	}}
	events, _ := runCodeMode(t, store, active, agent.AgentSpec{Model: driver, Tools: []tool.Tool{&plan.Tool{}}}, nil, nil)
	modelResult := driver.requests[1].Messages[len(driver.requests[1].Messages)-1].ToolResults()
	if len(modelResult) != 1 || len(modelResult[0].Content) != 1 || modelResult[0].Content[0].JSON == nil ||
		string(modelResult[0].Content[0].JSON.Value) != `{"updated":true}` {
		t.Fatalf("model-visible Plan result = %+v", modelResult)
	}
	var updates []*session.Event
	for _, event := range events {
		if event.PlanPayload != nil {
			updates = append(updates, event)
		}
	}
	if len(updates) != 2 {
		t.Fatalf("got %d Plan updates, want the initial list and explicit clear", len(updates))
	}
	want := []session.EventPlanEntry{{Content: "Check the output", Status: "in_progress", Priority: "medium"}}
	if !reflect.DeepEqual(updates[0].PlanPayload.Entries, want) || updates[0].Text != "Verify the result" {
		t.Fatalf("plan display = %+v, text = %q", updates[0].PlanPayload, updates[0].Text)
	}
	if len(updates[1].PlanPayload.Entries) != 0 || updates[1].Text != "Clear the list" {
		t.Fatalf("plan clear = %+v, text = %q", updates[1].PlanPayload, updates[1].Text)
	}
}

func TestRuntimeIgnoresPlanPresentationWithoutBuiltinDispatch(t *testing.T) {
	store, active, _ := codeModeStore(t)
	core, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	planDisplay := map[string]any{
		"updated": true,
		"entries": []map[string]any{{"content": "Presentational only", "status": "in_progress"}},
	}
	result := &session.Event{
		Type: session.EventTypeToolResult,
		Tool: &session.EventTool{ID: "custom-plan", Name: plan.ToolName, Status: "completed", Outcome: session.ToolOutcomeSucceeded},
		Meta: map[string]any{"caelis": map[string]any{"runtime": map[string]any{"tool": planDisplay}}},
	}
	run, err := core.Run(t.Context(), agent.RunRequest{
		SessionRef: active.SessionRef,
		Input:      "show a custom tool result",
		Agent:      seqAgent{events: []*session.Event{result}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Handle.WaitCompletion(t.Context()); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	var sawResult bool
	for _, event := range loaded.Events {
		if event.Type == session.EventTypePlan {
			t.Fatalf("presentation created a durable Plan event: %+v", event)
		}
		if event.Type == session.EventTypeToolResult && event.Tool != nil && event.Tool.ID == result.Tool.ID {
			sawResult = true
		}
	}
	if !sawResult {
		t.Fatal("custom Plan-named tool result was not committed")
	}
}

func TestCodeModeMutationDisplayAndModelReplay(t *testing.T) {
	for _, mode := range []string{"unmodified", "invoke-redacted", "result-redacted", "result-failed", "view-failed"} {
		t.Run(mode, func(t *testing.T) {
			const before = "fixture-preimage-only"
			store, active, root := codeModeStore(t)
			backend, err := host.New(host.Config{CWD: root})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = backend.Close() })
			write, err := filesystem.NewWrite(backend)
			if err != nil {
				t.Fatal(err)
			}
			patch, err := filesystem.NewPatch(backend)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "write.txt")
			if err := os.WriteFile(path, []byte(before+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "patch.txt"), []byte("line one\nline two\n"), 0600); err != nil {
				t.Fatal(err)
			}
			hooks := mutationDisplayHooks(mode)
			hooks.ToolPolicy = mcpFixturePolicy()
			rt, err := New(Config{Sessions: store, TaskStore: sessionfile.NewTaskStore(store), AgentFactory: chat.Factory{}, Hooks: hooks,
				DefaultPolicyMode: "mcp-test"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = rt.ReleaseCodeModeEnvironments() })
			code := `
const write = await Write({path: "write.txt", content: "replacement\nsecond\n"});
const patch = await Patch({path: "patch.txt", edits: [{old: "line two", new: "new line"}]});
for (const {data} of [write, patch]) {
  if ("diff_hunks" in data || "added_lines" in data || "removed_lines" in data) throw Error("display leaked to JS");
}
return {write: write.data, patch: patch.data};`
			args, err := json.Marshal(map[string]any{"code": code})
			if err != nil {
				t.Fatal(err)
			}
			driver := &mcpScriptModel{steps: func(i int, _ *model.Request) []model.ToolCall {
				if i == 0 {
					return []model.ToolCall{{ID: "script", Name: codemode.ToolName, Args: string(args)}}
				}
				return nil
			}}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "apply fixture edits",
				AgentSpec: agent.AgentSpec{Model: driver, Tools: []tool.Tool{write, patch, &codemode.Tool{Name: codemode.ToolName, Executor: &gojaexec.Executor{}}}}})
			if err != nil {
				t.Fatal(err)
			}
			completionErr := run.Handle.WaitCompletion(t.Context())
			if mode == "unmodified" && completionErr != nil {
				t.Fatal(completionErr)
			}
			if raw, err := os.ReadFile(path); err != nil || string(raw) != "replacement\nsecond\n" {
				t.Fatalf("Write effect = %q, error = %v", raw, err)
			}
			if mode == "unmodified" {
				if raw, err := os.ReadFile(filepath.Join(root, "patch.txt")); err != nil || string(raw) != "line one\nnew line\n" {
					t.Fatalf("Patch effect = %q, error = %v", raw, err)
				}
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: filepath.Join(root, "sessions")})
			events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
			if err != nil {
				t.Fatal(err)
			}
			pairs, err := session.PairToolFacts(events)
			if err != nil {
				t.Fatal(err)
			}
			var writes, patches int
			for _, pair := range pairs {
				child := pair.Result
				if child == nil || child.Tool.Parent == nil {
					continue
				}
				switch child.Tool.Name {
				case filesystem.WriteToolName:
					writes++
					assertNestedMutationDisplay(t, child, mode == "unmodified", before)
				case filesystem.PatchToolName:
					patches++
					assertNestedMutationDisplay(t, child, true, "line two")
				}
			}
			if writes != 1 || (mode == "unmodified" && patches != 1) {
				t.Fatalf("durable outcomes Write=%d Patch=%d", writes, patches)
			}
			entries, err := rt.tasks.store.ListSession(t.Context(), active.SessionRef)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].Kind != task.KindCodeMode {
				t.Fatalf("mutations gained Task authority: %+v", entries)
			}
			if mode != "unmodified" {
				return
			}
			live := errorDeliveryModelResult(t, driver.requests[1], "script")
			if live.IsError || bytes.Contains(mustMarshalHook(t, live), []byte(before)) {
				t.Fatalf("unexpected live model result: %+v", live)
			}
			replay := &mcpScriptModel{steps: func(int, *model.Request) []model.ToolCall { return nil }}
			runCodeMode(t, reopened, active, agent.AgentSpec{Model: replay}, nil, nil)
			want := append(model.CloneMessages(driver.requests[1].Messages), model.NewTextMessage(model.RoleAssistant, "done"))
			got := replay.requests[0].Messages
			if len(got) != len(want)+1 || !reflect.DeepEqual(got[:len(want)], want) {
				t.Fatalf("model replay changed the live context: %s", mustMarshalHook(t, got))
			}
		})
	}
}

func assertNestedMutationDisplay(t *testing.T, event *session.Event, wantDisplay bool, preimage string) {
	t.Helper()
	if event.Visibility != session.VisibilityMirror || event.Tool.Parent.CallID != "script" || event.Tool.Outcome != session.ToolOutcomeSucceeded {
		t.Fatalf("mutation lost identity or effect: %+v", event.Tool)
	}
	if nestedValue(event.Meta, "caelis", "runtime", "task") != nil || nestedValue(event.Meta, "caelis", "runtime", "binding", "task_result") == true {
		t.Fatalf("mutation gained Task result authority: %+v", event.Meta)
	}
	if got := bytes.Contains(mustMarshalHook(t, event.Meta), []byte(preimage)); got != wantDisplay {
		t.Fatalf("display of file preimage = %v, want %v: %+v", got, wantDisplay, event.Meta)
	}
	if got := nestedValue(event.Meta, "caelis", "runtime", "tool", "diff_hunks") != nil; got != wantDisplay {
		t.Fatalf("diff observation present = %v, want %v", got, wantDisplay)
	}
	if bytes.Contains(toolResultJSON(event.Tool.Result), []byte(preimage)) || bytes.Contains(mustMarshalHook(t, event.Message), []byte(preimage)) {
		t.Fatal("file preimage leaked to tool result or model message")
	}
}

func mutationDisplayHooks(mode string) hook.Set {
	redacted := tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(`{"redacted":true}`)}}}
	return hook.Set{
		ToolInvoke: []hook.ToolInvoke{{Name: "invoke", Apply: func(ctx context.Context, e hook.ToolContext, next hook.ToolNext) (tool.Result, error) {
			result, err := next(ctx)
			if err == nil && mode == "invoke-redacted" && e.Tool.Name == filesystem.WriteToolName {
				return redacted, nil
			}
			return result, err
		}}},
		ToolResult: []hook.ToolResult{{Name: "result", Apply: func(_ context.Context, e hook.ToolContext, outcome hook.ToolOutcome) (tool.Result, error) {
			if e.Tool.Name == filesystem.WriteToolName {
				switch mode {
				case "result-redacted":
					return redacted, nil
				case "result-failed":
					return tool.Result{}, errors.New("fixture result redactor failed")
				}
			}
			return outcome.Result, nil
		}}},
		ModelView: []hook.ModelView{{Name: "view", Apply: func(_ context.Context, e hook.ToolContext, message model.Message) (model.Message, error) {
			if mode == "view-failed" && e.Tool.Name == filesystem.WriteToolName {
				return model.Message{}, errors.New("fixture view redactor failed")
			}
			return message, nil
		}}},
	}
}

func nestedValue(values map[string]any, path ...string) any {
	var current any = values
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current = mapped[key]
	}
	return current
}
