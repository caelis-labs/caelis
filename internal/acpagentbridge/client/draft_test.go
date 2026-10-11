package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
	"github.com/caelis-labs/caelis/internal/acpdraft"
)

type draftSelectionResolver struct {
	request endpoint.Request
}

func (r *draftSelectionResolver) ResolveACPProcess(_ context.Context, request endpoint.Request) (endpoint.Process, error) {
	r.request = request
	return endpoint.Process{}, errors.New("fixture: stop before launch")
}

func TestHostedCodexKeepsStableDefaultAndExplicitDraftSelection(t *testing.T) {
	t.Parallel()
	for _, schema := range []string{"", acpdraft.SchemaTag} {
		resolver := &draftSelectionResolver{}
		if _, err := Start(t.Context(), Config{HostedAdapterID: "codex", WorkDir: "/tmp", ACPDraftSchema: schema, EndpointResolver: resolver}); err == nil {
			t.Fatal("fixture unexpectedly launched a process")
		}
		if resolver.request.ACPDraftSchema != schema {
			t.Fatalf("selected schema %q became %q", schema, resolver.request.ACPDraftSchema)
		}
	}
}

// The fixture uses the SDK's actual draft request dispatcher and transport.
// Prompt acknowledgement and foreground completion remain independent fences.
type draftTestAgent struct {
	conn       *v2.AgentSideConnection
	initialize func(v2.InitializeRequest) (v2.InitializeResponse, error)
	prompt     func(context.Context, v2.PromptRequest) (v2.PromptResponse, error)
	resume     func(context.Context, v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error)
	config     func(v2.SetSessionConfigOptionRequest) (v2.SetSessionConfigOptionResponse, error)
	requests   chan v2.PromptRequest
	cancels    chan v2.CancelSessionNotification
	logins     chan v2.LoginAuthRequest
	mu         sync.Mutex
	next       int
}

func (a *draftTestAgent) Initialize(_ context.Context, req v2.InitializeRequest) (v2.InitializeResponse, error) {
	if a.initialize != nil {
		return a.initialize(req)
	}
	if err := acpdraft.ValidateInitialize(req); err != nil {
		return v2.InitializeResponse{}, err
	}
	return v2.InitializeResponse{ProtocolVersion: 2, Info: v2.Implementation{Name: "draft-fixture", Version: "test"}, Capabilities: v2.AgentCapabilities{Session: &v2.SessionCapabilities{}}, Meta: acpdraft.InitializeMeta(), AuthMethods: []v2.AuthMethod{{Agent: &v2.AuthMethodAgentInline{MethodId: "login", Name: "Login", Type: "agent"}}}}, nil
}
func (a *draftTestAgent) LoginAuth(_ context.Context, req v2.LoginAuthRequest) (v2.LoginAuthResponse, error) {
	a.logins <- req
	return v2.LoginAuthResponse{}, nil
}
func (a *draftTestAgent) LogoutAuth(context.Context, v2.LogoutAuthRequest) (v2.LogoutAuthResponse, error) {
	return v2.LogoutAuthResponse{}, nil
}
func (a *draftTestAgent) NewSession(context.Context, v2.NewSessionRequest) (v2.NewSessionResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.next++
	return v2.NewSessionResponse{SessionId: v2.SessionId(fmt.Sprintf("session-%d", a.next))}, nil
}
func (a *draftTestAgent) Prompt(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
	a.requests <- req
	return a.prompt(ctx, req)
}
func (a *draftTestAgent) Cancel(_ context.Context, req v2.CancelSessionNotification) error {
	a.cancels <- req
	return nil
}
func (a *draftTestAgent) ResumeSession(ctx context.Context, req v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
	if a.resume != nil {
		return a.resume(ctx, req)
	}
	return v2.ResumeSessionResponse{}, nil
}
func (a *draftTestAgent) ListSessions(context.Context, v2.ListSessionsRequest) (v2.ListSessionsResponse, error) {
	return v2.ListSessionsResponse{Sessions: []v2.SessionInfo{{SessionId: "session-1", Cwd: "/tmp"}}}, nil
}
func (a *draftTestAgent) CloseSession(context.Context, v2.CloseSessionRequest) (v2.CloseSessionResponse, error) {
	return v2.CloseSessionResponse{}, nil
}
func (a *draftTestAgent) SetSessionConfigOption(_ context.Context, req v2.SetSessionConfigOptionRequest) (v2.SetSessionConfigOptionResponse, error) {
	return a.config(req)
}

func newOutboundDraftFixture(t *testing.T, agent *draftTestAgent, cfg Config) (*Client, context.Context, <-chan UpdateEnvelope) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	left, right := net.Pipe()
	updates := make(chan UpdateEnvelope, 128)
	agent.requests = make(chan v2.PromptRequest, 16)
	agent.cancels = make(chan v2.CancelSessionNotification, 4)
	agent.logins = make(chan v2.LoginAuthRequest, 4)
	cfg.ACPDraftSchema = acpdraft.SchemaTag
	cfg.OnUpdate = func(update UpdateEnvelope) { updates <- update }
	client, err := NewStreamClient(left, left, cfg)
	if err != nil {
		t.Fatal(err)
	}
	agent.conn, err = v2.NewAgentSideConnection(agent, right, right)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		_ = left.Close()
		_ = right.Close()
		_ = agent.conn.Close()
		_ = client.Close(context.Background())
	})
	return client, ctx, updates
}

func (a *draftTestAgent) update(ctx context.Context, sessionID string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	// Preserve [] and null on the wire. The receiving SDK dispatcher still
	// validates the recognized standard variant before the raw patch decoder.
	return a.conn.SessionUpdate(ctx, v2.UpdateSessionNotification{SessionId: v2.SessionId(sessionID), Update: v2.SessionUpdate{Other: func() *json.RawMessage { value := json.RawMessage(raw); return &value }()}})
}
func (a *draftTestAgent) state(ctx context.Context, session, activity, state, reason string) error {
	value := map[string]any{"sessionUpdate": "state_update", "state": state, "_meta": acpdraft.ActivityMeta(activity)}
	if state == "idle" {
		value["stopReason"] = reason
	}
	return a.update(ctx, session, value)
}
func (a *draftTestAgent) echo(ctx context.Context, session, id string) error {
	return a.update(ctx, session, map[string]any{"sessionUpdate": "user_message", "messageId": id, "content": []any{map[string]any{"type": "text", "text": "input"}}})
}
func requireDraftUpdate(t *testing.T, ctx context.Context, updates <-chan UpdateEnvelope) UpdateEnvelope {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return UpdateEnvelope{}
	}
}
func assertDraftPending(t *testing.T, results <-chan error) {
	t.Helper()
	select {
	case err := <-results:
		t.Fatalf("foreground completed before settlement: %v", err)
	case <-time.After(15 * time.Millisecond):
	}
}

func TestDraftOutboundReceiptCompletionAndIndependentSessions(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{}
	agent.prompt = func(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
		guard, err := acpdraft.ParseActiveOnly(req.Meta)
		if err != nil || guard == nil || !guard.IdleOnly || guard.ActivityID != "" {
			return v2.PromptResponse{}, errors.New("initial prompt lacks idle-only admission")
		}
		session := string(req.SessionId)
		if err := agent.state(ctx, session, "activity-"+session, "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := agent.echo(ctx, session, "input-"+session); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: v2.MessageId("input-" + session), Meta: acpdraft.ActivityMeta("activity-" + session)}, nil
	}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	results := make([]chan error, 2)
	for index := range results {
		session := fmt.Sprintf("session-%d", index+1)
		call, err := client.PreparePromptParts(session, []json.RawMessage{json.RawMessage(`{"type":"text","text":"input"}`)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := call.Dispatch(ctx); err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		results[index] = result
		go func() {
			response, err := call.Wait(ctx)
			if err == nil && response.StopReason != "end_turn" {
				err = fmt.Errorf("stop=%s", response.StopReason)
			}
			result <- err
		}()
		requireDraftUpdate(t, ctx, updates)
	}
	if _, err := client.PreparePromptParts("session-1", []json.RawMessage{json.RawMessage(`{"type":"text","text":"duplicate"}`)}, nil); err == nil {
		t.Fatal("same Session admitted a second foreground producer")
	}
	assertDraftPending(t, results[0])
	assertDraftPending(t, results[1])
	if err := agent.state(ctx, "session-1", "activity-session-1", "idle", "end_turn"); err != nil {
		t.Fatal(err)
	}
	if err := <-results[0]; err != nil {
		t.Fatal(err)
	}
	assertDraftPending(t, results[1])
	if err := agent.state(ctx, "session-2", "activity-session-2", "idle", "end_turn"); err != nil {
		t.Fatal(err)
	}
	if err := <-results[1]; err != nil {
		t.Fatal(err)
	}
}

func TestDraftOutboundInitialPromptUsesOwnerIdleGuardAndNeverInjectsOnBusy(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{}
	agent.prompt = func(_ context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
		guard, err := acpdraft.ParseActiveOnly(req.Meta)
		if err != nil || guard == nil || !guard.IdleOnly || guard.ActivityID != "" {
			return v2.PromptResponse{}, errors.New("missing idle-only guard")
		}
		// The owner became busy through a different Control caller while this
		// client's cached view was still idle. Admission must have no effect.
		return v2.PromptResponse{}, acpdraft.InactivePromptError()
	}
	client, ctx, _ := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := client.PromptParts(ctx, "session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"new turn"}`)}, nil)
	if !acpdraft.IsInactivePromptError(err) || PromptOutcomeUnknown(err) {
		t.Fatalf("busy admission is not a proven no-effect rejection: %v", err)
	}
	<-agent.requests
	select {
	case <-agent.requests:
		t.Fatal("initial prompt was silently retried as injected input")
	default:
	}
	if _, err := client.PreparePromptParts("session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"override"}`)}, map[string]any{acpdraft.ActiveOnlyMetaKey: map[string]any{"activityId": "other"}}); err == nil {
		t.Fatal("initial caller overrode its required idle condition")
	}
}

func TestDraftOutboundSteeringUsesStandardPromptAndExactGuard(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{}
	agent.prompt = func(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
		guard, err := acpdraft.ParseActiveOnly(req.Meta)
		if err != nil || guard == nil || guard.ActivityID != "native-activity" {
			return v2.PromptResponse{}, errors.New("incorrect conditional prompt")
		}
		if err := agent.echo(ctx, string(req.SessionId), "canonical-steer"); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: "canonical-steer", Meta: acpdraft.ActivityMeta("native-activity")}, nil
	}
	client, ctx, _ := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	response, err := client.SteerPartsWithAbort(ctx, "session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"steer"}`)}, nil, nil)
	if err != nil || response.Outcome != SessionSteeringPromptRequired {
		t.Fatalf("idle=%+v %v", response, err)
	}
	select {
	case <-agent.requests:
		t.Fatal("idle steering dispatched replacement work")
	default:
	}
	if err := agent.state(ctx, "session", "native-activity", "running", ""); err != nil {
		t.Fatal(err)
	}
	// An SDK response acts as the notification ordering barrier.
	if _, err := client.ListSessions(ctx, acp.ListSessionsRequest{}); err != nil {
		t.Fatal(err)
	}
	response, err = client.SteerPartsWithAbort(ctx, "session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"steer"}`)}, nil, nil)
	if err != nil || response.Outcome != SessionSteeringInjected {
		t.Fatalf("running=%+v %v", response, err)
	}
	<-agent.requests
	agent.prompt = func(context.Context, v2.PromptRequest) (v2.PromptResponse, error) {
		return v2.PromptResponse{}, acpdraft.InactivePromptError()
	}
	response, err = client.SteerPartsWithAbort(ctx, "session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"stale"}`)}, nil, nil)
	if err != nil || response.Outcome != SessionSteeringPromptRequired {
		t.Fatalf("stale guard=%+v %v", response, err)
	}
}

func TestDraftOutboundAcknowledgementStillWaitsForEchoAndCancellationSettlement(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{}
	agent.prompt = func(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
		if err := agent.state(ctx, string(req.SessionId), "activity", "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: "canonical-input", Meta: acpdraft.ActivityMeta("activity")}, nil
	}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	call, err := client.PreparePromptParts("session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"input"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 1)
	go func() {
		result, err := call.Wait(ctx)
		if err == nil && result.StopReason != "cancelled" {
			err = fmt.Errorf("stop=%s", result.StopReason)
		}
		results <- err
	}()
	<-agent.requests
	assertDraftPending(t, results)
	if err := client.Cancel(ctx, "session"); err != nil {
		t.Fatal(err)
	}
	if request := <-agent.cancels; request.SessionId != "session" {
		t.Fatal(request)
	}
	assertDraftPending(t, results)
	if err := agent.echo(ctx, "session", "canonical-input"); err != nil {
		t.Fatal(err)
	}
	requireDraftUpdate(t, ctx, updates)
	assertDraftPending(t, results)
	if err := agent.state(ctx, "session", "activity", "idle", "cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
}

func TestDraftOutboundRejectsIncompatibleProfileWithoutStableFallback(t *testing.T) {
	t.Parallel()
	for _, incompatible := range []string{"version", "schema", "guard", "idleGuard", "session"} {
		t.Run(incompatible, func(t *testing.T) {
			agent := &draftTestAgent{initialize: func(req v2.InitializeRequest) (v2.InitializeResponse, error) {
				if err := acpdraft.ValidateInitialize(req); err != nil {
					return v2.InitializeResponse{}, err
				}
				response := v2.InitializeResponse{ProtocolVersion: 2, Info: v2.Implementation{Name: "fixture", Version: "test"}, Capabilities: v2.AgentCapabilities{Session: &v2.SessionCapabilities{}}, Meta: acpdraft.InitializeMeta()}
				switch incompatible {
				case "version":
					response.ProtocolVersion = 1
				case "schema":
					response.Meta[acpdraft.SchemaMetaKey] = json.RawMessage(`"schema-v2.0.0-alpha.7"`)
				case "guard":
					delete(response.Meta, acpdraft.ConditionalPromptMetaKey)
				case "idleGuard":
					response.Meta[acpdraft.ConditionalPromptMetaKey] = json.RawMessage(`{"version":1,"activeOnly":true}`)
				case "session":
					response.Capabilities.Session = nil
				}
				return response, nil
			}}
			client, ctx, _ := newOutboundDraftFixture(t, agent, Config{})
			if _, err := client.Initialize(ctx); err == nil {
				t.Fatal("incompatible draft was accepted")
			}
			select {
			case <-agent.requests:
				t.Fatal("initialization failure dispatched work")
			default:
			}
		})
	}
}

func TestDraftOutboundReplayBarrierAndConfigIDNormalization(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{}
	agent.resume = func(ctx context.Context, request v2.ResumeSessionRequest) (v2.ResumeSessionResponse, error) {
		if request.ReplayFrom == nil || request.ReplayFrom.Start == nil {
			return v2.ResumeSessionResponse{}, errors.New("load omitted standard replay cursor")
		}
		if err := agent.echo(ctx, string(request.SessionId), "persisted-canonical"); err != nil {
			return v2.ResumeSessionResponse{}, err
		}
		return v2.ResumeSessionResponse{}, nil
	}
	agent.config = func(request v2.SetSessionConfigOptionRequest) (v2.SetSessionConfigOptionResponse, error) {
		if request.Id == nil || request.Id.ConfigId != "model" || request.Id.Value != "model-a" {
			return v2.SetSessionConfigOptionResponse{}, fmt.Errorf("wrong config request: %+v", request)
		}
		var option v2.SessionConfigOption
		if err := json.Unmarshal([]byte(`{"configId":"model","name":"Model","type":"select","currentValue":"model-a","options":[{"value":"model-a","name":"Model A"}]}`), &option); err != nil {
			return v2.SetSessionConfigOptionResponse{}, err
		}
		return v2.SetSessionConfigOptionResponse{ConfigOptions: []v2.SessionConfigOption{option}}, nil
	}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	replayed := false
	_, err := client.LoadSessionWithReplayEnd(ctx, "session", "/tmp", nil, func(context.Context) error {
		update := requireDraftUpdate(t, ctx, updates)
		chunk, ok := update.Update.(ContentChunk)
		replayed = ok && chunk.MessageID == "persisted-canonical" && chunk.Replacement
		return nil
	})
	if err != nil || !replayed {
		t.Fatalf("replay=%t %v", replayed, err)
	}
	response, err := client.SetConfigOption(ctx, "session", "model", "model-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ConfigOptions) != 1 || response.ConfigOptions[0].ID != "model" {
		t.Fatalf("config normalized=%+v", response)
	}
	if err := client.Authenticate(ctx, "login"); err != nil {
		t.Fatal(err)
	}
	if request := <-agent.logins; !reflect.DeepEqual(request.MethodId, v2.AuthMethodId("login")) {
		t.Fatal(request)
	}
}

func TestDraftOutboundRawMessageAndToolPatchesKeepClearSemantics(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	send := func(value any) Update {
		t.Helper()
		if err := agent.update(ctx, "session", value); err != nil {
			t.Fatal(err)
		}
		return requireDraftUpdate(t, ctx, updates).Update
	}
	chunk := send(map[string]any{"sessionUpdate": "agent_message_chunk", "messageId": "message", "content": map[string]any{"type": "text", "text": "before"}, "_meta": map[string]any{"display": "old"}}).(ContentChunk)
	if !chunk.Replacement {
		t.Fatal("first chunk did not register its v2 message identity")
	}
	patch := send(map[string]any{"sessionUpdate": "agent_message", "messageId": "message", "_meta": map[string]any{"display": "new"}}).(ContentChunk)
	if patch.Replacement || !patch.MetaPresent || patch.Meta["display"] != "new" || string(patch.Content) != `{"type":"text","text":""}` {
		t.Fatalf("metadata patch=%+v", patch)
	}
	for _, content := range []any{nil, []any{}} {
		clear := send(map[string]any{"sessionUpdate": "agent_message", "messageId": "message", "content": content, "_meta": nil}).(ContentChunk)
		if !clear.Replacement || clear.Meta != nil || string(clear.Content) != `{"type":"text","text":""}` {
			t.Fatalf("clear=%+v", clear)
		}
	}
	tool := send(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tool", "title": nil, "content": []any{}, "rawInput": nil, "rawOutput": nil, "locations": nil, "_meta": nil}).(ToolCallUpdate)
	for _, key := range []string{"title", "content", "rawInput", "rawOutput", "locations", "_meta"} {
		if _, ok := tool.RawPatch[key]; !ok {
			t.Fatalf("lost explicit %s patch: %+v", key, tool)
		}
	}
	if _, ok := tool.RawPatch["kind"]; ok {
		t.Fatal("absent kind became a clear")
	}
	if string(tool.RawPatch["content"]) != "[]" || string(tool.RawPatch["title"]) != "null" {
		t.Fatal(tool.RawPatch)
	}
}

func TestDraftOutboundTerminalChunksSnapshotsAndExitUseSharedProjection(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	send := func(value any) ToolCallUpdate {
		t.Helper()
		if err := agent.update(ctx, "session", value); err != nil {
			t.Fatal(err)
		}
		update, ok := requireDraftUpdate(t, ctx, updates).Update.(ToolCallUpdate)
		if !ok {
			t.Fatalf("terminal projection lost tool association")
		}
		return update
	}
	send(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "tool", "content": []any{map[string]any{"type": "terminal", "terminalId": "terminal"}}})
	for _, encoded := range []string{"YQ==", "Yg=="} {
		update := send(map[string]any{"sessionUpdate": "terminal_output_chunk", "terminalId": "terminal", "data": encoded})
		output, ok := acpmeta.ReadTerminalOutput(update.Meta)
		if !ok || output.Replacement || len(output.Data) != 1 {
			t.Fatalf("chunk appended a cumulative snapshot: %+v", update)
		}
	}
	update := send(map[string]any{"sessionUpdate": "terminal_update", "terminalId": "terminal", "output": map[string]any{"data": "c25hcHNob3Q="}})
	output, ok := acpmeta.ReadTerminalOutput(update.Meta)
	if !ok || !output.Replacement || output.Data != "snapshot" {
		t.Fatalf("replacement=%+v", update)
	}
	for _, snapshot := range []any{nil, map[string]any{"data": ""}} {
		update = send(map[string]any{"sessionUpdate": "terminal_update", "terminalId": "terminal", "output": snapshot})
		output, ok = acpmeta.ReadTerminalOutput(update.Meta)
		if !ok || !output.Replacement || output.Data != "" {
			t.Fatalf("clear=%+v", update)
		}
	}
	update = send(map[string]any{"sessionUpdate": "terminal_update", "terminalId": "terminal", "exitStatus": map[string]any{"exitCode": 0}})
	exit, ok := acpmeta.ReadTerminalExit(update.Meta)
	if !ok || exit.ExitCode == nil || *exit.ExitCode != 0 {
		t.Fatalf("zero exit lost: %+v", update)
	}
	if _, ok := acpmeta.ReadTerminalOutput(update.Meta); ok {
		t.Fatal("exit repeated terminal output")
	}
	update = send(map[string]any{"sessionUpdate": "terminal_update", "terminalId": "terminal", "exitStatus": nil})
	exit, ok = acpmeta.ReadTerminalExit(update.Meta)
	if !ok || exit.ExitCode != nil || exit.Signal != nil {
		t.Fatalf("exit clear=%+v", update)
	}
}

func TestDraftOutboundPermissionSelectsOnlyOriginalOptionAndNoSyntheticTool(t *testing.T) {
	t.Parallel()
	for _, selected := range []string{"native-option", "invented"} {
		t.Run(selected, func(t *testing.T) {
			agent := &draftTestAgent{}
			client, ctx, _ := newOutboundDraftFixture(t, agent, Config{OnDraftPermissionRequest: func(_ context.Context, req v2.RequestPermissionRequest) (v2.RequestPermissionResponse, error) {
				if req.Title != "native approval" || req.Subject != nil || len(req.Options) != 1 || req.Options[0].OptionId != "native-option" {
					return v2.RequestPermissionResponse{}, errors.New("permission context was altered")
				}
				return v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: v2.PermissionOptionId(selected)}}}, nil
			}})
			if _, err := client.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			response, err := agent.conn.RequestPermission(ctx, v2.RequestPermissionRequest{SessionId: "session", Title: "native approval", Options: []v2.PermissionOption{{OptionId: "native-option", Name: "Allow", Kind: "allow_once"}}})
			if selected == "invented" {
				var rpcError *acp.RequestError
				if !errors.As(err, &rpcError) || rpcError.Code != -32602 {
					t.Fatalf("foreign selection=%+v %v", response, err)
				}
			} else if err != nil || response.Outcome.Selected == nil || response.Outcome.Selected.OptionId != "native-option" {
				t.Fatalf("native selection=%+v %v", response, err)
			}
		})
	}
}

func TestDraftOutboundTransportLossAfterReceiptRemainsUnknown(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{}
	agent.prompt = func(ctx context.Context, req v2.PromptRequest) (v2.PromptResponse, error) {
		if err := agent.state(ctx, string(req.SessionId), "activity", "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := agent.echo(ctx, string(req.SessionId), "canonical-input"); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: "canonical-input", Meta: acpdraft.ActivityMeta("activity")}, nil
	}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	call, err := client.PreparePromptParts("session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"input"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 1)
	go func() { _, err := call.Wait(ctx); results <- err }()
	requireDraftUpdate(t, ctx, updates)
	assertDraftPending(t, results)
	_ = agent.conn.Close()
	err = <-results
	if err == nil || !PromptOutcomeUnknown(err) {
		t.Fatalf("lost connection fabricated completion=%v", err)
	}
	if len(agent.requests) != 1 {
		t.Fatal("uncertain Activity was resubmitted")
	}
}

func TestDraftOutboundCapabilitiesAuthenticationAndMCPUseDraftShapes(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{initialize: func(request v2.InitializeRequest) (v2.InitializeResponse, error) {
		if request.Capabilities.Auth == nil || request.Capabilities.Auth.Terminal == nil || request.Capabilities.Elicitation != nil {
			return v2.InitializeResponse{}, errors.New("incorrect client capabilities")
		}
		return v2.InitializeResponse{ProtocolVersion: 2, Info: v2.Implementation{Name: "fixture", Version: "test"}, Meta: acpdraft.InitializeMeta(), Capabilities: v2.AgentCapabilities{Session: &v2.SessionCapabilities{
			Prompt: &v2.PromptCapabilities{Image: &v2.PromptImageCapabilities{}, Audio: &v2.PromptAudioCapabilities{}, EmbeddedContext: &v2.PromptEmbeddedContextCapabilities{}},
			Mcp:    &v2.McpCapabilities{Stdio: &v2.McpStdioCapabilities{}, Http: &v2.McpHttpCapabilities{}},
		}}, AuthMethods: []v2.AuthMethod{
			{Agent: &v2.AuthMethodAgentInline{MethodId: "agent-login", Name: "Agent login", Type: "agent"}},
			{Terminal: &v2.AuthMethodTerminalInline{MethodId: "terminal-login", Name: "Terminal login", Type: "terminal", Args: []string{"login"}, Env: []v2.EnvVariable{{Name: "FIXTURE", Value: "value"}}}},
		}}, nil
	}}
	client, ctx, _ := newOutboundDraftFixture(t, agent, Config{TerminalAuth: true})
	response, err := client.Initialize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	caps := response.AgentCapabilities
	if !caps.PromptCapabilities.Image || !caps.PromptCapabilities.Audio || !caps.PromptCapabilities.EmbeddedContext || !caps.McpCapabilities.Http || caps.McpCapabilities.Sse {
		t.Fatalf("capabilities lost during private normalization: %+v", caps)
	}
	var methods []map[string]any
	for _, raw := range response.AuthMethods {
		var method map[string]any
		if err := json.Unmarshal(raw, &method); err != nil {
			t.Fatal(err)
		}
		methods = append(methods, method)
	}
	if len(methods) != 2 || methods[0]["id"] != "agent-login" || methods[1]["id"] != "terminal-login" || methods[1]["env"].(map[string]any)["FIXTURE"] != "value" {
		t.Fatalf("auth normalization=%+v", methods)
	}
	if err := client.Authenticate(ctx, "terminal-login"); err == nil {
		t.Fatal("terminal method was passed to auth/login")
	}
	if err := client.Authenticate(ctx, "unknown"); err == nil {
		t.Fatal("unadvertised method was dispatched")
	}
	if err := client.Authenticate(ctx, "agent-login"); err != nil {
		t.Fatal(err)
	}
	if request := <-agent.logins; request.MethodId != "agent-login" {
		t.Fatal(request)
	}
	servers, err := draftMCPServers([]acp.McpServer{{Stdio: &acp.McpServerStdio{Name: "fixture", Command: "/bin/true", Args: []string{}, Env: []acp.EnvVariable{}}}})
	if err != nil || len(servers) != 1 || servers[0].Stdio == nil || servers[0].Stdio.Type != "stdio" {
		t.Fatalf("stdio=%+v %v", servers, err)
	}
	if _, err := draftMCPServers([]acp.McpServer{{Sse: &acp.McpServerSseInline{Type: "sse", Name: "fixture", Url: "https://example.invalid", Headers: []acp.HttpHeader{}}}}); err == nil {
		t.Fatal("SSE silently changed transport")
	}
}

func TestDraftOutboundOldUserReplayCannotSatisfyNewReceipt(t *testing.T) {
	t.Parallel()
	agent := &draftTestAgent{}
	agent.prompt = func(ctx context.Context, request v2.PromptRequest) (v2.PromptResponse, error) {
		if err := agent.state(ctx, string(request.SessionId), "new-activity", "running", ""); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := agent.echo(ctx, string(request.SessionId), "old-message"); err != nil {
			return v2.PromptResponse{}, err
		}
		if err := agent.state(ctx, string(request.SessionId), "new-activity", "idle", "end_turn"); err != nil {
			return v2.PromptResponse{}, err
		}
		return v2.PromptResponse{MessageId: "old-message", Meta: acpdraft.ActivityMeta("new-activity")}, nil
	}
	client, ctx, updates := newOutboundDraftFixture(t, agent, Config{})
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := agent.echo(ctx, "session", "old-message"); err != nil {
		t.Fatal(err)
	}
	requireDraftUpdate(t, ctx, updates)
	call, err := client.PreparePromptParts("session", []json.RawMessage{json.RawMessage(`{"type":"text","text":"new input"}`)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	_, err = call.Wait(waitCtx)
	if err == nil || !PromptOutcomeUnknown(err) {
		t.Fatalf("history update became insertion proof: %v", err)
	}
	call.Abandon()
}
