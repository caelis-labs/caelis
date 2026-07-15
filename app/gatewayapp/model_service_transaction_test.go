package gatewayapp

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/ports/gateway"
)

func TestConnectRollsBackLookupRuntimeAndStoreWhenAgentRefreshFails(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	originalID := stack.lookup.DefaultID()
	originalRuntimeModel := stack.runtime.Model.ID
	wantErr := errors.New("refresh failed")
	stack.refreshConfiguredAgentsHook = func() error { return wantErr }

	_, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "new-model",
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Connect() error = %v, want %v", err, wantErr)
	}
	if got := stack.lookup.DefaultID(); got != originalID {
		t.Fatalf("lookup default = %q, want rollback to %q", got, originalID)
	}
	if got := stack.runtime.Model.ID; got != originalRuntimeModel {
		t.Fatalf("runtime model = %q, want rollback to %q", got, originalRuntimeModel)
	}
	doc, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatalf("Load() error = %v", loadErr)
	}
	if doc.Models.DefaultID != "" && doc.Models.DefaultID != originalID {
		t.Fatalf("stored default = %q, want rollback to %q", doc.Models.DefaultID, originalID)
	}
	for _, cfg := range doc.Models.Configs {
		if strings.Contains(cfg.ID, "new-model") {
			t.Fatalf("stored configs contain failed model %#v", cfg)
		}
	}
}

func TestConnectModelsPersistsBatchAtomicallyAndKeepsFirstDefault(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	modelIDs, err := stack.ConnectModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "batch-first"},
		{Provider: "ollama", API: providers.APIOllama, Model: "batch-second"},
	})
	if err != nil {
		t.Fatalf("ConnectModels() error = %v", err)
	}
	if len(modelIDs) != 2 || stack.lookup.DefaultID() != modelIDs[0] || stack.runtime.Model.ID != modelIDs[0] {
		t.Fatalf("batch ids/default/runtime = %#v/%q/%q", modelIDs, stack.lookup.DefaultID(), stack.runtime.Model.ID)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	found := map[string]bool{}
	for _, cfg := range doc.Models.Configs {
		found[cfg.Model] = true
	}
	if !found["batch-first"] || !found["batch-second"] || doc.Models.DefaultID != modelIDs[0] {
		t.Fatalf("persisted batch = models:%#v default:%q", found, doc.Models.DefaultID)
	}

	before := stack.lookup.Snapshot()
	_, err = stack.ConnectModels([]ModelConfig{
		{Provider: "ollama", API: providers.APIOllama, Model: "should-rollback"},
		{Model: "invalid-without-provider"},
	})
	if err == nil {
		t.Fatal("ConnectModels(invalid batch) error = nil")
	}
	after := stack.lookup.Snapshot()
	if !reflect.DeepEqual(after, before) || stack.lookup.HasAlias("ollama/should-rollback") {
		t.Fatalf("invalid batch leaked into lookup: before=%#v after=%#v", before, after)
	}
}

func TestUseModelRollsBackConfigWhenSessionStateUpdateFails(t *testing.T) {
	ctx := context.Background()
	stack, activeSession := newLocalStateTestStack(t)
	originalID := stack.lookup.DefaultID()
	nextID, err := stack.Connect(ModelConfig{
		Provider: "ollama",
		API:      providers.APIOllama,
		Model:    "next-model",
	})
	if err != nil {
		t.Fatalf("Connect(next) error = %v", err)
	}
	if err := stack.UseModel(ctx, activeSession.SessionRef, originalID); err != nil {
		t.Fatalf("UseModel(original) error = %v", err)
	}
	wantErr := errors.New("state update failed")
	stack.Sessions = &failingUpdateSessionService{Service: stack.Sessions, err: wantErr}

	err = stack.UseModel(ctx, activeSession.SessionRef, nextID)
	if !errors.Is(err, wantErr) {
		t.Fatalf("UseModel() error = %v, want %v", err, wantErr)
	}
	if got := stack.lookup.DefaultID(); got != originalID {
		t.Fatalf("lookup default = %q, want rollback to %q", got, originalID)
	}
	state, stateErr := stack.Sessions.SnapshotState(ctx, activeSession.SessionRef)
	if stateErr != nil {
		t.Fatalf("SnapshotState() error = %v", stateErr)
	}
	if got, _ := state[gateway.StateCurrentModelAlias].(string); got != originalID {
		t.Fatalf("session model state = %q, want %q", got, originalID)
	}
}

type failingUpdateSessionService struct {
	session.Service
	err error
}

func (s *failingUpdateSessionService) UpdateState(context.Context, session.UpdateStateRequest) (session.Session, error) {
	return session.Session{}, s.err
}
