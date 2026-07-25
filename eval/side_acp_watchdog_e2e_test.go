//go:build e2e

package eval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlassembly "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestSideACPDistinctXSearchBypassesOrchestrationWatchdogE2E(t *testing.T) {
	repo := repoRootForGatewayAppTest(t)
	root := t.TempDir()
	workdir := t.TempDir()
	interruptionProbe := filepath.Join(t.TempDir(), "external-acp-interrupted")
	launcher := writeDelayedXSearchLauncher(t, repo, interruptionProbe)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	stack, err := gatewayapp.NewLocalStack(gatewayapp.Config{
		AppName:      "caelis",
		UserID:       "side-agent-watchdog-user",
		StoreDir:     root,
		WorkspaceKey: workdir,
		WorkspaceCWD: workdir,
		ApprovalMode: "auto-review",
		Model:        gatewayapp.ModelConfig{Provider: "minimax", Model: "MiniMax-M2"},
		Assembly:     controlassembly.ResolvedAssembly{},
	})
	if err != nil {
		t.Fatalf("gatewayapp.NewLocalStack() error = %v", err)
	}
	t.Cleanup(func() {
		_ = stack.Close()
	})

	connectReq := controlagents.ConnectRequest{
		AdapterID:   "custom",
		Launcher:    controlagents.LauncherChoiceCommand,
		CommandLine: launcher,
		CWD:         workdir,
	}
	discovery, err := stack.DiscoverACPConnection(ctx, connectReq)
	if err != nil {
		t.Fatalf("DiscoverACPConnection() error = %v", err)
	}
	connectReq.ModelID = "opus"
	connectReq.ConfigValues = map[string]string{"effort": "max"}
	connectReq.Discovery = &discovery
	connected, err := stack.ConnectACP(ctx, connectReq)
	if err != nil {
		t.Fatalf("ConnectACP() error = %v", err)
	}
	if len(connected.Profiles) != 1 {
		t.Fatalf("ConnectACP() profiles = %#v, want one", connected.Profiles)
	}
	if _, err := stack.AgentBindings().BindAgentBinding(ctx, agentbinding.Binding{
		Handle:    agentbinding.HandleZenith,
		ProfileID: connected.Profiles[0].ID,
		Effort:    "max",
	}); err != nil {
		t.Fatalf("BindAgentBinding(zenith) error = %v", err)
	}

	active, err := stack.StartSession(ctx, "side-acp-watchdog", "side-acp-watchdog-e2e")
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	driver, err := local.NewLocalAdapterForSession(ctx, stack, active, "side-acp-watchdog-e2e", "")
	if err != nil {
		t.Fatalf("NewLocalAdapterForSession() error = %v", err)
	}
	result, err := controlprompt.New(controlprompt.RouterConfig{Service: driver}).Route(ctx, controlprompt.Request{
		Submission: control.Submission{Text: "/zenith run six distinct X searches"},
	})
	if err != nil {
		t.Fatalf("Route(/zenith) error = %v", err)
	}
	if result.Turn == nil {
		t.Fatal("Route(/zenith) returned nil Turn")
	}
	turnID := strings.TrimSpace(result.Turn.TurnID())
	if turnID == "" {
		t.Fatal("Route(/zenith) returned an empty TurnID")
	}

	startIDs := make([]string, 0, 6)
	completedQueries := make([]string, 0, 6)
	finalMessages := 0
	finalMaterializations := 0
	completedTurn := false
	for envelope := range result.Turn.Events() {
		if envelope.Err != nil {
			t.Fatalf("Side ACP envelope error = %v", envelope.Err)
		}
		if envelope.Kind == eventstream.KindError {
			t.Fatalf("Side ACP error envelope = %q", envelope.Error)
		}
		if eventstream.IsTurnTerminalLifecycle(envelope) && strings.TrimSpace(envelope.TurnID) == turnID {
			if envelope.Lifecycle.State != eventstream.LifecycleStateCompleted {
				t.Fatalf("Side ACP terminal lifecycle = %#v, want completed", envelope.Lifecycle)
			}
			completedTurn = true
		}
		switch update := envelope.Update.(type) {
		case schema.ToolCall:
			if update.SessionUpdate != schema.UpdateToolCall ||
				!strings.HasPrefix(update.ToolCallID, "x-search-") {
				continue
			}
			if update.Title != "X search:" || update.Kind != schema.ToolKindSearch ||
				update.Status != schema.ToolStatusInProgress {
				t.Fatalf("XSearch start = %#v", update)
			}
			rawInput, ok := update.RawInput.(map[string]any)
			if !ok || len(rawInput) != 2 || rawInput["variant"] != "XSearch" || rawInput["backend"] != true {
				t.Fatalf("XSearch %q rawInput = %#v, want fixed XSearch marker", update.ToolCallID, update.RawInput)
			}
			startIDs = append(startIDs, update.ToolCallID)
		case schema.ToolCallUpdate:
			if update.SessionUpdate != schema.UpdateToolCallInfo ||
				!strings.HasPrefix(update.ToolCallID, "x-search-") {
				continue
			}
			if update.Status == nil || *update.Status != schema.ToolStatusCompleted {
				t.Fatalf("XSearch completion = %#v", update)
			}
			query := delayedXSearchQuery(t, update.RawOutput)
			displayQuery := metautil.String(
				update.Meta,
				metautil.Root,
				metautil.Display,
				metautil.DisplayToolInput,
				"query",
			)
			if displayQuery != query {
				t.Fatalf("XSearch %q display query = %q, want %q", update.ToolCallID, displayQuery, query)
			}
			completedQueries = append(completedQueries, query)
		case schema.ContentChunk:
			if update.SessionUpdate != schema.UpdateAgentMessage ||
				strings.TrimSpace(schema.ExtractTextValue(update.Content)) != "external xsearch sequence complete" {
				continue
			}
			finalMessages++
			if envelope.Final {
				finalMaterializations++
			}
		}
	}
	if err := result.Turn.Close(); err != nil {
		t.Fatalf("Turn.Close() error = %v", err)
	}

	wantIDs := make([]string, 0, 6)
	wantQueries := make([]string, 0, 6)
	for index := 1; index <= 6; index++ {
		wantIDs = append(wantIDs, fmt.Sprintf("x-search-%d", index))
		wantQueries = append(wantQueries, fmt.Sprintf("CAELIS_ACP_XSEARCH_QUERY_%d", index))
	}
	if got := strings.Join(startIDs, ","); got != strings.Join(wantIDs, ",") {
		t.Fatalf("XSearch starts = %q, want %q", got, strings.Join(wantIDs, ","))
	}
	if got := strings.Join(completedQueries, ","); got != strings.Join(wantQueries, ",") {
		t.Fatalf("XSearch completed queries = %q, want %q", got, strings.Join(wantQueries, ","))
	}
	if finalMessages == 0 || finalMaterializations != 1 {
		t.Fatalf("final assistant messages = %d materialized = %d, want final output and one canonical materialization", finalMessages, finalMaterializations)
	}
	if !completedTurn {
		t.Fatal("Side ACP Turn did not emit a completed terminal lifecycle")
	}
	if content, err := os.ReadFile(interruptionProbe); err == nil {
		t.Fatalf("external ACP Prompt was cancelled or interrupted: %s", strings.TrimSpace(string(content)))
	} else if !os.IsNotExist(err) {
		t.Fatalf("ReadFile(interruption probe) error = %v", err)
	}

	loaded, err := stack.Sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	for _, event := range loaded.Events {
		if event == nil {
			continue
		}
		if event.Actor.Name == "agent-watchdog" || event.Actor.Name == "control-watchdog" {
			t.Fatalf("parent Session contains watchdog actor event: %#v", event)
		}
		if event.Lifecycle != nil {
			switch event.Lifecycle.Status {
			case "agent_loop_watchdog_checkpoint", "loop_watchdog_checkpoint":
				t.Fatalf("parent Session contains watchdog checkpoint: %#v", event)
			case eventstream.LifecycleStateInterrupted, eventstream.LifecycleStateCancelled, "canceled":
				t.Fatalf("parent Session contains interrupted/cancelled lifecycle: %#v", event)
			}
		}
	}
}

func delayedXSearchQuery(t *testing.T, rawOutput any) string {
	t.Helper()
	output, ok := rawOutput.(map[string]any)
	if !ok || output["name"] != "x_keyword_search" {
		t.Fatalf("XSearch rawOutput = %#v", rawOutput)
	}
	serialized, ok := output["input"].(string)
	if !ok {
		t.Fatalf("XSearch rawOutput input = %#v, want serialized JSON", output["input"])
	}
	var input map[string]any
	if err := json.Unmarshal([]byte(serialized), &input); err != nil {
		t.Fatalf("Unmarshal(XSearch rawOutput input) error = %v", err)
	}
	query, _ := input["query"].(string)
	if query == "" || input["limit"] != "3" || input["mode"] != "Latest" {
		t.Fatalf("XSearch decoded input = %#v", input)
	}
	return query
}

func writeDelayedXSearchLauncher(t *testing.T, repo string, interruptionProbe string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caelis-acp-delayed-xsearch")
	script := fmt.Sprintf(`#!/bin/sh
export SDK_ACP_WIRE_MODE=delayed_xsearch
export SDK_ACP_WATCHDOG_PROBE_PATH=%s
cd %s
exec go run ./internal/acpe2eagent
`, shellQuote(interruptionProbe), shellQuote(repo))
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("WriteFile(delayed XSearch launcher) error = %v", err)
	}
	return path
}
