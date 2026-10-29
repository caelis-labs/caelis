package acpagentbridge

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/aymanbagabas/go-udiff"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func TestDraftDiffProjectionConvertsLegacyFilesAndPreservesStructuredFacts(t *testing.T) {
	old, unchanged := "old line\n", "same\n"
	emptyChanges := []eventstream.ToolDiffChange{}
	changes := []eventstream.ToolDiffChange{{Operation: "delete", Path: "/tmp/removed"}}
	patch := &eventstream.ToolDiffPatch{Meta: map[string]any{"large": json.Number("9007199254740993"), "huge": json.Number("1e400")}, Format: "git_patch", Text: "diff --git a/file b/file\r\nexact patch bytes\r\n"}
	for _, tc := range []struct {
		name      string
		content   eventstream.ToolCallContent
		operation string
		wantPatch bool
	}{
		{"add", eventstream.ToolCallContent{Type: "diff", Path: "/tmp/file", NewText: "new line\n"}, "add", true},
		{"modify", eventstream.ToolCallContent{Type: "diff", Path: "/tmp/file", OldText: &old, NewText: "new line\n"}, "modify", true},
		{"clear is modify", eventstream.ToolCallContent{Type: "diff", Path: "/tmp/file", OldText: &old}, "modify", true},
		{"empty new file", eventstream.ToolCallContent{Type: "diff", Path: "/tmp/file"}, "add", false},
		{"unchanged contents", eventstream.ToolCallContent{Type: "diff", Path: "/tmp/file", OldText: &unchanged, NewText: unchanged}, "modify", false},
		{"explicit no changes", eventstream.ToolCallContent{Type: "diff", Changes: &emptyChanges}, "", false},
		{"explicit delete and patch", eventstream.ToolCallContent{Type: "diff", Changes: &changes, Patch: patch, Meta: map[string]any{"display": "source"}}, "delete", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.content)
			if err != nil {
				t.Fatal(err)
			}
			update, err := draftEnvelopeUpdate(eventstream.Envelope{SessionID: "session", Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "tool", Content: []eventstream.ToolCallContent{tc.content}}})
			if err != nil {
				t.Fatal(err)
			}
			var fields struct {
				Content []map[string]json.RawMessage `json:"content"`
			}
			if err := draftJSONConvert(update, &fields); err != nil {
				t.Fatal(err)
			}
			item := fields.Content[0]
			for _, field := range []string{"path", "oldText", "newText"} {
				if _, leaked := item[field]; leaked {
					t.Fatalf("legacy %s leaked into draft", field)
				}
			}
			var gotChanges []eventstream.ToolDiffChange
			if err := json.Unmarshal(item["changes"], &gotChanges); err != nil {
				t.Fatal(err)
			}
			if tc.operation == "" {
				if string(item["changes"]) != "[]" {
					t.Fatalf("explicit empty changes=%s", item["changes"])
				}
			} else if len(gotChanges) != 1 || gotChanges[0].Operation != tc.operation {
				t.Fatalf("changes=%s", item["changes"])
			}
			var gotPatch *eventstream.ToolDiffPatch
			if err := json.Unmarshal(item["patch"], &gotPatch); len(item["patch"]) != 0 && err != nil {
				t.Fatal(err)
			}
			if (gotPatch != nil) != tc.wantPatch {
				t.Fatalf("patch=%#v", gotPatch)
			}
			if tc.content.Patch != nil {
				if !reflect.DeepEqual(gotPatch, patch) || !strings.Contains(string(item["_meta"]), "source") {
					t.Fatalf("structured patch/metadata changed: %s", item)
				}
			} else if gotPatch != nil {
				if gotPatch.Format != "git_patch" || !strings.HasPrefix(gotPatch.Text, "diff --git ") {
					t.Fatalf("rendered patch=%#v", gotPatch)
				}
				old := ""
				if tc.content.OldText != nil {
					old = *tc.content.OldText
				}
				body := strings.SplitN(gotPatch.Text, "\n", 2)[1]
				applied, err := udiff.ApplyUnified(body, old)
				if err != nil || applied != tc.content.NewText {
					t.Fatalf("patch does not represent original file: %q err=%v", applied, err)
				}
			}
			after, _ := json.Marshal(tc.content)
			if string(after) != string(before) {
				t.Fatal("draft conversion mutated stable whole-file content")
			}
		})
	}
}

func TestDraftLegacyDiffUsesActualSDKWireWithoutClosingObservation(t *testing.T) {
	f := newDraftNativeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client, _, peer := connectDraftNative(t, f)
	created, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
	if err != nil {
		t.Fatal(err)
	}
	id := string(created.SessionId)
	done := make(chan error, 1)
	go func() { _, err := client.Prompt(ctx, draftNativePrompt(id, "change file")); done <- err }()
	input := <-f.inserts
	f.insert(input)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	feed := f.feeds[id]
	f.mu.Unlock()
	old := "before\n"
	if err := feed.Publish(eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: id, TurnID: input.target.TurnID,
		Update: eventstream.ToolCallUpdate{SessionUpdate: eventstream.UpdateToolCallInfo, ToolCallID: "write-tool", Content: []eventstream.ToolCallContent{{Type: "diff", Path: "/tmp/file", OldText: &old, NewText: "after\n"}}}}); err != nil {
		t.Fatal(err)
	}
	f.finish(id, false)
	for {
		peer.mu.Lock()
		var diff, terminal bool
		for _, n := range peer.updates {
			if tool := n.Update.ToolCallUpdate; tool != nil && string(tool.ToolCallId) == "write-tool" {
				diff = len(tool.Content) == 1 && tool.Content[0].Diff != nil && len(tool.Content[0].Diff.Changes) == 1 && tool.Content[0].Diff.Changes[0].Modify != nil
			}
			if n.Update.StateUpdate != nil && n.Update.StateUpdate.StateUpdate.Idle != nil {
				_, reason := draftCollectedState(t, n)
				terminal = terminal || reason == "end_turn"
			}
		}
		peer.mu.Unlock()
		if diff && terminal {
			return
		}
		select {
		case <-peer.changed:
		case <-ctx.Done():
			t.Fatalf("diff=%v terminal=%v; observation closed or payload rejected", diff, terminal)
		}
	}
}

func TestDraftDiffPermissionSubjectPreservesOriginalOptionsScopeAndResolve(t *testing.T) {
	old := "before\n"
	changes := []session.ProtocolDiffChange{{Operation: "modify", Path: "/tmp/file"}}
	for _, content := range []session.ProtocolToolCallContent{
		{Type: "diff", Path: "/tmp/file", OldText: &old, NewText: "after\n"},
		{Type: "diff", Changes: &changes, Patch: &session.ProtocolDiffPatch{Format: "git_patch", Text: "exact rendered patch\r\n"}},
	} {
		name := "legacy"
		if content.Changes != nil {
			name = "structured"
		}
		t.Run(name, func(t *testing.T) {
			f := newDraftNativeFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			client, _, peer := connectDraftNative(t, f)
			created, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(t.TempDir())})
			if err != nil {
				t.Fatal(err)
			}
			id := string(created.SessionId)
			done := make(chan error, 1)
			go func() { _, err := client.Prompt(ctx, draftNativePrompt(id, "edit")); done <- err }()
			input := <-f.inserts
			f.insert(input)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			approval := &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "original-edit-tool", Title: "Edit file", Content: []session.ProtocolToolCallContent{content}},
				Options: []session.ProtocolApprovalOption{{ID: "original-allow", Name: "Allow once", Kind: "allow_once"}, {ID: "original-deny", Name: "Reject", Kind: "reject_once"}},
				Context: &session.PermissionContext{Title: "Review edit", NativeRequestID: json.RawMessage(`"PRIVATE-NATIVE-ID"`), RawSubject: json.RawMessage(`{"type":"tool_call","toolCall":{"toolCallId":"original-edit-tool"}}`)}}
			subject := &session.ApprovalSubject{Kind: "file", Operation: session.KnownApprovalField("write"), Targets: []session.ApprovalField{session.KnownApprovalField("/tmp/file")},
				Scope: session.KnownApprovalField("one operation"), Grants: []session.ApprovalGrant{{OptionID: "original-allow", Scope: session.KnownApprovalField("this operation")}}}
			before := session.CloneProtocolApproval(*approval)
			f.mu.Lock()
			state := f.states[id]
			state.Run.WaitingApproval = true
			state.Approval.Active = &appserver.ActiveApproval{RequestID: "original-edit-request", Target: input.target, Permission: approval, Subject: subject}
			f.states[id] = state
			feed := f.feeds[id]
			f.mu.Unlock()
			if err := feed.Publish(eventstream.Envelope{Kind: eventstream.KindRequestPermission, SessionID: id, ApprovalRequestID: "original-edit-request"}); err != nil {
				t.Fatal(err)
			}
			select {
			case request := <-peer.permissions:
				if err := request.Validate(); err != nil {
					t.Fatal(err)
				}
				if request.Subject == nil || request.Subject.ToolCall == nil {
					t.Fatalf("tool subject=%#v", request.Subject)
				}
				tool := request.Subject.ToolCall.ToolCall
				if tool.ToolCallId != "original-edit-tool" || len(tool.Content) != 1 || tool.Content[0].Diff == nil || len(tool.Content[0].Diff.Changes) != 1 {
					t.Fatalf("diff subject=%#v", tool)
				}
				if len(request.Options) != 2 || request.Options[0].OptionId != "original-allow" || request.Options[1].OptionId != "original-deny" {
					t.Fatalf("native options changed=%#v", request.Options)
				}
				var display session.ApprovalSubject
				if err := json.Unmarshal(request.Meta["caelis/approval_subject"], &display); err != nil {
					t.Fatal(err)
				}
				if display.Scope.Text != "one operation" || len(display.Grants) != 2 || display.Grants[0].OptionID != "original-allow" || display.Grants[0].Scope.Text != "this operation" || display.Grants[1].OptionID != "original-deny" || display.Grants[1].Scope.State != session.ApprovalFieldUnavailable {
					t.Fatalf("native display scope changed=%#v", display)
				}
				wire, _ := json.Marshal(request)
				if strings.Contains(string(wire), "PRIVATE-NATIVE-ID") || strings.Contains(string(wire), "raw_subject") {
					t.Fatal("private native identity/evidence leaked to draft")
				}
			case <-ctx.Done():
				t.Fatal("diff permission did not reach actual SDK peer")
			}
			peer.permissionResponse <- v2.RequestPermissionResponse{Outcome: v2.RequestPermissionOutcome{Selected: &v2.RequestPermissionOutcomeSelected{Outcome: "selected", OptionId: "original-allow"}}}
			select {
			case resolved := <-f.resolved:
				if resolved.Target != input.target || resolved.ApprovalRequestID != "original-edit-request" || resolved.OptionID != "original-allow" {
					t.Fatalf("native resolve changed=%#v", resolved)
				}
			case <-ctx.Done():
				t.Fatal("original diff approval did not resolve")
			}
			if !reflect.DeepEqual(before, *approval) {
				t.Fatal("draft projection changed native approval/context")
			}
			f.finish(id, false)
		})
	}
}
