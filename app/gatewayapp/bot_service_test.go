package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/bot"
	"github.com/caelis-labs/caelis/control/memorybinding"
)

func createTestBot(t *testing.T, stack *Stack, operation, name string) bot.Bot {
	t.Helper()
	principal := appserver.Principal{ID: "local-user"}
	result, err := stack.Bots().CreateBot(context.Background(), principal, appserver.CreateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: operation}, Config: bot.Config{Name: name},
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Resource == nil {
		t.Fatalf("create Bot: %+v, %v", result, err)
	}
	value, err := stack.Bots().GetBot(context.Background(), principal, result.Resource.Ref)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestBotIdentityIsolationPersistenceAndLifecycle(t *testing.T) {
	ctx := context.Background()
	cfg := Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir(), WorkspaceKey: "work", SkillDirs: []string{}, Sandbox: SandboxConfig{RequestedType: "host"}}
	stack, err := NewLocalStack(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stack.Close() })
	principal := appserver.Principal{ID: "local-user"}
	first := createTestBot(t, stack, "create-first", "First")
	second := createTestBot(t, stack, "create-second", "Second")
	if first.ID == second.ID || first.SessionID == second.SessionID || first.ID == first.SessionID {
		t.Fatalf("identities not independent: %+v / %+v", first, second)
	}
	for _, value := range []bot.Bot{first, second} {
		active, err := stack.Sessions().Session(ctx, session.SessionRef{SessionID: value.SessionID})
		if err != nil {
			t.Fatal(err)
		}
		if active.CWD == cfg.WorkspaceCWD || active.WorkspaceKey != value.ID {
			t.Fatalf("Bot inherited launching workspace: %+v", active)
		}
		state, err := stack.Sessions().SnapshotState(ctx, active.SessionRef)
		if err != nil {
			t.Fatal(err)
		}
		if _, bound := state[memorybinding.SessionStateKey]; bound {
			t.Fatal("Bot acquired Workspace Memory")
		}
	}
	updatedConfig := first.Config
	updatedConfig.Name, updatedConfig.Description = "Renamed", "Use concise answers."
	result, err := stack.Bots().UpdateBot(ctx, principal, appserver.UpdateBotRequest{
		WriteBase: appserver.WriteBase{OperationID: "edit-first", SessionID: first.SessionID, ExpectedRevision: &first.Revision}, BotID: first.ID, Config: updatedConfig,
	})
	if err != nil || result.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("save: %+v, %v", result, err)
	}
	current, err := stack.Bots().GetBot(ctx, principal, first.ID)
	if err != nil || current.ID != first.ID || current.SessionID != first.SessionID || current.Config != updatedConfig {
		t.Fatalf("rename changed identity or config: %+v, %v", current, err)
	}
	other, _ := stack.Bots().GetBot(ctx, principal, second.ID)
	if other != second {
		t.Fatalf("config crossed Bots: %+v", other)
	}
	_, err = stack.Bots().GetBot(ctx, appserver.Principal{ID: "another-user"}, first.ID)
	if !errors.Is(err, appserver.ErrUnauthorized) {
		t.Fatalf("foreign Bot read: %v", err)
	}
	foreign, err := stack.Bots().ListBots(ctx, appserver.Principal{ID: "another-user"})
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign Bot discovery: %+v, %v", foreign, err)
	}
	listed, err := stack.ControlClient().ListSessions(ctx, principal, appserver.ListSessionsRequest{Limit: 1})
	if err != nil || len(listed.Sessions) != 0 {
		t.Fatalf("Bot leaked into ordinary Session list: %+v, %v", listed, err)
	}
	closed, err := stack.ControlClient().CloseSession(ctx, principal, appserver.CloseSessionRequest{WriteBase: appserver.WriteBase{OperationID: "close-bot", SessionID: first.SessionID}})
	if !errors.Is(err, appserver.ErrUnauthorized) || closed.Outcome != appserver.OutcomeRejected {
		t.Fatalf("ordinary close changed Bot lifecycle: %+v, %v", closed, err)
	}
	_, err = stack.ConfigurationCommands().UseSessionModel(ctx, principal, appserver.SessionModelRequest{WriteBase: appserver.WriteBase{OperationID: "generic-model", SessionID: first.SessionID, ExpectedRevision: &current.Revision}, Model: "anything"})
	if !errors.Is(err, appserver.ErrUnauthorized) {
		t.Fatalf("generic model configuration changed Bot: %v", err)
	}
	// Missing model must not destroy identity or silently choose a work Agent.
	failed, err := stack.ControlClient().Prompt(ctx, principal, appserver.PromptRequest{WriteBase: appserver.WriteBase{OperationID: "unconfigured-prompt", SessionID: first.SessionID}, Input: "hello"})
	if err == nil || failed.Outcome == appserver.OutcomeCommitted {
		t.Fatalf("unconfigured Bot pretended to chat: %+v, %v", failed, err)
	}
	before, err := stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: first.SessionID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Events) != 3 {
		t.Fatalf("config or rejected prompt duplicated history: %+v", before.Events)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	// A different startup CWD cannot change Bot identity or its conversation.
	cfg.WorkspaceCWD, cfg.WorkspaceKey = t.TempDir(), "different-work"
	stack, err = NewLocalStack(cfg)
	if err != nil {
		t.Fatal(err)
	}
	after, err := stack.Sessions().LoadSession(ctx, session.LoadSessionRequest{SessionRef: session.SessionRef{SessionID: first.SessionID}})
	if err != nil || !reflect.DeepEqual(before.Events, after.Events) || !reflect.DeepEqual(before.State, after.State) {
		t.Fatalf("restart changed canonical history/state: %v", err)
	}
	restored, err := stack.Bots().GetBot(ctx, principal, first.ID)
	if err != nil || restored.Config != updatedConfig || restored.SessionID != first.SessionID {
		t.Fatalf("restart lost Bot: %+v, %v", restored, err)
	}
}

func TestBotConcurrentCreateAndConfigurationCAS(t *testing.T) {
	ctx := context.Background()
	stack, err := newGatewayAppTestStack(t, Config{StoreDir: t.TempDir(), WorkspaceCWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	principal := appserver.Principal{ID: "local-user"}
	request := appserver.CreateBotRequest{WriteBase: appserver.WriteBase{OperationID: "same-create"}, Config: bot.Config{Name: "One"}}
	var wg sync.WaitGroup
	results := make(chan appserver.CommandResult, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := stack.Bots().CreateBot(ctx, principal, request)
			if err != nil {
				t.Errorf("concurrent create: %v", err)
			}
			results <- result
		}()
	}
	wg.Wait()
	close(results)
	var first appserver.CommandResult
	for result := range results {
		if first.SessionID == "" {
			first = result
		}
		if !reflect.DeepEqual(first, result) {
			t.Fatalf("creation retry changed result: %+v / %+v", first, result)
		}
	}
	list, err := stack.Bots().ListBots(ctx, principal)
	if err != nil || len(list) != 1 {
		t.Fatalf("duplicate Bots: %+v, %v", list, err)
	}
	value := list[0]
	outcomes := make(chan appserver.Outcome, 2)
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			config := value.Config
			config.Name = fmt.Sprintf("Editor %d", i)
			result, _ := stack.Bots().UpdateBot(ctx, principal, appserver.UpdateBotRequest{WriteBase: appserver.WriteBase{OperationID: fmt.Sprintf("edit-%d", i), SessionID: value.SessionID, ExpectedRevision: &value.Revision}, BotID: value.ID, Config: config})
			outcomes <- result.Outcome
		}()
	}
	wg.Wait()
	close(outcomes)
	counts := map[appserver.Outcome]int{}
	for outcome := range outcomes {
		counts[outcome]++
	}
	if counts[appserver.OutcomeCommitted] != 1 || counts[appserver.OutcomeConflicted] != 1 {
		t.Fatalf("lost configuration update: %+v", counts)
	}
	request.Config.Name = "Changed payload"
	result, err := stack.Bots().CreateBot(ctx, principal, request)
	if !errors.Is(err, appserver.ErrOperationConflict) || result.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("operation payload conflict: %+v, %v", result, err)
	}
}
