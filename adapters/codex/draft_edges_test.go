package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	acp "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
)

func TestDraftNativeSkillMentionHistory(t *testing.T) {
	var thread historyThread
	raw := `{"id":"thread-1","cwd":"/workspace","turns":[{"id":"turn-1","status":"completed","items":[{"id":"user-1","type":"userMessage","content":[{"type":"text","text":"use this skill"},{"type":"skill","name":"pdf","path":"/workspace/skills/pdf/SKILL.md"},{"type":"mention","name":"README.md","path":"/workspace/README.md"},{"type":"mention","name":"calendar","path":"app://calendar"}]}]}]}`
	if err := json.Unmarshal([]byte(raw), &thread); err != nil {
		t.Fatal(err)
	}
	stable, err := historyUpdates(thread, terminalOutputLegacy)
	if err != nil || len(stable) != 1 || stable[0].UserMessageChunk.Content.Text.Text != "use this skill" {
		t.Fatalf("stable history behavior changed: %+v, %v", stable, err)
	}
	updates, err := draftHistoryUpdates(thread, terminalOutputLegacy)
	if err != nil || len(updates) != 4 {
		t.Fatalf("valid native skill/mention history cannot replay: %+v, %v", updates, err)
	}
	for i, want := range []struct{ name, uri string }{{"pdf", "file:///workspace/skills/pdf/SKILL.md"}, {"README.md", "file:///workspace/README.md"}, {"calendar", "app://calendar"}} {
		message := updates[i+1].UserMessageChunk
		link := message.Content.ResourceLink
		if message.MessageId == nil || *message.MessageId != acp.MessageId(stableID(thread.ID, "user-1", "user-message")) || link == nil || link.Name != want.name || link.Uri != want.uri {
			t.Fatalf("reference identity changed: %+v, want %+v", message, want)
		}
		if _, err := draftUpdates(thread.ID, updates[i+1]); err != nil {
			t.Fatalf("reference is not a valid alpha.8 update: %v", err)
		}
	}
}

func TestDraftNativeReferencesPreserveNamesAndTargets(t *testing.T) {
	for _, test := range []struct{ kind, name, path, uri string }{
		{"skill", "pdf", "/missing/skill #1/SKILL.md", "file:///missing/skill%20%231/SKILL.md"},
		{"mention", "calendar", "app://calendar", "app://calendar"},
		{"mention", "docs", "mcp://docs", "mcp://docs"},
		{"mention", "team", "skill://team/skill", "skill://team/skill"},
		{"mention", "editor", "plugin://sample@test?app=com.example.editor", "plugin://sample@test?app=com.example.editor"},
		{"mention", "relative", "docs/README.md", "docs/README.md"},
	} {
		t.Run(test.kind+"/"+test.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"type": test.kind, "name": test.name, "path": test.path})
			block, err := draftUserContent(raw)
			if err != nil || block.ResourceLink == nil || block.ResourceLink.Name != test.name || block.ResourceLink.Uri != test.uri {
				t.Fatalf("reference = %+v, %v; want %s -> %s", block, err, test.name, test.uri)
			}
		})
	}
}

func TestDraftResumeNativeSkillMentionReferencesOverSDK(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	contents := []any{map[string]any{"type": "skill", "name": "fixture", "path": cwd + "/missing skill/SKILL.md"}, map[string]any{"type": "mention", "name": "calendar", "path": "app://calendar"}}
	history := map[string]any{"id": "thread-1", "cwd": cwd, "turns": []any{map[string]any{"id": "turn-1", "status": "completed", "items": []any{map[string]any{"id": "user-1", "type": "userMessage", "content": contents}}}}}
	done := make(chan error, 1)
	go func() {
		_, err := client.ResumeSession(ctx, v2.ResumeSessionRequest{SessionId: "thread-1", Cwd: v2.AbsolutePath(cwd), ReplayFrom: &v2.ReplayFrom{Start: &v2.ReplayFromStartInline{Type: "start"}}})
		done <- err
	}()
	fake.reply(fake.next(t, "thread/read"), map[string]any{"thread": map[string]any{"id": "thread-1", "cwd": cwd}})
	fake.reply(fake.next(t, "thread/resume"), map[string]any{"thread": map[string]any{"id": "thread-1", "cwd": cwd}, "model": "gpt-test"})
	for range 2 {
		fake.reply(fake.next(t, "thread/read"), map[string]any{"thread": history})
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	id := v2.MessageId(stableID("thread-1", "user-1", "user-message"))
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		chunk := update.Update.UserMessageChunk
		return chunk != nil && chunk.MessageId == id && chunk.Content.ResourceLink != nil && chunk.Content.ResourceLink.Name == "calendar" && chunk.Content.ResourceLink.Uri == "app://calendar"
	})
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, stop, activity := draftState(update)
		return state == "idle" && stop == "end_turn" && activity == "turn-1"
	})
	// Live and restored references use the same native content mapper and IDs.
	go func() {
		_, err := client.Prompt(ctx, v2.PromptRequest{SessionId: "thread-1", Prompt: []v2.ContentBlock{v2.TextBlock("continue")}})
		done <- err
	}()
	req := fake.next(t, "turn/start")
	fake.reply(req, map[string]any{"turn": map[string]any{"id": "turn-2"}})
	fake.mu.Lock()
	token := fake.tokens["thread-1"]
	fake.mu.Unlock()
	fake.notify("item/started", map[string]any{"threadId": "thread-1", "turnId": "turn-2", "item": map[string]any{"id": "user-2", "clientId": token, "type": "userMessage", "content": contents}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		message := update.Update.UserMessage
		return message != nil && message.MessageId == v2.MessageId(stableID("thread-1", "user-2", "user-message")) && len(message.Content) == 2 && message.Content[0].ResourceLink != nil && message.Content[0].ResourceLink.Name == "fixture" && message.Content[1].ResourceLink != nil && message.Content[1].ResourceLink.Uri == "app://calendar"
	})
	finishDraftTurn(fake, "thread-1", "turn-2", "completed")
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, _, activity := draftState(update)
		return state == "idle" && activity == "turn-2"
	})
}

func TestDraftTerminalSignedExitStatus(t *testing.T) {
	for _, code := range []int64{-1, math.MinInt32, 0, 1, math.MaxInt32, math.MaxUint32} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"terminal_id": "terminal", "exit_code": code, "signal": "SIGTERM"})
			updates, err := draftTerminalUpdates(map[string]json.RawMessage{terminalExitMetaKey: raw})
			if err != nil || len(updates) != 1 || updates[0].TerminalUpdate.ExitStatus == nil {
				t.Fatalf("native terminal exit = %+v, %v", updates, err)
			}
			exit := updates[0].TerminalUpdate.ExitStatus
			if exit.Signal == nil || *exit.Signal != "SIGTERM" || code < 0 && exit.ExitCode != nil || code >= 0 && (exit.ExitCode == nil || int64(*exit.ExitCode) != code) {
				t.Fatalf("native exit %d mapped to %+v", code, exit)
			}
			if err := updates[0].Validate(); err != nil {
				t.Fatalf("not a valid standard terminal update: %v", err)
			}
		})
	}
	raw := json.RawMessage(`{"terminal_id":"terminal","exit_code":4294967296}`)
	if _, err := draftTerminalUpdates(map[string]json.RawMessage{terminalExitMetaKey: raw}); err == nil {
		t.Fatal("out-of-range terminal exit was silently truncated")
	}
}

func TestDraftNegativeNativeExitAfterReceipt(t *testing.T) {
	fake, client, recorder, ctx, cwd := newDraftFixture(t)
	openDraftSession(t, fake, client, ctx, cwd, "thread-1")
	receipt := acceptDraftPrompt(t, fake, client, ctx, "thread-1", "turn-1", "user-1", "run command")
	fake.notify("item/started", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "command-1", "type": "commandExecution", "cwd": cwd, "command": "test command"}})
	fake.notify("item/completed", map[string]any{"threadId": "thread-1", "turnId": "turn-1", "item": map[string]any{"id": "command-1", "type": "commandExecution", "cwd": cwd, "command": "test command", "aggregatedOutput": "process ended", "exitCode": -1, "status": "failed"}})
	terminal := recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		return update.Update.TerminalUpdate != nil && update.Update.TerminalUpdate.ExitStatus != nil
	})
	if terminal.Update.TerminalUpdate.ExitStatus.ExitCode != nil {
		t.Fatalf("negative native sentinel became an ACP exit code: %+v", terminal.Update.TerminalUpdate)
	}
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		return update.Update.TerminalOutputChunk != nil && update.Update.TerminalOutputChunk.Data == base64.StdEncoding.EncodeToString([]byte("process ended"))
	})
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		return update.Update.ToolCallUpdate != nil && update.Update.ToolCallUpdate.Status != nil && *update.Update.ToolCallUpdate.Status == v2.ToolCallStatusFailed
	})
	assertDraftNoIdle(t, recorder, "turn-1")
	if route := fake.backend.route("thread-1"); route == nil || route.failure() != nil {
		t.Fatalf("negative sentinel dropped the native route: %+v", route)
	}
	finishDraftTurn(fake, "thread-1", "turn-1", "completed")
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, stop, activity := draftState(update)
		return state == "idle" && stop == "end_turn" && activity == "turn-1"
	})
	if receipt.MessageId != v2.MessageId(stableID("thread-1", "user-1", "user-message")) {
		t.Fatalf("receipt identity changed: %+v", receipt)
	}
	acceptDraftPrompt(t, fake, client, ctx, "thread-1", "turn-2", "user-2", "continue")
	finishDraftTurn(fake, "thread-1", "turn-2", "completed")
	recorder.wait(t, func(update v2.UpdateSessionNotification) bool {
		state, _, activity := draftState(update)
		return state == "idle" && activity == "turn-2"
	})
}
