package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestDurableApprovalSeparatesPrivateEvidenceFromPresentation(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	input := json.RawMessage(`{"command":"git fetch","credential":"DURABLE-MARKED-SECRET"}`)
	original := &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "call", Name: "RunCommand", Title: "DURABLE-MARKED-SECRET", RawInput: map[string]any{"command": "git fetch", "credential": "DURABLE-MARKED-SECRET"}}, Options: []session.ProtocolApprovalOption{{ID: "native-allow", Name: "Allow", Kind: "allow_once"}, {ID: "native-reject", Name: "Reject", Kind: "reject_once"}}}
	req := agent.ApprovalRequest{SessionRef: active.SessionRef, Session: active, RunID: "run", TurnID: "turn", OperationTaskID: "worker-task",
		Tool: tool.Definition{Name: "RunCommand", InputSchema: map[string]any{"properties": map[string]any{"credential": map[string]any{"writeOnly": true}}}}, Call: tool.Call{ID: "call", Name: "RunCommand", Input: input}, Approval: original}
	decision := agent.ApprovalResponse{Outcome: "selected", OptionID: "native-allow", Approved: true, ReviewText: "DURABLE-MARKED-SECRET reviewed"}
	got, err := runtime.requestDurableApproval(t.Context(), req, approvalRequesterFunc(func(_ context.Context, observed agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		if !reflect.DeepEqual(observed.Approval, original) || string(observed.Call.Input) != string(input) {
			t.Fatal("reviewer did not receive exact action evidence")
		}
		return decision, nil
	}))
	if err != nil || got != decision {
		t.Fatalf("request = %+v, %v", got, err)
	}
	fresh := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	events, err := fresh.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var token *session.PauseToken
	for _, event := range events {
		if resolved := session.ResolvedApprovalReview(event); resolved != nil {
			token = resolved
		}
	}
	if token == nil || token.Presentation == nil || string(token.Input) != string(input) || token.Approval.ToolCall.RawInput["credential"] != "DURABLE-MARKED-SECRET" || token.ReviewText != decision.ReviewText || token.OptionID != decision.OptionID {
		t.Fatalf("private durable evidence changed: %+v", token)
	}
	raw, _ := json.Marshal(token.Presentation)
	if strings.Contains(string(raw), "DURABLE-MARKED-SECRET") || !token.Presentation.Subject.SensitiveInput || !reflect.DeepEqual(token.Presentation.Options, original.Options) {
		t.Fatalf("public snapshot leaked or changed options: %s", raw)
	}
	if page := session.PageEvents(events, session.EventPageRequest{Visibility: session.EventPageCanonical}); len(page.Events) != 0 {
		t.Fatal("private evidence entered normal model history")
	}
}

func TestDurableIndependentPermissionRestoresExactEvidenceWithoutTool(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user"})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	original := &session.ProtocolApproval{Context: &session.PermissionContext{Title: "Original prompt", RawSubject: json.RawMessage(`{"type":"_private","secret":"OPAQUE-PRIVATE-EVIDENCE"}`), NativeRequestID: json.RawMessage(`9007199254740991`)}, Subject: &session.ApprovalSubject{Kind: "opaque", OpaqueInput: true}, Options: []session.ProtocolApprovalOption{{ID: "native-reject", Name: " Reject original ", Kind: "reject_once"}}}
	req := agent.ApprovalRequest{SessionRef: active.SessionRef, Session: active, RunID: "run", TurnID: "turn", OperationTaskID: "worker", Approval: original}
	decision := agent.ApprovalResponse{Outcome: "selected", OptionID: "native-reject", ReviewText: "OPAQUE-PRIVATE-EVIDENCE reviewed"}
	_, err = runtime.requestDurableApproval(t.Context(), req, approvalRequesterFunc(func(_ context.Context, observed agent.ApprovalRequest) (agent.ApprovalResponse, error) {
		if !reflect.DeepEqual(observed.Approval, original) || observed.Call.ID != "" || observed.Tool.Name != "" {
			t.Fatal("independent permission lost exact evidence or acquired a fake tool")
		}
		return decision, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	events, err := sessionfile.NewStore(sessionfile.Config{RootDir: root}).Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	var token *session.PauseToken
	for _, event := range events {
		if resolved := session.ResolvedApprovalReview(event); resolved != nil {
			token = resolved
		}
	}
	if token == nil || token.ToolCallID != "" || token.ToolName != "" || !reflect.DeepEqual(token.Approval.Context, original.Context) || token.Approval.Options[0].Name != " Reject original " || token.OptionID != decision.OptionID || token.Presentation == nil || !token.Presentation.Subject.OpaqueInput {
		t.Fatalf("independent permission did not survive file restore: %+v", token)
	}
	raw, _ := json.Marshal(token.Presentation)
	if strings.Contains(string(raw), "OPAQUE-PRIVATE-EVIDENCE") || strings.Contains(string(raw), "9007199254740991") || token.Presentation.ToolCall.ID != "" || token.Presentation.ToolCall.Name != "" {
		t.Fatalf("private evidence escaped display or fake tool appeared: %s", raw)
	}
}
