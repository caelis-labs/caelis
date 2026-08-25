package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestPendingPromptAndSteeringResponsesShareUpdateBarrier(t *testing.T) {
	t.Parallel()

	clientSide, peerSide := net.Pipe()
	defer peerSide.Close()
	notificationStarted := make(chan struct{})
	releaseNotification := make(chan struct{})
	acpClient, err := NewStreamClient(clientSide, clientSide, Config{
		OnUpdate: func(UpdateEnvelope) {
			close(notificationStarted)
			<-releaseNotification
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer acpClient.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	responsesWritten := make(chan struct{})
	go servePromptAndSteering(t, peerSide, responsesWritten)

	prompt, err := acpClient.PreparePromptParts("session-1", []json.RawMessage{
		mustMarshalRaw(TextContent{Type: "text", Text: "start"}),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := prompt.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	promptDone := make(chan error, 1)
	go func() {
		_, waitErr := prompt.Wait(ctx)
		promptDone <- waitErr
	}()
	steeringDone := make(chan error, 1)
	go func() {
		response, steerErr := acpClient.SteerPartsWithAbort(ctx, "session-1", []json.RawMessage{
			mustMarshalRaw(TextContent{Type: "text", Text: "adjust"}),
		}, nil, nil)
		if steerErr == nil && response.Outcome != SessionSteeringInjected {
			steerErr = errors.New("steering response was not injected")
		}
		steeringDone <- steerErr
	}()

	<-notificationStarted
	<-responsesWritten
	close(releaseNotification)
	for name, done := range map[string]<-chan error{"prompt": promptDone, "steering": steeringDone} {
		if err := <-done; err != nil {
			t.Fatalf("%s response: %v", name, err)
		}
	}
}

func servePromptAndSteering(t *testing.T, peer net.Conn, responsesWritten chan<- struct{}) {
	t.Helper()
	scanner := bufio.NewScanner(peer)
	ids := make(map[string]json.RawMessage)
	for len(ids) < 2 && scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		ids[request.Method] = request.ID
	}
	messages := []map[string]any{
		{
			"jsonrpc": "2.0",
			"method":  MethodSessionUpdate,
			"params": SessionNotification{
				SessionID: "session-1",
				Update: mustMarshalRaw(ContentChunk{
					SessionUpdate: UpdateAgentMessage,
					MessageID:     "message-1",
					Content:       mustMarshalRaw(TextContent{Type: "text", Text: "progress"}),
				}),
			},
		},
		{
			"jsonrpc": "2.0",
			"id":      ids[MethodSessionSteering],
			"result":  SessionSteeringResponse{Outcome: SessionSteeringInjected},
		},
		{
			"jsonrpc": "2.0",
			"id":      ids[MethodSessionPrompt],
			"result":  PromptResponse{StopReason: "end_turn"},
		},
	}
	for _, message := range messages {
		raw, err := json.Marshal(message)
		if err != nil {
			t.Errorf("encode response: %v", err)
			return
		}
		if _, err := peer.Write(append(raw, '\n')); err != nil {
			return
		}
	}
	close(responsesWritten)
}

func TestDispatchMayHaveCommittedClassifiesCompletedResponses(t *testing.T) {
	t.Parallel()

	if DispatchMayHaveCommitted(&acpsdk.RequestError{Code: -32000, Message: "rejected"}) {
		t.Fatal("peer RequestError classified as ambiguous")
	}
	if !DispatchMayHaveCommitted(&acpsdk.ResponseDecodeError{Err: errors.New("bad result")}) {
		t.Fatal("successful undecodable response classified as retry-safe")
	}
}

func TestSubmissionProvenNotStartedRequiresSDKClassification(t *testing.T) {
	t.Parallel()

	clientSide, peerSide := net.Pipe()
	acpClient, err := NewStreamClient(clientSide, clientSide, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := acpClient.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = peerSide.Close()

	_, err = acpClient.PreparePromptParts("session-1", []json.RawMessage{mustMarshalRaw(TextContent{Type: "text", Text: "work"})}, nil)
	if err == nil {
		t.Fatal("PreparePromptParts() error = nil for closed connection")
	}
	if !SubmissionProvenNotStarted(err) {
		t.Fatalf("SubmissionProvenNotStarted(%v) = false, want SDK pre-write proof", err)
	}
	if SubmissionProvenNotStarted(errors.New("connection closed")) {
		t.Fatal("unclassified connection error treated as retry-safe")
	}
}

func TestDefaultPermissionPolicyRejectsOnce(t *testing.T) {
	t.Parallel()

	client := &Client{}
	result, rpcErr := client.handleRequest(context.Background(), MethodSessionReqPermission, mustMarshalRaw(RequestPermissionRequest{}))
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	response, ok := result.(RequestPermissionResponse)
	if !ok || response.Outcome.Outcome != "selected" || response.Outcome.OptionID != "reject_once" {
		t.Fatalf("permission response = %#v", result)
	}
}

func TestPermissionHandlerErrorUsesInternalErrorCode(t *testing.T) {
	t.Parallel()

	client := &Client{cfg: Config{OnPermissionRequest: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
		return RequestPermissionResponse{}, errors.New("permission backend unavailable")
	}}}
	_, rpcErr := client.handleRequest(context.Background(), MethodSessionReqPermission, mustMarshalRaw(RequestPermissionRequest{}))
	if rpcErr == nil {
		t.Fatal("permission handler error = nil")
	}
	if rpcErr.Code != -32603 {
		t.Fatalf("permission handler code = %d, want -32603", rpcErr.Code)
	}
}

func TestClientRejectsWrongACPMessageDirection(t *testing.T) {
	t.Parallel()

	clientSide, peerSide := net.Pipe()
	permissionCalls := 0
	updateCalls := 0
	updateObserved := make(chan struct{}, 1)
	acpClient, err := NewStreamClient(clientSide, clientSide, Config{
		OnPermissionRequest: func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error) {
			permissionCalls++
			return PermissionSelectedOutcome("allow_once"), nil
		},
		OnUpdate: func(UpdateEnvelope) {
			updateCalls++
			updateObserved <- struct{}{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer acpClient.Close(context.Background())
	peer, err := acpsdk.NewConnectionWithOptions(nil, peerSide, peerSide, acpsdk.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := peer.SendNotification(ctx, MethodSessionReqPermission, RequestPermissionRequest{}); err != nil {
		t.Fatalf("request_permission notification error = %v", err)
	}
	update := SessionNotification{
		SessionID: "session-1",
		Update: mustMarshalRaw(ContentChunk{
			SessionUpdate: UpdateAgentMessage,
			MessageID:     "message-1",
			Content:       mustMarshalRaw(TextContent{Type: "text", Text: "progress"}),
		}),
	}
	if err := peer.SendNotification(ctx, MethodSessionUpdate, update); err != nil {
		t.Fatalf("session/update notification error = %v", err)
	}
	select {
	case <-updateObserved:
	case <-ctx.Done():
		t.Fatal("timed out waiting for valid session/update notification")
	}
	if permissionCalls != 0 {
		t.Fatalf("permission calls after notification = %d, want 0", permissionCalls)
	}

	_, err = acpsdk.SendRequest[struct{}](peer, ctx, MethodSessionUpdate, update)
	var requestErr *acpsdk.RequestError
	if !errors.As(err, &requestErr) || requestErr.Code != -32601 {
		t.Fatalf("session/update request error = %v, want method not found", err)
	}
	if updateCalls != 1 {
		t.Fatalf("update calls after request = %d, want only the notification", updateCalls)
	}

	response, err := acpsdk.SendRequest[RequestPermissionResponse](peer, ctx, MethodSessionReqPermission, RequestPermissionRequest{})
	if err != nil {
		t.Fatalf("request_permission request error = %v", err)
	}
	if response.Outcome.Outcome != "selected" || response.Outcome.OptionID != "allow_once" {
		t.Fatalf("permission response = %#v", response)
	}
	if permissionCalls != 1 {
		t.Fatalf("permission calls after request = %d, want 1", permissionCalls)
	}
}

func TestSDKWireIngressPreservesStandardGrokToolLifecycle(t *testing.T) {
	t.Parallel()

	clientSide, peerSide := net.Pipe()
	updates := make(chan UpdateEnvelope, 2)
	acpClient, err := NewStreamClient(clientSide, clientSide, Config{
		OnUpdate: func(update UpdateEnvelope) { updates <- update },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer acpClient.Close(context.Background())
	peer, err := acpsdk.NewConnectionWithOptions(nil, peerSide, peerSide, acpsdk.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	wire := []json.RawMessage{
		json.RawMessage(`{"sessionId":"grok-session","update":{"sessionUpdate":"tool_call","toolCallId":"list-1","title":"List \u0060docs\u0060","kind":"other","status":"in_progress","rawInput":{"variant":"ListDir","target_directory":"docs"},"content":[{"type":"content","content":{"type":"text","text":"listing docs"}}],"_meta":{"x.ai/tool":{"version":1,"name":"list_dir","kind":"list","namespace":"grok_build","label":"List Files","read_only":true,"input":{"directory":"docs"}}}}}`),
		json.RawMessage(`{"sessionId":"grok-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"list-1","status":"completed"}}`),
	}
	for _, params := range wire {
		if err := peer.SendNotification(ctx, MethodSessionUpdate, params); err != nil {
			t.Fatalf("session/update notification error = %v", err)
		}
	}

	got := make([]UpdateEnvelope, 0, len(wire))
	for range wire {
		select {
		case update := <-updates:
			got = append(got, update)
		case <-ctx.Done():
			t.Fatal("timed out waiting for standard tool lifecycle")
		}
	}

	providerMeta := grokListMeta(true)
	providerMeta[xAIToolMetaKey].(map[string]any)["version"] = float64(1)
	expectedCall := NormalizeInboundUpdate(ToolCall{
		SessionUpdate: UpdateToolCall,
		ToolCallID:    "list-1",
		Title:         "List `docs`",
		Kind:          "other",
		Status:        "in_progress",
		RawInput:      map[string]any{"variant": "ListDir", "target_directory": "docs"},
		Content: []ToolCallContent{{
			Type:    "content",
			Content: map[string]any{"type": "text", "text": "listing docs"},
		}},
		Meta: providerMeta,
	}).(ToolCall)
	completed := "completed"
	expected := []UpdateEnvelope{
		{SessionID: "grok-session", Update: expectedCall},
		{SessionID: "grok-session", Update: ToolCallUpdate{
			SessionUpdate: UpdateToolCallState,
			ToolCallID:    "list-1",
			Status:        &completed,
		}},
	}
	for i := range expected {
		if got[i].SessionID != expected[i].SessionID || !reflect.DeepEqual(got[i].Update, expected[i].Update) {
			t.Fatalf("wire update %d = %#v, want %#v", i, got[i], expected[i])
		}
	}

	var rawCall map[string]any
	if err := json.Unmarshal(got[0].Raw, &rawCall); err != nil {
		t.Fatalf("decode retained raw tool call: %v", err)
	}
	if rawCall["title"] != "List `docs`" || rawCall["kind"] != "other" || !reflect.DeepEqual(rawCall["rawInput"], map[string]any{
		"variant": "ListDir", "target_directory": "docs",
	}) {
		t.Fatalf("retained raw tool call = %#v, want original standard fields", rawCall)
	}
	if update := got[1].Update.(ToolCallUpdate); update.Title != nil || update.Kind != nil || update.RawInput != nil {
		t.Fatalf("sparse terminal update = %#v, want omitted fields to remain absent", update)
	}
}

func TestSDKWireIngressRestoresMissingGrokExecuteKindWithoutExactIdentity(t *testing.T) {
	t.Parallel()

	clientSide, peerSide := net.Pipe()
	updates := make(chan UpdateEnvelope, 1)
	acpClient, err := NewStreamClient(clientSide, clientSide, Config{
		OnUpdate: func(update UpdateEnvelope) { updates <- update },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer acpClient.Close(context.Background())
	peer, err := acpsdk.NewConnectionWithOptions(nil, peerSide, peerSide, acpsdk.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	wire := json.RawMessage(`{"sessionId":"grok-session","update":{"sessionUpdate":"tool_call","toolCallId":"execute-1","title":"run_terminal_command","rawInput":{"command":"git status --short"},"_meta":{"x.ai/tool":{"version":1,"name":"run_terminal_command","kind":"execute","namespace":"grok_build","label":"Run Command","read_only":false}}}}`)
	if err := peer.SendNotification(ctx, MethodSessionUpdate, wire); err != nil {
		t.Fatalf("session/update notification error = %v", err)
	}

	var got UpdateEnvelope
	select {
	case got = <-updates:
	case <-ctx.Done():
		t.Fatal("timed out waiting for Grok execute tool call")
	}
	call, ok := got.Update.(ToolCall)
	if !ok {
		t.Fatalf("wire update = %T, want ToolCall", got.Update)
	}
	rawInput, _ := call.RawInput.(map[string]any)
	if call.Kind != schema.ToolKindExecute || call.Title != "run_terminal_command" || rawInput["command"] != "git status --short" {
		t.Fatalf("normalized Grok execute call = %#v", call)
	}
	if exactName := metautil.String(call.Meta, metautil.Root, metautil.Runtime, metautil.RuntimeTool, metautil.RuntimeToolName); exactName != "" {
		t.Fatalf("runtime exact tool name = %q, want provider name to remain presentation-only evidence", exactName)
	}
	var rawCall map[string]any
	if err := json.Unmarshal(got.Raw, &rawCall); err != nil {
		t.Fatalf("decode retained raw execute call: %v", err)
	}
	if _, present := rawCall["kind"]; present {
		t.Fatalf("retained raw execute call = %#v, want original missing standard kind", rawCall)
	}
}

func TestSDKWireIngressPreservesOrderedIdenticalAssistantDeltas(t *testing.T) {
	t.Parallel()

	clientSide, peerSide := net.Pipe()
	updates := make(chan UpdateEnvelope, 3)
	acpClient, err := NewStreamClient(clientSide, clientSide, Config{
		OnUpdate: func(update UpdateEnvelope) { updates <- update },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer acpClient.Close(context.Background())
	peer, err := acpsdk.NewConnectionWithOptions(nil, peerSide, peerSide, acpsdk.ConnectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, text := range []string{"ha", "ha", "!"} {
		params := json.RawMessage(`{"sessionId":"child-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":` + string(mustMarshalRaw(text)) + `}}}`)
		if err := peer.SendNotification(ctx, MethodSessionUpdate, params); err != nil {
			t.Fatalf("session/update notification error = %v", err)
		}
	}

	var got []string
	for range 3 {
		select {
		case envelope := <-updates:
			chunk, ok := envelope.Update.(ContentChunk)
			if !ok {
				t.Fatalf("wire update = %T, want ContentChunk", envelope.Update)
			}
			if chunk.MessageID != "" {
				t.Fatalf("message id = %q, want real Grok omission preserved", chunk.MessageID)
			}
			var content TextChunk
			if err := json.Unmarshal(chunk.Content, &content); err != nil {
				t.Fatalf("decode assistant chunk content: %v", err)
			}
			got = append(got, content.Text)
		case <-ctx.Done():
			t.Fatal("timed out waiting for assistant deltas")
		}
	}
	if !reflect.DeepEqual(got, []string{"ha", "ha", "!"}) {
		t.Fatalf("assistant deltas = %#v, want ordered exact wire chunks", got)
	}
}

func TestConcurrentStderrTail(t *testing.T) {
	t.Parallel()

	client := &Client{}
	writer := stderrBufferWriter{client: client}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = writer.Write([]byte("diagnostic\n"))
			_ = client.StderrTail(128)
		}()
	}
	wg.Wait()
	if client.StderrTail(128) == "" {
		t.Fatal("stderr tail is empty")
	}
}
