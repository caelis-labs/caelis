package acpagentbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acpmeta"
)

func TestDraftProjectionPreservesExplicitNarrativeAndToolReplacement(t *testing.T) {
	for _, update := range []eventstream.Update{
		eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "native-message", Replacement: true, Content: eventstream.TextContent{Type: "text", Text: "replacement"}},
		eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "native-tool", Content: []eventstream.ToolCallContent{}, ReplacedFields: []string{"name", "locations", "rawInput", "rawOutput"}},
	} {
		projected, err := draftEnvelopeUpdate(eventstream.Envelope{SessionID: "session", Update: update}, false)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(projected)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err = json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if update.SessionUpdateType() == eventstream.UpdateAgentMessage {
			if string(fields["sessionUpdate"]) != `"agent_message"` || string(fields["messageId"]) != `"native-message"` || !strings.HasPrefix(string(fields["content"]), "[") {
				t.Fatalf("replacement=%s", encoded)
			}
		} else {
			for _, field := range []string{"name", "locations", "rawInput", "rawOutput"} {
				if string(fields[field]) != "null" {
					t.Errorf("%s was not cleared: %s", field, encoded)
				}
			}
			if string(fields["content"]) != "[]" {
				t.Errorf("empty content was lost: %s", encoded)
			}
		}
		if _, ok := fields["replacedFields"]; ok {
			t.Fatal("native replacement extension leaked to standard v2")
		}
		if _, ok := fields["replacement"]; ok {
			t.Fatal("native narrative replacement extension leaked to standard v2")
		}
	}
}

func TestDraftTerminalReplayReplacesPriorOutputAndPreservesLiveChunks(t *testing.T) {
	peer := &draftNativePeer{}
	s := &draftSession{agent: &DraftAgent{callbacks: peer}, id: "session", ctx: context.Background()}
	appendOutput := func(text, snapshotID string) {
		t.Helper()
		env := eventstream.Envelope{SessionID: s.id, Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "native-tool", Meta: acpmeta.WithTerminalOutput(nil, "terminal", text)}}
		if err := s.emitEnvelope(env, snapshotID != "", false, snapshotID); err != nil {
			t.Fatal(err)
		}
	}
	appendOutput("old", "")
	appendOutput("a", "snapshot-1")
	appendOutput("b", "snapshot-1")
	appendOutput("c", "")
	appendOutput("x", "snapshot-2")
	appendOutput("y", "snapshot-2")
	var output []byte
	var outputs []string
	for _, n := range peer.updates {
		if update := n.Update.TerminalUpdate; update != nil && update.Output != nil {
			data, err := base64.StdEncoding.DecodeString(update.Output.Data)
			if err != nil {
				t.Fatal(err)
			}
			output = data
		}
		if chunk := n.Update.TerminalOutputChunk; chunk != nil {
			data, err := base64.StdEncoding.DecodeString(chunk.Data)
			if err != nil {
				t.Fatal(err)
			}
			output = append(output, data...)
			outputs = append(outputs, string(output))
		}
	}
	if strings.Join(outputs, ",") != "old,a,ab,abc,x,xy" || len(s.terminalResets) != 1 {
		t.Fatalf("output sequence=%v reset generations=%d", outputs, len(s.terminalResets))
	}
}

func TestDraftProjectionUsesStandardTerminalAndPlanSnapshots(t *testing.T) {
	unknown := -1
	meta := acpmeta.WithTerminalExit(acpmeta.WithTerminalOutput(acpmeta.WithTerminalInfo(nil, "terminal"), "terminal", "output", true), "terminal", &unknown, nil)
	env := eventstream.Envelope{SessionID: "session", Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "native-tool", Meta: meta}}
	tool, err := draftEnvelopeUpdate(env, false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(tool)
	if strings.Contains(string(raw), "terminal_output") || strings.Contains(string(raw), "terminal_info") || strings.Contains(string(raw), "terminal_exit") {
		t.Fatalf("custom terminal payload survived: %s", raw)
	}
	updates, err := draftTerminalUpdates(env)
	if err != nil || len(updates) != 1 || updates[0].TerminalUpdate == nil {
		t.Fatalf("terminal=%#v err=%v", updates, err)
	}
	terminal := updates[0].TerminalUpdate
	if terminal.TerminalId != "terminal" || terminal.Output == nil || terminal.Output.Data != base64.StdEncoding.EncodeToString([]byte("output")) {
		t.Fatalf("snapshot=%#v", terminal)
	}
	raw, _ = json.Marshal(terminal)
	if strings.Contains(string(raw), "caelis/exit_code") || strings.Contains(string(raw), "-1") {
		t.Fatalf("unknown native exit fabricated a code: %s", raw)
	}
	plan, err := draftEnvelopeUpdate(eventstream.Envelope{SessionID: "session", ScopeID: "child", Update: eventstream.PlanUpdate{SessionUpdate: eventstream.UpdatePlan}}, true)
	if err != nil {
		t.Fatal(err)
	}
	var typed v2.SessionUpdate
	if err = draftJSONConvert(plan, &typed); err != nil || typed.PlanUpdate == nil || typed.PlanUpdate.Plan.Items == nil || typed.PlanUpdate.Plan.Items.PlanId != "native-plan:child" {
		t.Fatalf("plan=%#v err=%v", typed, err)
	}
}

func TestDraftProjectionConfigAndCommandsUseV2Identities(t *testing.T) {
	choices := acp.SessionConfigSelectOptionsUngrouped{{Value: "manual", Name: "Manual"}}
	options, err := draftConfigOptions([]acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{Type: "select", Id: "mode", Name: "Mode", CurrentValue: "manual", Options: acp.SessionConfigSelectOptions{Ungrouped: &choices}}}})
	if err != nil || len(options) != 1 || options[0].Select == nil || options[0].Select.ConfigId != "mode" || options[0].Select.CurrentValue != "manual" {
		t.Fatalf("options=%#v err=%v", options, err)
	}
	commands := draftCommands([]acp.AvailableCommand{{Name: "model", Description: "Select model", Input: &acp.AvailableCommandInput{Unstructured: &acp.UnstructuredCommandInput{Hint: "model effort"}}}})
	if len(commands) != 1 || commands[0].Input == nil || commands[0].Input.Text == nil || commands[0].Input.Text.Type != "text" || commands[0].Input.Text.Hint != "model effort" {
		t.Fatalf("commands=%#v", commands)
	}
}

func TestDraftIndependentPermissionPublishesOnlyBoundedDisplayFacts(t *testing.T) {
	const secret = "PRIVATE-RAW-SUBJECT"
	opaque := session.ApprovalField{State: session.ApprovalFieldOpaque}
	known := session.KnownApprovalField("Permission required")
	approval := &session.ProtocolApproval{Context: &session.PermissionContext{Title: secret, RawSubject: json.RawMessage(`{"private":"PRIVATE-RAW-SUBJECT"}`), NativeRequestID: json.RawMessage(`"private-request"`)},
		Subject: &session.ApprovalSubject{Title: &known, Kind: "opaque", Tool: opaque, Operation: opaque, Targets: []session.ApprovalField{opaque}, Effect: opaque, Scope: opaque, OpaqueInput: true},
		Options: []session.ProtocolApprovalOption{{ID: "native-option", Name: "Allow once", Kind: "allow_once"}}}
	request, err := draftPermissionRequest("session", approval)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(request)
	if request.Subject != nil || request.Title != "Permission required" || len(request.Options) != 1 || request.Options[0].OptionId != "native-option" || strings.Contains(string(raw), secret) || strings.Contains(string(raw), "private-request") {
		t.Fatalf("display=%s", raw)
	}
	var subject session.ApprovalSubject
	if err = json.Unmarshal(request.Meta["caelis/approval_subject"], &subject); err != nil || subject.Operation.State != session.ApprovalFieldOpaque || len(subject.Grants) != 1 || subject.Grants[0].OptionID != "native-option" {
		t.Fatalf("subject=%#v err=%v", subject, err)
	}
}
