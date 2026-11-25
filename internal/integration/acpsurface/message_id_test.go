package acpsurface

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/testenv"
)

func TestProductACPStreamingChunksShareOneMessageID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	workspace := t.TempDir()
	provider := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, delta := range []string{"CAELIS", "_ACP", "_SMOKE_OK"} {
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"chunk\",\"object\":\"chat.completion.chunk\",\"model\":\"messageid-test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":%q}}]}\n\n", delta)
		}
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chunk\",\"object\":\"chat.completion.chunk\",\"model\":\"messageid-test\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"chunk\",\"object\":\"chat.completion.chunk\",\"model\":\"messageid-test\",\"choices\":[],\"usage\":{\"prompt_tokens\":4280,\"completion_tokens\":44,\"total_tokens\":4324}}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))

	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acpagent-messageid-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: "acp-messageid-workspace",
		WorkspaceCWD: workspace,
		ApprovalMode: "auto-review",
		SkillDirs:    []string{t.TempDir()},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
		Model: gatewayapp.ModelConfig{
			Provider:            "custom-chat",
			API:                 model.APIOpenAICompatible,
			Model:               "messageid-test",
			BaseURL:             provider.URL,
			HTTPClient:          provider.Client(),
			Token:               "messageid-test-token",
			AuthType:            model.AuthBearerToken,
			Timeout:             2 * time.Second,
			ContextWindowTokens: 1_000_000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })

	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: workspace})
	if err != nil {
		t.Fatal(err)
	}

	live := &recordingCallbacks{}
	result, err := agent.Prompt(ctx, textPromptRequest(string(created.SessionId), "reply once"), live)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != acpsdk.StopReasonEndTurn {
		t.Fatalf("StopReason = %q, want end_turn", result.StopReason)
	}

	liveIDs, liveText := agentMessageIdentities(live.notifications)
	if len(liveIDs) < 2 {
		t.Fatalf("live stream chunks = %#v, want multi-chunk streaming so shared messageId is non-vacuous", liveIDs)
	}
	if strings.TrimSpace(liveText) != "CAELIS_ACP_SMOKE_OK" {
		t.Fatalf("live assistant text = %q, want CAELIS_ACP_SMOKE_OK", liveText)
	}
	if !allEqualNonEmpty(liveIDs) {
		t.Fatalf("live agent_message_chunk messageIds drifted: %#v", liveIDs)
	}
	liveUsage := usageUpdates(live.notifications)
	if len(liveUsage) != 1 || liveUsage[0].Used != 4324 || liveUsage[0].Size != 1_000_000 {
		t.Fatalf("live usage updates = %#v, want one 4324/1000000 update", liveUsage)
	}

	replayed := &recordingCallbacks{}
	if _, err := agent.LoadSession(ctx, acpsdk.LoadSessionRequest{
		SessionId: created.SessionId,
		Cwd:       workspace,
	}, replayed); err != nil {
		t.Fatal(err)
	}
	replayIDs, replayText := agentMessageIdentities(replayed.notifications)
	if len(replayIDs) == 0 {
		t.Fatal("LoadSession replay emitted no agent_message_chunk updates")
	}
	if strings.TrimSpace(replayText) != "CAELIS_ACP_SMOKE_OK" {
		t.Fatalf("replay assistant text = %q, want CAELIS_ACP_SMOKE_OK", replayText)
	}
	if !allEqualNonEmpty(replayIDs) {
		t.Fatalf("replay agent_message_chunk messageIds drifted: %#v", replayIDs)
	}
	if replayIDs[0] != liveIDs[0] {
		t.Fatalf("replay messageId = %q, want live identity %q", replayIDs[0], liveIDs[0])
	}
	replayUsage := usageUpdates(replayed.notifications)
	if len(replayUsage) != 1 || replayUsage[0].Used != 4324 || replayUsage[0].Size != 1_000_000 {
		t.Fatalf("replay usage updates = %#v, want one 4324/1000000 update", replayUsage)
	}

	// A later logical assistant message must rotate identity.
	second := &recordingCallbacks{}
	if _, err := agent.Prompt(ctx, textPromptRequest(string(created.SessionId), "reply again"), second); err != nil {
		t.Fatal(err)
	}
	secondIDs, _ := agentMessageIdentities(second.notifications)
	if !allEqualNonEmpty(secondIDs) {
		t.Fatalf("second-turn messageIds drifted: %#v", secondIDs)
	}
	if secondIDs[0] == liveIDs[0] {
		t.Fatalf("second logical message reused first messageId %q", liveIDs[0])
	}

	// Durable history must still distinguish the two logical messages after reconnect.
	reloaded := &recordingCallbacks{}
	if _, err := agent.LoadSession(ctx, acpsdk.LoadSessionRequest{
		SessionId: created.SessionId,
		Cwd:       workspace,
	}, reloaded); err != nil {
		t.Fatal(err)
	}
	reloadedIDs, _ := agentMessageIdentities(reloaded.notifications)
	unique := uniqueNonEmpty(reloadedIDs)
	if len(unique) != 2 {
		t.Fatalf("reloaded messageIds = %#v, want exactly two logical assistant identities", unique)
	}
	if !containsString(unique, liveIDs[0]) || !containsString(unique, secondIDs[0]) {
		t.Fatalf("reloaded messageIds = %#v, want live=%q and second=%q", unique, liveIDs[0], secondIDs[0])
	}
}

func TestProductACPRetriesRateLimitAfterStreamedText(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	const partial = "Partial answer."
	const recovered = "Recovered answer."
	workspace := t.TempDir()
	var calls atomic.Int32
	provider := testenv.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read provider input: %v", err)
			return
		}
		attempt := calls.Add(1)
		if strings.Contains(string(body), partial) || (attempt == 3 && !strings.Contains(string(body), recovered)) {
			t.Errorf("provider input did not reflect the accepted response on request %d", attempt)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", partial)
			_, _ = fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"tokens\",\"code\":\"rate_limit_exceeded\",\"message\":\"Please try again in 0.001s.\"}}\n\n")
			return
		}
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", recovered)
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}]}}\n\n", recovered)
	}))
	stack, err := newACPAgentTestStack(t, gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "acp-retry-test",
		StoreDir:     t.TempDir(),
		WorkspaceKey: "acp-retry-workspace",
		WorkspaceCWD: workspace,
		SkillDirs:    []string{t.TempDir()},
		Sandbox:      gatewayapp.SandboxConfig{RequestedType: "host"},
		Model: gatewayapp.ModelConfig{
			Provider: "custom-responses", API: model.APIOpenAIResponses, Model: "retry-test",
			BaseURL: provider.URL, HTTPClient: provider.Client(),
			Token: "synthetic-token", AuthType: model.AuthAPIKey,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	agent, err := newTestAgentFromStack(stack)
	if err != nil {
		t.Fatal(err)
	}
	created, err := agent.NewSession(ctx, acpsdk.NewSessionRequest{Cwd: workspace})
	if err != nil {
		t.Fatal(err)
	}
	callbacks := &recordingCallbacks{}
	result, err := agent.Prompt(ctx, textPromptRequest(string(created.SessionId), "reply once"), callbacks)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != acpsdk.StopReasonEndTurn || calls.Load() != 2 {
		t.Fatalf("stop reason = %q, requests = %d", result.StopReason, calls.Load())
	}
	liveIDs, liveText := agentMessageIdentities(callbacks.notifications)
	ids := uniqueNonEmpty(liveIDs)
	if len(ids) != 2 || liveText != partial+recovered {
		t.Fatalf("append-only stream = %q, message IDs = %v", liveText, liveIDs)
	}
	replay := &recordingCallbacks{}
	if _, err := agent.LoadSession(ctx, acpsdk.LoadSessionRequest{SessionId: created.SessionId, Cwd: workspace}, replay); err != nil {
		t.Fatal(err)
	}
	// ACP can replay the exact display history while the live spool retains it.
	// The next provider request below verifies the rebuilt model context.
	replayIDs, replayText := agentMessageIdentities(replay.notifications)
	if replayText != liveText || !slices.Equal(replayIDs, liveIDs) {
		t.Fatalf("replayed stream = %q, message IDs = %v, want exact display history", replayText, replayIDs)
	}
	if _, err := agent.Prompt(ctx, textPromptRequest(string(created.SessionId), "continue"), &recordingCallbacks{}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("provider requests = %d, want 3", calls.Load())
	}
}

func agentMessageIdentities(notifications []eventstream.SessionNotification) ([]string, string) {
	ids := make([]string, 0, len(notifications))
	var text strings.Builder
	for _, notification := range notifications {
		chunk, ok := notification.Update.(eventstream.ContentChunk)
		if !ok || chunk.SessionUpdate != eventstream.UpdateAgentMessage {
			continue
		}
		ids = append(ids, strings.TrimSpace(chunk.MessageID))
		switch content := chunk.Content.(type) {
		case eventstream.TextContent:
			text.WriteString(content.Text)
		case map[string]any:
			if value, _ := content["text"].(string); value != "" {
				text.WriteString(value)
			}
		}
	}
	return ids, text.String()
}

func allEqualNonEmpty(values []string) bool {
	if len(values) == 0 {
		return false
	}
	first := strings.TrimSpace(values[0])
	if first == "" {
		return false
	}
	for _, value := range values[1:] {
		if strings.TrimSpace(value) != first {
			return false
		}
	}
	return true
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func usageUpdates(notifications []eventstream.SessionNotification) []eventstream.UsageUpdate {
	out := make([]eventstream.UsageUpdate, 0, 1)
	for _, notification := range notifications {
		if usage, ok := notification.Update.(eventstream.UsageUpdate); ok {
			out = append(out, usage)
		}
	}
	return out
}
