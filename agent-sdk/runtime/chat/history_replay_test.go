package chat

import (
	"context"
	"encoding/json"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func messagesFromContext(ctx agent.Context) []model.Message {
	messages, _ := messagesFromContextChecked(ctx)
	return messages
}

func TestMessagesFromContextProjectsCompactSystemOverlayAsProviderUser(t *testing.T) {
	t.Parallel()

	event := &session.Event{
		Type:       session.EventTypeCompact,
		Visibility: session.VisibilityCanonical,
		Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: "runtime"},
		Text:       "CONTEXT CHECKPOINT\n\nRuntime-generated; non-authorizing.",
	}
	ctx := agent.NewContext(agent.ContextSpec{
		Context: context.Background(),
		Events:  []*session.Event{event},
	})
	messages := messagesFromContext(ctx)
	if len(messages) != 1 {
		t.Fatalf("messages = %#v, want one compact projection", messages)
	}
	if messages[0].Role != model.RoleUser || messages[0].TextContent() != event.Text {
		t.Fatalf("compact projection = %#v, want provider-compatible user text", messages[0])
	}
}

func TestAgentCommunicationDisplayRepairKeepsRuntimeAndReplayInput(t *testing.T) {
	ctx := t.Context()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	var produced []*session.Event
	var producedMessages []model.Message
	accepted, err := (&Agent{}).drainPendingSubmissions(agent.NewContext(agent.ContextSpec{Context: ctx, DrainSubmissions: func() []agent.Submission {
		return []agent.Submission{{Kind: agent.SubmissionKindAgentCommunication, Text: "admitted message", Actor: session.ParentCommunicationActor()}}
	}}), &producedMessages, func(event *session.Event) bool {
		produced = append(produced, session.CloneEvent(event))
		return true
	})
	if err != nil || !accepted || len(produced) != 1 {
		t.Fatalf("native mail production failed: %v %#v", err, produced)
	}
	admitted := produced[0]
	admitted.Protocol.Update.MessageID = "mail"
	live := []*session.Event{admitted}
	for _, text := range []string{"edited display", ""} {
		observed := session.CloneEvent(admitted)
		observed.Visibility, observed.Text = session.VisibilityMirror, text
		body := model.NewTextMessage(model.RoleUser, text)
		observed.Message = &body
		observed.Protocol.Update.Replacement = true
		observed.Protocol.Update.Content = map[string]any{"type": "text", "text": text}
		live = append(live, observed)
	}
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	invoke := func(events []*session.Event) []model.Message {
		t.Helper()
		provider := &recordingModel{}
		chat, err := New("chat", provider, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, err := range chat.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: events})) {
			if err != nil {
				t.Fatal(err)
			}
		}
		return provider.last.Messages
	}
	actual := invoke(live)
	loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	replayed := invoke(loaded.Events)
	if len(actual) != 1 || !strings.Contains(actual[0].TextContent(), "admitted message") || strings.Contains(actual[0].TextContent(), "edited display") || !reflect.DeepEqual(actual, replayed) || !reflect.DeepEqual(actual, producedMessages) {
		t.Fatalf("observed repair changed runtime/replay input: %#v / %#v", actual, replayed)
	}
	page, err := store.EventsPage(ctx, session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay})
	if err != nil || len(page.Events) != 3 {
		t.Fatalf("observed mail repair missing from replay: %v %#v", err, page)
	}
	for i, want := range []string{"admitted message", "edited display", ""} {
		communication := session.ProtocolAgentCommunicationOf(page.Events[i])
		if communication == nil || communication.Text != want || session.EventMessageID(page.Events[i]) != "mail" || page.Events[i].Actor != admitted.Actor {
			t.Fatalf("replayed message patch lost body/identity/source: %#v", page.Events[i])
		}
	}
}

func TestChildOriginAndToolProjectionSurviveFileReopenAndNextRequest(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	root := t.TempDir()
	parentStore := sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "parent-293" },
	})
	parent, err := parentStore.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatalf("StartSession(parent) error = %v", err)
	}
	childStore := sessionfile.NewStore(sessionfile.Config{
		RootDir:            root,
		SessionIDGenerator: func() string { return "child-293" },
	})
	child, err := childStore.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatalf("StartSession(child) error = %v", err)
	}

	childMessage := model.NewTextMessage(model.RoleAssistant, "child completed the delegated lookup")
	childEvent := &session.Event{
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityCanonical,
		Message:    &childMessage,
	}
	if _, err := childStore.AppendEvent(ctx, session.AppendEventRequest{SessionRef: child.SessionRef, Event: childEvent}); err != nil {
		t.Fatalf("AppendEvent(child) error = %v", err)
	}

	userMessage := model.NewTextMessage(model.RoleUser, "delegate a lookup and report the result")
	call := model.ToolCall{ID: "spawn-call-293", Name: "StartThread", Args: `{"task":"lookup"}`}
	assistantCall := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
	result := tool.Result{Content: []tool.ContentBlock{
		tool.StructuredContent{Value: json.RawMessage(`{"child_session_id":"child-293","status":"started"}`)},
	}}
	resultMessage := toolbinding.ResultMessage(call, result, nil)
	nestedParent := session.EventParentTool{
		Kind:      "code_mode",
		Execution: tool.InvocationContext{SessionID: parent.SessionID, TurnID: "turn-293", ItemID: "item-inner-293"},
		CallID:    "script-call-293",
		Name:      "js_exec",
	}
	resultEvent := toolResultEvent(call, result, nil, &resultMessage)
	resultEvent.Tool.Binding = &tool.Binding{Name: "StartThread", Identity: "participants", Version: "v1"}
	resultEvent.Tool.Parent = &nestedParent
	resultEvent.Tool.ProcessingFailure = &session.ToolProcessingFailure{Phase: "projection", Name: "renderer", Error: "recorded failure"}
	resultEvent.Tool.ProposedCall = &call

	childOrigin := &session.EventChildOrigin{
		Scope:         session.EventChildScopeSubagent,
		ScopeID:       "task-293",
		TaskID:        "task-293",
		DelegationID:  "delegation-293",
		ParticipantID: "participant-293",
		ACPSessionID:  "acp-child-293",
		SourceEventID: "child-event-293",
		ParentTool: session.EventParentTool{
			Kind:      "subagent",
			Execution: tool.InvocationContext{SessionID: parent.SessionID, TurnID: "turn-293", ItemID: "item-spawn-293"},
			CallID:    call.ID,
			Name:      call.Name,
		},
	}
	childMirror := &session.Event{
		ID:          "child-mirror-293",
		Type:        session.EventTypeAssistant,
		Visibility:  session.VisibilityMirror,
		ChildOrigin: childOrigin,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     "child-message-293",
				Content:       session.ProtocolTextContent(childMessage.TextContent()),
			},
		},
	}

	toolCallEvent := modelToolCallEvents(assistantCall, nil, "turn-293", nil)[0]
	nestedParentCopy := nestedParent
	toolCallEvent.Tool.Parent = &nestedParentCopy
	parentEvents := []*session.Event{
		{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &userMessage},
		toolCallEvent,
		resultEvent,
		childMirror,
	}
	assertToolProjectionIsolated(t, resultEvent)
	for _, event := range parentEvents {
		if _, err := parentStore.AppendEvent(ctx, session.AppendEventRequest{SessionRef: parent.SessionRef, Event: event}); err != nil {
			t.Fatalf("AppendEvent(parent, %s) error = %v", session.EventTypeOf(event), err)
		}
	}

	liveModel := &recordingModel{}
	runCloneReopenChat(t, ctx, parent, parentEvents, liveModel)

	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loadedParent, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: parent.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatalf("LoadSession(parent) error = %v", err)
	}
	loadedChild, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: child.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession(child) error = %v", err)
	}
	if len(loadedChild.Events) != 1 || session.EventText(loadedChild.Events[0]) != childMessage.TextContent() {
		t.Fatalf("reopened child events = %#v, want its persisted assistant event", loadedChild.Events)
	}

	var loadedResult *session.Event
	var loadedMirror *session.Event
	for _, event := range loadedParent.Events {
		switch {
		case event.Tool != nil && event.Type == session.EventTypeToolResult:
			loadedResult = event
		case event.ChildOrigin != nil:
			loadedMirror = event
		}
	}
	if loadedResult == nil {
		t.Fatalf("reopened parent events = %#v, missing tool result", loadedParent.Events)
	}
	if loadedMirror == nil || !reflect.DeepEqual(*loadedMirror.ChildOrigin, *childOrigin) {
		t.Fatalf("reopened child origin = %#v, want %#v", loadedMirror, childOrigin)
	}
	assertToolProjectionIsolated(t, loadedResult)

	replayModel := &recordingModel{}
	runCloneReopenChat(t, ctx, loadedParent.Session, loadedParent.Events, replayModel)
	if !reflect.DeepEqual(liveModel.last.Messages, replayModel.last.Messages) {
		t.Fatalf("next model request changed after reopen:\nlive:   %s\nreopen: %s", canonicalMessagesJSON(t, liveModel.last.Messages), canonicalMessagesJSON(t, replayModel.last.Messages))
	}
	if got, want := replayModel.last.Messages, []model.Message{
		userMessage,
		assistantCall,
		resultMessage,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened next model request messages = %s, want %s", canonicalMessagesJSON(t, got), canonicalMessagesJSON(t, want))
	}
}

func runCloneReopenChat(t *testing.T, ctx context.Context, active session.Session, events []*session.Event, model *recordingModel) {
	t.Helper()

	chatAgent, err := New("chat", model, "")
	if err != nil {
		t.Fatalf("New(chat) error = %v", err)
	}
	for _, err := range chatAgent.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: events})) {
		if err != nil {
			t.Fatalf("Chat.Run() error = %v", err)
		}
	}
}

func assertToolProjectionIsolated(t *testing.T, event *session.Event) {
	t.Helper()

	if event == nil || event.Tool == nil {
		t.Fatal("tool event is nil")
	}
	projection := session.EventToolProjection(event)
	if projection == nil || projection.Result == nil || projection.Binding == nil || projection.Parent == nil || projection.ProcessingFailure == nil || projection.ProposedCall == nil {
		t.Fatalf("EventToolProjection() = %#v, want all durable pointer fields", projection)
	}

	projection.Result.Content[0] = tool.StructuredContent{Value: json.RawMessage(`{"changed":true}`)}
	projection.Binding.Name = "changed-binding"
	projection.Parent.CallID = "changed-parent"
	projection.Parent.Execution.ItemID = "changed-item"
	projection.ProcessingFailure.Name = "changed-renderer"
	projection.ProposedCall.ID = "changed-proposal"

	if got := string(event.Tool.Result.Content[0].(tool.StructuredContent).Value); got != `{"child_session_id":"child-293","status":"started"}` {
		t.Fatalf("source result content = %s, want original JSON", got)
	}
	if event.Tool.Binding.Name != "StartThread" || event.Tool.Parent.CallID != "script-call-293" || event.Tool.Parent.Execution.ItemID != "item-inner-293" || event.Tool.ProcessingFailure.Name != "renderer" || event.Tool.ProposedCall.ID != "spawn-call-293" {
		t.Fatalf("projection mutation changed source tool event: %#v", event.Tool)
	}
}

func TestNativeStructuredDiffModelContextSurvivesFileStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	call := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "edit", Name: "Edit", Args: `{}`}}, "")
	changes := []session.ProtocolDiffChange{{Operation: "modify", Path: " exact file.txt "}}
	patch := &session.ProtocolDiffPatch{Format: "git_patch", Text: "-old\r\n+new\r\n"}
	diff := session.EventToolContent{Type: "diff", Changes: &changes, Patch: patch, Meta: map[string]any{"display": "exact"}}
	native := &session.Event{Type: session.EventTypeToolResult, Tool: &session.EventTool{ID: "edit", Name: "Edit", Status: "completed", Content: []session.EventToolContent{diff}}}
	mirror := &session.Event{Type: session.EventTypeToolResult, Visibility: session.VisibilityMirror, Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: "tool_call_update", ToolCallID: "remote", Content: []session.ProtocolToolCallContent{{Type: "diff", Changes: &changes, Patch: &session.ProtocolDiffPatch{Format: "git_patch", Text: "UNTRUSTED-MIRROR"}}}}}}
	live := []*session.Event{modelToolCallEvents(call, nil, "", nil)[0], native, mirror}
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	want := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: live}))
	loaded, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	got := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Session: loaded.Session, Events: loaded.Events}))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("native recovery context changed: %s / %s", canonicalMessagesJSON(t, got), canonicalMessagesJSON(t, want))
	}
	if len(got) != 2 || len(got[1].ToolResults()) != 1 {
		t.Fatalf("mirror entered model context: %s", canonicalMessagesJSON(t, got))
	}
	var payload struct {
		Content []session.EventToolContent `json:"content"`
	}
	if err := json.Unmarshal(got[1].ToolResults()[0].Content[0].JSON.Value, &payload); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(payload.Content, []session.EventToolContent{diff}) {
		t.Fatalf("native structured result lost: %#v", payload.Content)
	}
	native.Tool.Output = map[string]any{"result": "authoritative native output"}
	if got := toolResultContextPayload(native.Tool); !reflect.DeepEqual(got, native.Tool.Output) {
		t.Fatalf("display diff displaced native output: %#v", got)
	}
}

func TestCommandConversationReplayMatchesRuntimeProducedModelContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	user := model.NewTextMessage(model.RoleUser, "hello")
	live := []*session.Event{
		{ID: "command-message", MessageID: "command-message", Type: session.EventTypeInteraction, Visibility: session.VisibilityMirror,
			Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeUserMessage), MessageID: "command-message", Content: session.ProtocolTextContent("/status")}}},
		{Type: session.EventTypeUser, Message: &user},
	}
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	first := &recordingModel{}
	chat, err := New("chat", first, "")
	if err != nil {
		t.Fatal(err)
	}
	for event, err := range chat.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: live})) {
		if err != nil {
			t.Fatal(err)
		}
		if session.IsCanonicalHistoryEvent(event) {
			live = append(live, event)
			if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(first.last.Messages) != 1 || first.last.Messages[0].TextContent() != "hello" {
		t.Fatalf("command entered runtime request: %+v", first.last.Messages)
	}
	liveModel := &recordingModel{}
	liveChat, err := New("chat", liveModel, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range liveChat.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: active, Events: live})) {
		if err != nil {
			t.Fatal(err)
		}
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	replayModel := &recordingModel{}
	replayChat, err := New("chat", replayModel, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range replayChat.Run(agent.NewContext(agent.ContextSpec{Context: ctx, Session: loaded.Session, Events: loaded.Events})) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(liveModel.last.Messages, replayModel.last.Messages) {
		t.Fatalf("recovery changed runtime-produced model context: %s / %s", canonicalMessagesJSON(t, liveModel.last.Messages), canonicalMessagesJSON(t, replayModel.last.Messages))
	}
}

func TestCrossVersionReplayCorpusMatchesRuntimeProducedModelContext(t *testing.T) {
	t.Parallel()

	liveUser := model.NewTextMessage(model.RoleUser, "inspect")
	liveAssistantCall := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "call-1", Name: "probe", Args: `{"path":"README.md"}`}}, "")
	liveEvents := []*session.Event{
		{Schema: session.EventSchemaVersion, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &liveUser},
		modelToolCallEvents(liveAssistantCall, nil, "", nil)[0],
		toolResultEvent(model.ToolCall{ID: "call-1", Name: "probe", Args: `{"path":"README.md"}`}, tool.Result{
			Content: []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(`{"value":"ok"}`)}},
		}, nil, nil),
		modelResponseEvent(model.NewTextMessage(model.RoleAssistant, "done"), nil, "", nil),
	}
	for _, event := range liveEvents {
		event.Schema = session.EventSchemaVersion
	}
	want := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: context.Background(), Events: liveEvents}))

	for _, version := range []string{"v0", "v1"} {
		version := version
		t.Run(version, func(t *testing.T) {
			t.Parallel()
			events := loadReplayCorpus(t, filepath.Join("testdata", "replay", version+".json"))
			got := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: context.Background(), Events: events}))
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("rebuilt model context = %s, want runtime-produced %s", canonicalMessagesJSON(t, got), canonicalMessagesJSON(t, want))
			}
		})
	}
}

func loadReplayCorpus(t *testing.T, path string) []*session.Event {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var encoded []session.Event
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	out := make([]*session.Event, 0, len(encoded))
	for _, event := range encoded {
		migrated, err := session.MigrateEvent(event)
		if err != nil {
			t.Fatalf("MigrateEvent() error = %v", err)
		}
		if err := session.ValidateDurableCoreEvent(&migrated); err != nil {
			t.Fatalf("ValidateDurableCoreEvent() error = %v", err)
		}
		out = append(out, &migrated)
	}
	return out
}

func TestToolObservationInputNumbersSurviveSessionFileReplay(t *testing.T) {
	t.Parallel()
	const decimal = "9007199254740991.1"
	const integer = "900719925474099312345678901234567890"
	const exponent = "1e1000"
	call := model.ToolCall{
		ID: "call-1", Name: "probe",
		Args: `{"decimal":9007199254740991.1,"exponent":1e1000,"integer":900719925474099312345678901234567890,"nested":{"value":9007199254740991.1}}`,
	}
	assertInputs := func(t *testing.T, events []*session.Event) {
		t.Helper()
		for _, event := range events {
			if event.Tool == nil {
				t.Fatalf("%s has no tool payload", event.Type)
			}
			input := event.Tool.Input
			for key, want := range map[string]string{"decimal": decimal, "integer": integer, "exponent": exponent} {
				if got, ok := input[key].(json.Number); !ok || string(got) != want {
					t.Fatalf("%s input[%q] = %#v, want exact JSON number %s", event.Type, key, input[key], want)
				}
			}
			nested, ok := input["nested"].(map[string]any)
			if !ok || nested["value"] != json.Number(decimal) {
				t.Fatalf("%s nested input = %#v, want exact JSON number %s", event.Type, input["nested"], decimal)
			}
			encoded, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(encoded); got != call.Args {
				t.Fatalf("%s serialized input = %s, want %s", event.Type, got, call.Args)
			}
		}
	}

	live := []*session.Event{
		modelToolCallEvents(model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, ""), nil, "", nil)[0],
		toolResultEvent(call, tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(`{"result":"ok"}`)}}}, nil, nil),
	}
	assertInputs(t, live)
	ctx := context.Background()
	wantContext := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: live}))
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != len(live) {
		t.Fatalf("replayed events = %d, want %d", len(loaded.Events), len(live))
	}
	assertInputs(t, loaded.Events)
	gotContext := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: loaded.Events}))
	if !reflect.DeepEqual(gotContext, wantContext) {
		t.Fatalf("rebuilt model context = %s, want runtime-produced %s", canonicalMessagesJSON(t, gotContext), canonicalMessagesJSON(t, wantContext))
	}
}

func TestMustObjectRejectsTrailingJSON(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{`{"n":1} {"n":2}`, `{"n":1} garbage`, `{"n":1} 2`} {
		if got := mustObject(raw); got != nil {
			t.Fatalf("mustObject(%q) = %#v, want nil", raw, got)
		}
	}
}

func TestRecordedToolReplySurvivesDifferentOutputBudget(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cfg := sessionfile.Config{RootDir: t.TempDir()}
	store := sessionfile.NewStore(cfg)
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}

	call := model.ToolCall{ID: "call-1", Name: "read_record", Args: `{"key":"row-7"}`}
	assistant := model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, "")
	result := textToolResult(strings.Repeat("x", tool.DefaultTruncationPolicy().ByteBudget()*2))
	reply := toolbinding.ResultMessage(call, result, nil)
	events := append(modelToolCallEvents(assistant, nil, "", nil), toolResultEvent(call, result, nil, &reply))
	for _, event := range events {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}

	reopened := sessionfile.NewStore(cfg)
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	got := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: loaded.Events}))
	if !reflect.DeepEqual(got, []model.Message{assistant, reply}) {
		t.Fatal("reopened context differs from the original assistant call and tool reply")
	}
}

func TestToolNamePersistencePreservesRuntimeModelContext(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	user := model.NewTextMessage(model.RoleUser, "inspect")
	call := model.ToolCall{ID: "call-1", Name: "probe", Args: `{"path":"README.md"}`}
	live := []*session.Event{
		{Type: session.EventTypeUser, Message: &user},
		modelToolCallEvents(model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{call}, ""), nil, "", nil)[0],
		toolResultEvent(call, tool.Result{Content: []tool.ContentBlock{tool.StructuredContent{Value: json.RawMessage(`{"value":"ok"}`)}}}, nil, nil),
		modelResponseEvent(model.NewTextMessage(model.RoleAssistant, "done"), nil, "", nil),
	}
	want := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: live}))
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	name := "external_display_tool"
	subject := session.CloneApprovalSubject(&session.ApprovalSubject{Kind: "file", Tool: session.KnownApprovalField("probe"), Operation: session.KnownApprovalField("read"), Targets: []session.ApprovalField{session.KnownApprovalField("README.md")}, Effect: session.KnownApprovalField("read the exact path"), Scope: session.KnownApprovalField("this invocation")})
	live = append(live, &session.Event{
		Type: session.EventTypeToolCall, Visibility: session.VisibilityMirror,
		Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{
			SessionUpdate: "tool_call", ToolCallID: "remote-1", Name: &name, Title: "Display only", Kind: "other",
		}},
	})
	for _, present := range []bool{true, false} {
		live = append(live, &session.Event{
			Type: session.EventTypeLifecycle,
			Protocol: &session.EventProtocol{
				Method: session.ProtocolMethodRequestPermission,
				Permission: &session.ProtocolApproval{Subject: subject, ToolCall: session.ProtocolToolCall{
					ID: "remote-1", NamePresent: new(present),
				}},
			},
		})
	}
	for _, event := range live {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
	}
	fresh := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	loaded, err := fresh.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	got := messagesFromContext(agent.NewContext(agent.ContextSpec{Context: ctx, Events: loaded.Events}))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rebuilt context = %s, want %s", canonicalMessagesJSON(t, got), canonicalMessagesJSON(t, want))
	}
	update := session.ProtocolUpdateOf(loaded.Events[len(live)-3])
	if update == nil || update.Name == nil || *update.Name != name {
		t.Fatalf("replayed display name = %#v", update)
	}
	for i, present := range []bool{true, false} {
		approval := session.ProtocolPermissionOf(loaded.Events[len(live)-2+i])
		if approval == nil || approval.ToolCall.NamePresent == nil || *approval.ToolCall.NamePresent != present {
			t.Fatalf("replayed approval = %#v, want name presence %t", approval, present)
		}
		if !reflect.DeepEqual(approval.Subject, subject) {
			t.Fatalf("approval display facts changed during model-context round trip: %+v", approval.Subject)
		}
	}
}
