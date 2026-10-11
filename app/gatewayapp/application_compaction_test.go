package gatewayapp

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/application"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestApplicationCompactUsesProfileModelAndSharedCheckpointReplay(t *testing.T) {
	ctx := t.Context()
	provider := newGatewayAppCompactionOllamaServer(t)
	stack, err := newGatewayAppTestStack(t, Config{AppName: "test", UserID: "owner", StoreDir: filepath.Join(t.TempDir(), "store"), WorkspaceCWD: t.TempDir(), ContextWindow: 4096,
		Model: ModelConfig{Provider: "ollama", API: providers.APIOllama, Model: "compact-test", BaseURL: provider.URL, HTTPClient: provider.Client()}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := stack.Applications().Register(ctx, appserver.Principal{ID: "owner"}, application.Registration{OperationID: "enroll", Name: "compact", Credential: "app-client-" + strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: connection.PrincipalID, ApplicationID: connection.ApplicationID, ConnectionID: connection.ConnectionID}
	created, err := stack.Applications().Create(ctx, principal, appserver.CreateApplicationSessionRequest{WriteBase: appserver.WriteBase{OperationID: "create"}, Profile: application.Profile{
		Version: "test/1", Instructions: "Only explicit application instructions", Model: "ollama/compact-test", Execution: "tools-only", ToolsVersion: "tools/1"}})
	if err != nil {
		t.Fatal(err)
	}
	ref := session.SessionRef{SessionID: created.SessionID}
	for _, event := range []*session.Event{gatewayAppUserEvent("Project objective: application compaction preserves context."), gatewayAppAssistantEvent("ack"),
		gatewayAppUserEvent("Current blocker: bare compact truncates prompt-visible history."), gatewayAppAssistantEvent("ack"), gatewayAppUserEvent("Next action: structured checkpoint.")} {
		appendGatewayAppEvent(t, stack, ref, event)
	}
	active := mustCurrentSession(t, stack, ref.SessionID)
	base := appserver.WriteBase{OperationID: "compact", SessionID: ref.SessionID, ExpectedRevision: &active.Revision, ExpectedControllerEpoch: active.Controller.EpochID}
	result, err := stack.ControlClient().CompactSession(ctx, principal, appserver.CompactSessionRequest{WriteBase: base})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || provider.compactionCalls.Load() != 1 || provider.normalCalls.Load() != 0 {
		t.Fatalf("profile compaction: %+v, %v, summary calls=%d", result, err, provider.compactionCalls.Load())
	}
	loaded, err := stack.composition.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	event, present := latestGatewayAppCompactEvent(loaded.Events)
	data, valid := compact.CompactEventDataFromEvent(event)
	if !present || !valid || data.Trigger != "manual" || data.SourceEventCount == 0 {
		t.Fatalf("native checkpoint metadata: %+v", event)
	}
	before := compact.PromptEventsFromLatestCompact(loaded.Events)
	if len(before) == 0 || !strings.Contains(session.EventText(before[0]), "CONTEXT CHECKPOINT") {
		t.Fatalf("native rebuilt model context lost checkpoint: %+v", before)
	}
	loadedAgain, err := stack.composition.sessions.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	if after := compact.PromptEventsFromLatestCompact(loadedAgain.Events); len(after) != len(before) || session.EventText(after[0]) != session.EventText(before[0]) {
		t.Fatalf("checkpoint replay differs after durable reload: %+v / %+v", before, after)
	}
	base.OperationID = "stale-compact"
	if _, err := stack.ControlClient().CompactSession(ctx, principal, appserver.CompactSessionRequest{WriteBase: base}); err == nil {
		t.Fatal("stale compaction bypassed native Session CAS")
	}
	if provider.compactionCalls.Load() != 1 {
		t.Fatal("stale command dispatched another compaction request")
	}
}
