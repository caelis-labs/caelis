package codex

import (
	"encoding/json"
	"errors"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"

	"github.com/caelis-labs/caelis/adapters/codex/internal/appserver"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TestRouteStreamsReasoningSectionsWithoutRepeatingCompletedItem(t *testing.T) {
	t.Parallel()

	route := &sessionRoute{
		state:    &sessionState{threadID: "thread-1"},
		seenItem: make(map[string]bool), toolOutput: make(map[string]bool),
	}
	for _, notification := range []appserver.Notification{
		{Method: "item/reasoning/summaryTextDelta", Params: json.RawMessage(`{"itemId":"reasoning-1","delta":"Inspecting"}`)},
		{Method: "item/reasoning/summaryPartAdded", Params: json.RawMessage(`{"itemId":"reasoning-1"}`)},
	} {
		updates, _, err := route.translateNotification(notification)
		if err != nil || len(updates) != 1 || updates[0].AgentThoughtChunk == nil {
			t.Fatalf("translate %s = %#v, err=%v", notification.Method, updates, err)
		}
	}
	updates, _, err := route.translateNotification(appserver.Notification{
		Method: "item/completed",
		Params: json.RawMessage(`{"item":{"id":"reasoning-1","type":"reasoning","summary":["Inspecting","the result"]}}`),
	})
	if err != nil || len(updates) != 0 {
		t.Fatalf("completed streamed reasoning = %#v, err=%v", updates, err)
	}

	sectionOnly := &sessionRoute{
		state:    &sessionState{threadID: "thread-1"},
		seenItem: make(map[string]bool), toolOutput: make(map[string]bool),
	}
	updates, _, err = sectionOnly.translateNotification(appserver.Notification{
		Method: "item/reasoning/summaryPartAdded",
		Params: json.RawMessage(`{"itemId":"reasoning-2"}`),
	})
	if err != nil || len(updates) != 1 || updates[0].AgentThoughtChunk == nil {
		t.Fatalf("standalone reasoning section = %#v, err=%v", updates, err)
	}
	updates, _, err = sectionOnly.translateNotification(appserver.Notification{
		Method: "item/completed",
		Params: json.RawMessage(`{"item":{"id":"reasoning-2","type":"reasoning","summary":["completed summary"]}}`),
	})
	if err != nil || len(updates) != 0 {
		t.Fatalf("completed section-only reasoning = %#v, err=%v", updates, err)
	}
}

func TestRouteStreamsTerminalOutputMetadataAndSparseCompletion(t *testing.T) {
	t.Parallel()

	route := &sessionRoute{
		state:    &sessionState{threadID: "thread-1"},
		seenItem: make(map[string]bool), toolOutput: make(map[string]bool),
	}
	updates, _, err := route.translateNotification(appserver.Notification{
		Method: "item/started",
		Params: json.RawMessage(`{"item":{"id":"command-1","type":"commandExecution","command":"printf ok","cwd":"/workspace","status":"inProgress"}}`),
	})
	if err != nil || len(updates) != 1 || updates[0].ToolCall == nil {
		t.Fatalf("command start = %#v, err=%v", updates, err)
	}
	updates, _, err = route.translateNotification(appserver.Notification{
		Method: "item/commandExecution/outputDelta",
		Params: json.RawMessage(`{"itemId":"command-1","delta":"ACP_TOOL_RESULT_42\n"}`),
	})
	if err != nil || len(updates) != 1 || updates[0].ToolCallUpdate == nil {
		t.Fatalf("output delta = %#v, err=%v", updates, err)
	}
	if raw := updates[0].ToolCallUpdate.Meta[metautil.TerminalOutputDeltaKey]; !json.Valid(raw) ||
		!jsonContainsString(raw, "data", "ACP_TOOL_RESULT_42\n") {
		t.Fatalf("output delta metadata = %#v", updates[0].ToolCallUpdate.Meta)
	}
	updates, _, err = route.translateNotification(appserver.Notification{
		Method: "item/commandExecution/terminalInteraction",
		Params: json.RawMessage(`{"itemId":"command-1","stdin":"yes"}`),
	})
	if err != nil || len(updates) != 1 || updates[0].ToolCallUpdate == nil {
		t.Fatalf("terminal interaction = %#v, err=%v", updates, err)
	}
	if raw := updates[0].ToolCallUpdate.Meta[metautil.TerminalOutputDeltaKey]; !jsonContainsString(raw, "data", "\nyes\n") {
		t.Fatalf("terminal interaction metadata = %#v", updates[0].ToolCallUpdate.Meta)
	}

	updates, _, err = route.translateNotification(appserver.Notification{
		Method: "item/completed",
		Params: json.RawMessage(`{"item":{"id":"command-1","type":"commandExecution","command":"printf ok","cwd":"/workspace","aggregatedOutput":"ACP_TOOL_RESULT_42\n","exitCode":0,"status":"completed"}}`),
	})
	if err != nil || len(updates) != 1 || updates[0].ToolCallUpdate == nil ||
		updates[0].ToolCallUpdate.Status == nil || *updates[0].ToolCallUpdate.Status != acp.ToolCallStatusCompleted {
		t.Fatalf("command completion = %#v, err=%v", updates, err)
	}
	if _, duplicated := updates[0].ToolCallUpdate.Meta[metautil.TerminalOutputDeltaKey]; duplicated {
		t.Fatalf("completion repeated streamed output: %#v", updates[0].ToolCallUpdate.Meta)
	}
	if raw := updates[0].ToolCallUpdate.Meta[metautil.TerminalExitKey]; !json.Valid(raw) {
		t.Fatalf("terminal exit metadata = %#v", updates[0].ToolCallUpdate.Meta)
	}
}

func TestRouteNegotiatesCanonicalAndLegacyTerminalOutputWireKeys(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		mode    terminalOutputMode
		wantKey string
		dropKey string
	}{
		{name: "canonical", mode: terminalOutputCanonical, wantKey: metautil.TerminalOutputKey, dropKey: metautil.TerminalOutputDeltaKey},
		{name: "legacy", mode: terminalOutputLegacy, wantKey: metautil.TerminalOutputDeltaKey, dropKey: metautil.TerminalOutputKey},
	} {
		t.Run(test.name, func(t *testing.T) {
			route := &sessionRoute{
				agent: &agent{terminalMode: test.mode}, state: &sessionState{threadID: "thread-1"},
				seenItem: make(map[string]bool), startedTool: make(map[string]bool), toolOutput: make(map[string]bool),
			}
			_, _, err := route.translateNotification(appserver.Notification{
				Method: "item/started",
				Params: json.RawMessage(`{"item":{"id":"command-1","type":"commandExecution","command":"printf ok","cwd":"/workspace","status":"inProgress"}}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			updates, _, err := route.translateNotification(appserver.Notification{
				Method: "item/commandExecution/outputDelta",
				Params: json.RawMessage(`{"itemId":"command-1","delta":"wire output\n"}`),
			})
			if err != nil || len(updates) != 1 || updates[0].ToolCallUpdate == nil {
				t.Fatalf("output delta = %#v, err=%v", updates, err)
			}
			wire, err := json.Marshal(updates[0])
			if err != nil {
				t.Fatal(err)
			}
			var encoded map[string]any
			if err := json.Unmarshal(wire, &encoded); err != nil {
				t.Fatal(err)
			}
			meta, _ := encoded["_meta"].(map[string]any)
			if _, ok := meta[test.wantKey]; !ok {
				t.Fatalf("wire metadata = %#v, want %q", meta, test.wantKey)
			}
			if _, ok := meta[test.dropKey]; ok {
				t.Fatalf("wire metadata = %#v, unexpectedly retained %q", meta, test.dropKey)
			}
		})
	}
}

func TestRouteCompletionOnlyToolPublishesCompleteSnapshot(t *testing.T) {
	t.Parallel()

	route := &sessionRoute{
		state:    &sessionState{threadID: "thread-1"},
		seenItem: make(map[string]bool), startedTool: make(map[string]bool), toolOutput: make(map[string]bool),
	}
	updates, _, err := route.translateNotification(appserver.Notification{
		Method: "item/completed",
		Params: json.RawMessage(`{"item":{"id":"image-1","type":"imageGeneration","status":"completed","result":{"path":"/tmp/image.png"}}}`),
	})
	if err != nil || len(updates) != 1 || updates[0].ToolCall == nil {
		t.Fatalf("completion-only image generation = %#v, err=%v", updates, err)
	}
	if updates[0].ToolCall.Title != "Generate image" || updates[0].ToolCall.Kind != acp.ToolKindOther ||
		updates[0].ToolCall.Status != acp.ToolCallStatusCompleted || updates[0].ToolCall.RawOutput == nil {
		t.Fatalf("completion-only snapshot = %#v", updates[0].ToolCall)
	}

	updates, _, err = route.translateNotification(appserver.Notification{
		Method: "item/completed",
		Params: json.RawMessage(`{"item":{"id":"change-1","type":"fileChange","status":"completed","changes":[{"path":"/workspace/main.go","kind":"update"}]}}`),
	})
	if err != nil || len(updates) != 1 || updates[0].ToolCall == nil {
		t.Fatalf("completion-only file change = %#v, err=%v", updates, err)
	}
	if updates[0].ToolCall.Title != "Apply file changes" || updates[0].ToolCall.Kind != acp.ToolKindEdit ||
		len(updates[0].ToolCall.Locations) != 1 || updates[0].ToolCall.Locations[0].Path != "/workspace/main.go" {
		t.Fatalf("completion-only file presentation = %#v", updates[0].ToolCall)
	}
}

func TestRouteCompletionWithoutTerminalStartRetainsCanonicalAggregate(t *testing.T) {
	t.Parallel()

	route := &sessionRoute{
		agent: &agent{terminalMode: terminalOutputCanonical}, state: &sessionState{threadID: "thread-1"},
		seenItem: make(map[string]bool), startedTool: make(map[string]bool), toolOutput: make(map[string]bool),
	}
	updates, _, err := route.translateNotification(appserver.Notification{
		Method: "item/commandExecution/outputDelta",
		Params: json.RawMessage(`{"itemId":"command-1","delta":"partial\n"}`),
	})
	if err != nil || len(updates) != 1 || updates[0].ToolCallUpdate == nil {
		t.Fatalf("pre-start output = %#v, err=%v", updates, err)
	}
	if _, ok := updates[0].ToolCallUpdate.Meta[metautil.TerminalOutputDeltaKey]; !ok {
		t.Fatalf("pre-start output did not use compatibility key: %#v", updates[0].ToolCallUpdate.Meta)
	}
	updates, _, err = route.translateNotification(appserver.Notification{
		Method: "item/completed",
		Params: json.RawMessage(`{"item":{"id":"command-1","type":"commandExecution","command":"printf partial","cwd":"/workspace","aggregatedOutput":"partial\n","exitCode":0,"status":"completed"}}`),
	})
	if err != nil || len(updates) != 1 || updates[0].ToolCall == nil {
		t.Fatalf("completion-only command = %#v, err=%v", updates, err)
	}
	if _, ok := updates[0].ToolCall.Meta[metautil.TerminalInfoKey]; !ok {
		t.Fatalf("completion snapshot omitted terminal info: %#v", updates[0].ToolCall.Meta)
	}
	if raw := updates[0].ToolCall.Meta[metautil.TerminalOutputKey]; !jsonContainsString(raw, "data", "partial\n") {
		t.Fatalf("completion snapshot omitted canonical aggregate: %#v", updates[0].ToolCall.Meta)
	}
}

func TestRouteIgnoresStandaloneProcessOutputDelta(t *testing.T) {
	t.Parallel()

	route := &sessionRoute{
		state:    &sessionState{threadID: "thread-1"},
		seenItem: make(map[string]bool), toolOutput: make(map[string]bool),
	}
	updates, terminal, err := route.translateNotification(appserver.Notification{
		Method: "command/exec/outputDelta",
		Params: json.RawMessage(`{"processId":"process-1","stream":"stdout","deltaBase64":"b2sK"}`),
	})
	if err != nil || terminal != nil || len(updates) != 0 {
		t.Fatalf("standalone process delta = updates=%#v terminal=%#v err=%v, want ignored", updates, terminal, err)
	}
}

func TestRouteProjectsPlanAndUsageUpdates(t *testing.T) {
	t.Parallel()

	route := &sessionRoute{
		state:    &sessionState{threadID: "thread-1"},
		seenItem: make(map[string]bool), toolOutput: make(map[string]bool),
	}
	updates, _, err := route.translateNotification(appserver.Notification{
		Method: "turn/plan/updated",
		Params: json.RawMessage(`{"plan":[{"step":"inspect","status":"inProgress"},{"step":"report","status":"pending"}]}`),
	})
	if err != nil || len(updates) != 1 || updates[0].Plan == nil || len(updates[0].Plan.Entries) != 2 ||
		updates[0].Plan.Entries[0].Status != acp.PlanEntryStatusInProgress {
		t.Fatalf("plan update = %#v, err=%v", updates, err)
	}

	updates, _, err = route.translateNotification(appserver.Notification{
		Method: "thread/tokenUsage/updated",
		Params: json.RawMessage(`{"tokenUsage":{"last":{"totalTokens":4200},"modelContextWindow":200000}}`),
	})
	if err != nil || len(updates) != 1 || updates[0].UsageUpdate == nil ||
		updates[0].UsageUpdate.Used != 4200 || updates[0].UsageUpdate.Size != 200000 {
		t.Fatalf("usage update = %#v, err=%v", updates, err)
	}
}

func jsonContainsString(raw json.RawMessage, key, want string) bool {
	var values map[string]any
	if json.Unmarshal(raw, &values) != nil {
		return false
	}
	got, _ := values[key].(string)
	return got == want
}

func TestBufferedRouteSplicesOnlyPostBarrierNotifications(t *testing.T) {
	route := &sessionRoute{mode: routeBuffering, closed: make(chan struct{}), seenItem: make(map[string]bool)}
	route.enqueue(appserver.Notification{Sequence: 1, Method: "before"})
	route.enqueue(appserver.Notification{Sequence: 2, Method: "after"})
	post := route.acceptStableBarrier(1)
	if len(post) != 1 || post[0].Sequence != 2 {
		t.Fatalf("post-barrier batch = %#v", post)
	}

	route.enqueue(appserver.Notification{Sequence: 3, Method: "during-drain"})
	batch, live, err := route.takeBufferedOrSwitchLive()
	if err != nil {
		t.Fatal(err)
	}
	if live || len(batch) != 1 || batch[0].Sequence != 3 {
		t.Fatalf("drain batch = %#v, live=%t", batch, live)
	}
	batch, live, err = route.takeBufferedOrSwitchLive()
	if err != nil {
		t.Fatal(err)
	}
	if !live || len(batch) != 0 || route.mode != routeLive {
		t.Fatalf("final drain = %#v, live=%t, mode=%v", batch, live, route.mode)
	}
}

func TestClosedBufferedRouteReturnsItsFailure(t *testing.T) {
	route := &sessionRoute{mode: routeBuffering, closed: make(chan struct{}), seenItem: make(map[string]bool), state: &sessionState{}}
	want := errors.New("route failed")
	route.close(want)

	_, live, err := route.takeBufferedOrSwitchLive()
	if live || !errors.Is(err, want) {
		t.Fatalf("closed drain: live=%t err=%v, want %v", live, err, want)
	}
}
