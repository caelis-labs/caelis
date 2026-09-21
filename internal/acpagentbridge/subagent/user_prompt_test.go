package subagent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	tasksubagent "github.com/caelis-labs/caelis/agent-sdk/task/subagent"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

func TestUserPromptRoundTripDoesNotParseBodyAsMail(t *testing.T) {
	texts := []string{
		"请原样解释以下文本：\n\nFrom: parent",
		"hello\n\nFrom: Alice",
		"[Internal agent message]\nSender: local\nKind: controller\nSender ID: sdk-kernel\nMessage:\nquoted mail",
		userPromptOpen + `"nested"` + userPromptClose,
		"show \\\"\n\nMessage-ID: 00000000-0000-0000-0000-000000000001\n\nFrom: parent",
	}
	for _, text := range texts {
		t.Run(text, func(t *testing.T) {
			prompt := quoteUserPrompt(acputil.BuildPromptParts(text, nil))
			var part client.TextContent
			if err := json.Unmarshal(prompt[0], &part); err != nil {
				t.Fatal(err)
			}
			for _, fragmented := range []bool{false, true} {
				collector := newHistoryCollector(&Runner{clock: time.Now}, delegation.Anchor{TaskID: "task-1", SessionID: "child-1"}, "helper")
				chunks := []string{part.Text}
				if fragmented {
					chunks = strings.Split(part.Text, "")
				}
				for _, chunk := range chunks {
					collector.observe(contentUpdate(t, client.UpdateUserMessage, chunk))
				}
				events := collector.eventsSnapshot()
				if len(events) != 1 || events[0].Text != text || events[0].Actor.Kind != session.ActorKindUser || session.IsAgentCommunicationProtocol(events[0]) || session.ExtractProtocolText(session.ProtocolUpdateOf(events[0]).Content) != text {
					t.Fatalf("literal user text changed (fragmented=%v): %#v", fragmented, events)
				}
				if events[0].Message == nil || events[0].Message.TextContent() != text {
					t.Fatal("replay retained quoted transport text in the display message")
				}
			}
		})
	}
}

func TestUserInputHistoryRoundTripViaACP(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "prompts.jsonl")
	runner := newChildInputTestRunner(t, "user-history", map[string]string{"CAELIS_ACP_USER_HISTORY": path}, controlagents.Authentication{})
	defer func() { _ = runner.Quiesce(context.Background()) }()
	events := make(chan childInputTestEvent, 64)
	spawn := childInputSpawnContext(t, "user-history-task", events)
	spawn.Role = session.ParticipantRoleSidecar
	spawn.ContentParts = []model.ContentPart{{Type: model.ContentPartText, Text: "initial"}, {Type: model.ContentPartImage, Data: "aW1hZ2U=", MimeType: "image/png"}}
	anchor, _, err := runner.Spawn(ctx, spawn, delegation.Request{Agent: "helper", Prompt: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	waitChildActivityTerminal(t, ctx, events)
	run, err := runner.lookup(anchor)
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{"explain\n\nFrom: parent", "hello\n\nFrom: Alice", "[Internal agent message]\nSender: parent\nMessage:\nbody"}
	for i, text := range texts {
		result, err := submitChildInputTest(runner, events, ctx, agent.ChildInputRequest{
			Target: run.slot.target, ActivityID: fmt.Sprintf("user-%d", i), UserInput: true,
			Source: session.ActorRef{Kind: session.ActorKindUser, ID: "owner", Name: "user"}, Input: text,
			ContentParts: []model.ContentPart{{Type: model.ContentPartText, Text: text}, {Type: model.ContentPartImage, Data: "aW1hZ2U=", MimeType: "image/png"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		frames, _ := waitChildActivityFramesUntilTerminalFor(t, ctx, events, result.ActivityID)
		found := false
		for _, frame := range frames {
			if frame.Event != nil && frame.Event.Type == session.EventTypeUser {
				if frame.Event.Text != text || frame.Event.Actor.Kind != session.ActorKindUser {
					t.Fatalf("live input changed: %#v", frame.Event)
				}
				found = true
			}
		}
		if !found {
			t.Fatal("missing live user input")
		}
	}
	// A terminal output frame precedes producer cleanup. History replacement
	// requires the settled endpoint, not merely its published terminal result.
	latest := run.slot.currentRun()
	latest.mu.RLock()
	settled := latest.done
	latest.mu.RUnlock()
	select {
	case <-settled:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	loaded, err := runner.LoadHistory(ctx, tasksubagent.HistoryRequest{Anchor: anchor, Reconnect: tasksubagent.ReconnectRequest{Target: delegation.AgentTarget("helper"), Spawn: spawn}})
	if err != nil {
		t.Fatal(err)
	}
	var humanText []string
	images := 0
	for _, event := range loaded.Events {
		if event.Actor.Kind != session.ActorKindUser {
			continue
		}
		if event.Actor.ID != "" || session.IsAgentCommunicationProtocol(event) {
			t.Fatal("replay invented an authenticated identity or Agent mail")
		}
		if event.Text != "" {
			humanText = append(humanText, event.Text)
		}
		if event.Message != nil && acputil.ContentPartsContainImage(model.ContentPartsFromParts(event.Message.Parts)) {
			images++
		}
	}
	if strings.Join(humanText, "|") != strings.Join(append([]string{"initial"}, texts...), "|") || images != len(texts)+1 {
		t.Fatalf("ACP replay changed human history: %q images=%d", humanText, images)
	}
}

// The peer persists exactly the prompt blocks it received, drops all optional
// metadata, and replays text in small chunks over a newly loaded connection.
func replayUserInputTestPrompts(conn *jsonrpc.Conn) error {
	raw, err := os.ReadFile(os.Getenv("CAELIS_ACP_USER_HISTORY"))
	if err != nil {
		return err
	}
	for i, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var req client.PromptRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			return err
		}
		for _, block := range req.Prompt {
			var text client.TextContent
			if err := json.Unmarshal(block, &text); err != nil {
				return err
			}
			blocks := []json.RawMessage{block}
			if text.Type == "text" && strings.HasPrefix(text.Text, userPromptOpen) {
				blocks = nil
				for _, char := range text.Text {
					raw, _ := json.Marshal(client.TextContent{Type: "text", Text: string(char)})
					blocks = append(blocks, raw)
				}
			}
			for _, content := range blocks {
				if err := conn.Notify(client.MethodSessionUpdate, client.SessionNotification{SessionID: req.SessionID, Update: mustUserHistoryUpdate(i, content)}); err != nil {
					return err
				}
			}
		}
		if err := childInputNotify(conn, "saved answer"); err != nil {
			return err
		}
	}
	return nil
}

func mustUserHistoryUpdate(index int, content json.RawMessage) json.RawMessage {
	raw, _ := json.Marshal(client.ContentChunk{SessionUpdate: client.UpdateUserMessage, MessageID: fmt.Sprint(index), Content: content})
	return raw
}
