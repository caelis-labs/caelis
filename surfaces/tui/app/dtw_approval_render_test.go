package tuiapp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/policy/presets"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/toolpolicy"
	"github.com/caelis-labs/caelis/internal/evalharness"
)

func TestDTWAPPApprovalRenderedScope(t *testing.T) {
	input := policy.ToolContext{Session: session.Session{SessionRef: session.SessionRef{SessionID: "fixture-session"}, CWD: "/task/workspace", CreatedAt: time.Unix(1000000000, 0).UTC()}, Tool: tool.Definition{Name: "dtw__look", Metadata: map[string]any{mcp.MetadataKind: mcp.KindTool, mcp.MetadataPluginID: "desktop-world", mcp.MetadataServer: "dtw", mcp.MetadataTool: "look", mcp.MetadataSourceFingerprint: "look-schema-v1"}}, Call: tool.Call{ID: "actual-call", Name: "dtw__look", Input: []byte(`{"app":"dev.caelis.fixture"}`)}}
	decision, err := (toolpolicy.Mode{Base: presets.WorkspaceWriteMode(), Authority: "fixture-owner"}).DecideTool(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	display := approval.ProtocolApprovalFromPayload(approval.DisplayPayloadFromRuntimeRequest(approval.Request{Session: input.Session, SessionRef: input.Session.SessionRef, Tool: input.Tool, Call: input.Call, Approval: decision.Approval, Authorization: decision.Authorization, Metadata: decision.Metadata}))
	permission := &eventstream.RequestPermissionRequest{ToolCall: eventstream.ToolCallUpdate{ToolCallID: display.ToolCall.ID, Name: new(display.ToolCall.Name), RawInput: display.ToolCall.RawInput}}
	for _, option := range display.Options {
		permission.Options = append(permission.Options, acpsdk.PermissionOption{OptionId: acpsdk.PermissionOptionId(option.ID), Name: option.Name, Kind: acpsdk.PermissionOptionKind(option.Kind)})
	}
	req := approvalPayloadFromACPEvent(eventstream.Envelope{Kind: eventstream.KindRequestPermission, ApprovalRequestID: "actual-approval", ApprovalSubject: display.Subject, Permission: permission})
	prompt := approvalToPromptRequest(req, make(chan PromptResponse, 1))
	model := NewModel(Config{InitialPrompt: &prompt, NoColor: true, NoAnimation: true})
	model.width, model.height = 120, 50
	frame := evalharness.NormalizeFrame(model.renderPromptModal()) + "\n"
	for _, want := range []string{"dev.caelis.fixture", "all 14 DTW atomic", "Allow this app for this Session", "Cancel"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("APP permission omitted %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "Allow Always") || strings.Contains(frame, "approval_reason") {
		t.Fatalf("APP permission offered widened scope or synthetic arguments:\n%s", frame)
	}
	path := filepath.Join("testdata", "approval", "dtw-app-session.golden")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(frame), 0600); err != nil {
			t.Fatal(err)
		}
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(expected) != frame {
		t.Fatalf("rendered APP permission differs from %s:\n%s", path, frame)
	}
}
