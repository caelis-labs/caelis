package memorytool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/control/memorybinding"
	v1alpha1 "github.com/caelis-labs/memory/api/memory/v1alpha1"
)

func TestToolsExposeOnlyTextAndQueryAndPersistHiddenConsistency(t *testing.T) {
	ctx := context.Background()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: "session-memory"})
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding()
	if err := memorybinding.AdmitSession(ctx, store, active.SessionRef, binding); err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeCtx := session.ContextWithRuntimeFence(ctx, fence)
	defer func() { _ = store.ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(fence)) }()
	client := &fakeClient{
		remember: v1alpha1.RememberResponse{
			Accepted: true, ReceiptID: "receipt-private", ConsistencyToken: "token-remember",
			ProcessingState: v1alpha1.ProcessingStateAccepted,
		},
		recall: v1alpha1.RecallResponse{
			Fragments: []v1alpha1.RecallFragment{{
				FragmentID: "fragment-private", Text: "commit does not authorize push",
				EvidenceRefs: []v1alpha1.ReceiptID{"receipt-private"}, SpaceClass: v1alpha1.SpaceClassPrivate,
			}},
			ConsistencyToken: "token-recall",
		},
	}
	tools, err := New(Config{Client: client, Sessions: store, SessionRef: active.SessionRef, Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 || tools[0].Definition().Name != RememberToolName || tools[1].Definition().Name != RecallToolName {
		t.Fatalf("tools = %#v", tool.Definitions(tools))
	}
	for index, name := range []string{"text", "query"} {
		definition := tools[index].Definition()
		properties := definition.InputSchema["properties"].(map[string]any)
		if len(properties) != 1 || properties[name] == nil {
			t.Fatalf("%s schema = %#v", definition.Name, properties)
		}
		if definition.Capabilities.ParallelSafe {
			t.Fatalf("%s unexpectedly permits calls to bypass the Session causal order", definition.Name)
		}
	}

	remember, err := tools[0].Call(runtimeCtx, tool.Call{ID: "remember-call", Name: RememberToolName, Input: json.RawMessage(`{"text":"commit does not authorize push"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(remember.Content[0].JSON.Value); got != `{"accepted":true}` {
		t.Fatalf("Remember result = %s", got)
	}
	if raw, _ := json.Marshal(remember); strings.Contains(string(raw), "capability") || strings.Contains(string(raw), "issuer-super-secret") {
		t.Fatalf("Remember result leaked authority: %s", raw)
	}

	recall, err := tools[1].Call(runtimeCtx, tool.Call{ID: "recall-call", Name: RecallToolName, Input: json.RawMessage(`{"query":"commit push"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(recall.Content[0].JSON.Value); got != `{"fragments":["commit does not authorize push"]}` {
		t.Fatalf("Recall result = %s", got)
	}
	if client.recallToken != "token-remember" {
		t.Fatalf("Recall consistency token = %q", client.recallToken)
	}
	state, err := store.SnapshotState(ctx, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	stateRaw, _ := json.Marshal(state)
	if !strings.Contains(string(stateRaw), "token-recall") || strings.Contains(string(stateRaw), "commit does not authorize push") {
		t.Fatalf("hidden state = %s", stateRaw)
	}
}

func TestRememberRecoveryReusesStableEffectIdentity(t *testing.T) {
	ctx, store, ref, binding, cleanup := testToolRuntime(t)
	defer cleanup()
	client := &fakeClient{remember: v1alpha1.RememberResponse{Accepted: true, ReceiptID: "receipt", ConsistencyToken: "token", ProcessingState: v1alpha1.ProcessingStateAccepted}}
	configured, err := New(Config{Client: client, Sessions: store, SessionRef: ref, Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	remember := configured[0].(*rememberTool)
	call := tool.Call{ID: "remember-call", Name: RememberToolName, Input: json.RawMessage(`{"text":"fact"}`)}
	if _, err := remember.Call(ctx, call); err != nil {
		t.Fatal(err)
	}
	firstKey := client.rememberKeys[0]
	recovered, err := remember.Recover(ctx, tool.RecoveryRequest{ExecutionIdentity: "execution-a", Call: call})
	if err != nil || recovered.Status != tool.RecoverySucceeded {
		t.Fatalf("Recover() = %#v, %v", recovered, err)
	}
	if len(client.rememberKeys) != 2 || client.rememberKeys[1] != firstKey {
		t.Fatalf("Remember keys = %#v", client.rememberKeys)
	}
}

func TestUnavailableRecallIsErrorNotEmpty(t *testing.T) {
	ctx, store, ref, binding, cleanup := testToolRuntime(t)
	defer cleanup()
	client := &fakeClient{recallErr: &v1alpha1.ServiceError{Code: v1alpha1.ErrorCodeUnavailable, Message: "offline", Retryable: true}}
	configured, err := New(Config{Client: client, Sessions: store, SessionRef: ref, Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	_, err = configured[1].Call(ctx, tool.Call{ID: "recall-call", Name: RecallToolName, Input: json.RawMessage(`{"query":"anything"}`)})
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCode("unavailable") || !toolErr.Retryable {
		t.Fatalf("Recall unavailable error = %#v", err)
	}
}

func TestMemoryAdmissionFailurePrecedesRememberEffect(t *testing.T) {
	ctx, store, ref, binding, cleanup := testToolRuntime(t)
	defer cleanup()
	client := &fakeClient{remember: v1alpha1.RememberResponse{Accepted: true}}
	binding.RuntimeActorRef = "actor-b"
	configured, err := New(Config{Client: client, Sessions: store, SessionRef: ref, Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	_, err = configured[0].Call(ctx, tool.Call{ID: "remember-call", Name: RememberToolName, Input: json.RawMessage(`{"text":"must not commit"}`)})
	if err == nil || len(client.rememberKeys) != 0 {
		t.Fatalf("mismatched admission error=%v Remember calls=%d", err, len(client.rememberKeys))
	}
}

func TestUnknownRememberOutcomeRemainsRecoverableAndNeverClaimsFailure(t *testing.T) {
	ctx, store, ref, binding, cleanup := testToolRuntime(t)
	defer cleanup()
	client := &fakeClient{rememberErr: &v1alpha1.ServiceError{Code: v1alpha1.ErrorCodeUnknownOutcome, Message: "response lost", Retryable: true}}
	configured, err := New(Config{Client: client, Sessions: store, SessionRef: ref, Binding: binding})
	if err != nil {
		t.Fatal(err)
	}
	remember := configured[0].(*rememberTool)
	call := tool.Call{ID: "remember-call", Name: RememberToolName, Input: json.RawMessage(`{"text":"fact"}`)}
	_, err = remember.Call(ctx, call)
	var toolErr *tool.ToolError
	if !errors.As(err, &toolErr) || toolErr.Code != tool.ErrorCode("unknown_outcome") {
		t.Fatalf("Remember unknown outcome = %#v", err)
	}
	recovered, recoverErr := remember.Recover(ctx, tool.RecoveryRequest{Call: call})
	if recoverErr == nil || recovered.Status != tool.RecoveryUnknown || len(client.rememberKeys) != 2 || client.rememberKeys[0] != client.rememberKeys[1] {
		t.Fatalf("Remember recovery = %#v, %v keys=%#v", recovered, recoverErr, client.rememberKeys)
	}
}

func TestRememberEffectIdentityFollowsStableExecutionNotMutableInputOrBinding(t *testing.T) {
	config := Config{SessionRef: session.SessionRef{SessionID: "session"}, Binding: testBinding()}
	base := rememberIdempotencyKey(config, tool.Call{ID: "call-a"})
	if changed := rememberIdempotencyKey(config, tool.Call{ID: "call-b"}); changed == base {
		t.Fatal("Remember effect identity did not bind the stable tool call")
	}
	updated := config
	updated.Binding.ViewRef = "view-b"
	updated.Binding.GrantRef = "grant-b"
	updated.Binding.BindingVersion++
	if changed := rememberIdempotencyKey(updated, tool.Call{ID: "call-a"}); changed != base {
		t.Fatal("compatible binding renewal changed the Remember effect identity")
	}
}

type fakeClient struct {
	remember     v1alpha1.RememberResponse
	rememberErr  error
	recall       v1alpha1.RecallResponse
	recallErr    error
	rememberKeys []string
	recallToken  v1alpha1.ConsistencyToken
}

func (c *fakeClient) Remember(_ context.Context, _ string, key string, _ *time.Time) (v1alpha1.RememberResponse, error) {
	c.rememberKeys = append(c.rememberKeys, key)
	return c.remember, c.rememberErr
}

func (c *fakeClient) Recall(_ context.Context, _ string, token v1alpha1.ConsistencyToken) (v1alpha1.RecallResponse, error) {
	c.recallToken = token
	return c.recall, c.recallErr
}

func testToolRuntime(t *testing.T) (context.Context, *sessionmemory.Store, session.SessionRef, memorybinding.RuntimeMemoryBindingSnapshot, func()) {
	t.Helper()
	ctx := context.Background()
	store := sessionmemory.NewStore(sessionmemory.Config{})
	active, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user", PreferredSessionID: "session-memory"})
	if err != nil {
		t.Fatal(err)
	}
	binding := testBinding()
	if err := memorybinding.AdmitSession(ctx, store, active.SessionRef, binding); err != nil {
		t.Fatal(err)
	}
	fence, err := store.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeCtx := session.ContextWithRuntimeFence(ctx, fence)
	return runtimeCtx, store, active.SessionRef, binding, func() {
		_ = store.ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(fence))
	}
}

func testBinding() memorybinding.RuntimeMemoryBindingSnapshot {
	return memorybinding.RuntimeMemoryBindingSnapshot{
		BindingRef:      "binding-a",
		RuntimeActorRef: "actor-a", PrincipalRef: "principal:a",
		IssuerCredentialRef: "memory-issuer:" + strings.Repeat("a", 32),
		ViewRef:             "view-a", GrantRef: "grant-a",
		Audience: memorybinding.OutputAudiencePrivate, BindingVersion: 1,
	}
}
