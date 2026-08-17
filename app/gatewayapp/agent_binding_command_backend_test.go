package gatewayapp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

func TestAgentBindingCommandsUseHostCASAndSharedLedger(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	principal := appserver.Principal{ID: stack.composition.userID}
	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", Model: "binding-command"})
	if err != nil {
		t.Fatal(err)
	}
	beforeSession, err := stack.composition.sessions.Session(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	revision, err := stack.ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}

	bindRequest := appserver.BindAgentBindingRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-binding-bind", ExpectedRevision: &revision},
		Binding: agentbinding.Binding{
			Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort,
		},
	}
	bound, err := stack.AgentCommands().BindAgentBinding(ctx, principal, bindRequest)
	requireCommittedAgentBindingCommand(t, bound, err, revision+1)
	replayed, err := stack.AgentCommands().BindAgentBinding(ctx, principal, bindRequest)
	if err != nil || replayed != bound {
		t.Fatalf("BindAgentBinding(replay) = %#v, %v; want %#v", replayed, err, bound)
	}
	changed := bindRequest
	changed.Binding.ProfileID = "provider:changed"
	conflicted, err := stack.AgentCommands().BindAgentBinding(ctx, principal, changed)
	if !errors.Is(err, appserver.ErrOperationConflict) || conflicted.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("BindAgentBinding(changed payload) = %#v, %v", conflicted, err)
	}

	current := bound.Revision
	savedSet, err := stack.AgentCommands().SaveAgentBindingSet(ctx, principal, appserver.AgentBindingSetRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-binding-set-save", ExpectedRevision: &current},
		SetName:   "baseline",
	})
	requireCommittedAgentBindingCommand(t, savedSet, err, current+1)
	current = savedSet.Revision
	reset, err := stack.AgentCommands().ResetAgentBinding(ctx, principal, appserver.ResetAgentBindingRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-binding-reset", ExpectedRevision: &current},
		Handle:    agentbinding.HandleOrbit,
	})
	requireCommittedAgentBindingCommand(t, reset, err, current+1)
	current = reset.Revision
	applied, err := stack.AgentCommands().ApplyAgentBindingSet(ctx, principal, appserver.AgentBindingSetRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-binding-set-apply", ExpectedRevision: &current},
		SetName:   "baseline",
	})
	requireCommittedAgentBindingCommand(t, applied, err, current+1)
	current = applied.Revision
	created, err := stack.AgentCommands().CreateAgentRole(ctx, principal, appserver.CreateAgentRoleRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-role-create", ExpectedRevision: &current},
		Role:      agentbinding.Role{Handle: "research", Description: "Investigate unfamiliar systems."},
		Binding:   agentbinding.Binding{ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort},
	})
	requireCommittedAgentBindingCommand(t, created, err, current+1)
	current = created.Revision
	deletedRole, err := stack.AgentCommands().DeleteAgentRole(ctx, principal, appserver.DeleteAgentRoleRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-role-delete", ExpectedRevision: &current},
		Handle:    "research",
	})
	requireCommittedAgentBindingCommand(t, deletedRole, err, current+1)
	current = deletedRole.Revision
	deletedSet, err := stack.AgentCommands().DeleteAgentBindingSet(ctx, principal, appserver.AgentBindingSetRequest{
		WriteBase: appserver.WriteBase{OperationID: "agent-binding-set-delete", ExpectedRevision: &current},
		SetName:   "baseline",
	})
	requireCommittedAgentBindingCommand(t, deletedSet, err, current+1)

	sharedConflict, err := stack.ConfigurationCommands().UseModel(ctx, principal, appserver.UseModelRequest{
		WriteBase: appserver.WriteBase{OperationID: bindRequest.OperationID, ExpectedRevision: &deletedSet.Revision},
		Model:     stack.composition.lookup.DefaultID(),
	})
	if !errors.Is(err, appserver.ErrOperationConflict) || sharedConflict.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("UseModel(shared Agent operation ID) = %#v, %v", sharedConflict, err)
	}
	afterSession, err := stack.composition.sessions.Session(ctx, activeSession.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	if afterSession.Revision != beforeSession.Revision {
		t.Fatalf("Host Agent binding commands changed Session revision: before=%d after=%d", beforeSession.Revision, afterSession.Revision)
	}
}

func TestAgentBindingCASAllowsOnlyOneConcurrentHostWriter(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seed := newAppConfigStore(root)
	if err := seed.Save(AppConfig{}); err != nil {
		t.Fatal(err)
	}
	doc, err := seed.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	expected := doc.ConfigurationRevision

	ready := make(chan struct{}, 1)
	release := make(chan struct{})
	var saveCalls atomic.Int32
	makeStore := func() *appConfigStore {
		store := newAppConfigStore(root)
		store.saveHook = func(AppConfig) error {
			saveCalls.Add(1)
			ready <- struct{}{}
			<-release
			return nil
		}
		return store
	}
	first := &Stack{composition: runtimeComposition{store: makeStore()}}
	second := &Stack{composition: runtimeComposition{store: makeStore()}}
	type outcome struct {
		result agentBindingMutationResult
		err    error
	}
	results := make(chan outcome, 2)
	var start sync.WaitGroup
	start.Add(2)
	mutate := func(stack *Stack, setName string) {
		defer start.Done()
		result, mutationErr := stack.mutateAgentBindingsAtRevision(ctx, appserver.ActionAgentBindingSetSave, appserver.AgentBindingSetRequest{
			WriteBase: appserver.WriteBase{ExpectedRevision: &expected},
			SetName:   setName,
		})
		results <- outcome{result: result, err: mutationErr}
	}
	go mutate(first, "first")
	go mutate(second, "second")
	<-ready
	close(release)
	start.Wait()
	close(results)

	committed := 0
	conflicts := 0
	for item := range results {
		switch {
		case item.err == nil:
			committed++
			if item.result.Revision != expected+1 {
				t.Fatalf("committed revision = %d, want %d", item.result.Revision, expected+1)
			}
		case errors.Is(item.err, configstore.ErrConfigurationRevisionConflict):
			conflicts++
			if item.result.Revision != expected+1 {
				t.Fatalf("conflict actual revision = %d, want %d", item.result.Revision, expected+1)
			}
		default:
			t.Fatalf("mutation error = %v", item.err)
		}
	}
	attempts := saveCalls.Load()
	if committed != 1 || conflicts != 1 || attempts < 1 || attempts > 2 {
		t.Fatalf("committed=%d conflicts=%d CAS attempts=%d, want 1/1/(1 or 2)", committed, conflicts, attempts)
	}
	loaded, err := seed.LoadContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConfigurationRevision != expected+1 || len(loaded.AgentBindings.Sets) != 1 {
		t.Fatalf("canonical Agent bindings = %#v at revision %d", loaded.AgentBindings, loaded.ConfigurationRevision)
	}
}

func requireCommittedAgentBindingCommand(t *testing.T, result appserver.CommandResult, err error, revision uint64) {
	t.Helper()
	if err != nil || result.Outcome != appserver.OutcomeCommitted || result.Revision != revision {
		t.Fatalf("Agent binding command = %#v, %v; want committed revision %d", result, err, revision)
	}
}
