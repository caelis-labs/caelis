package runtime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPRevocationSnapshotGuardOrdersCheckpointAndRevocation(t *testing.T) {
	store, err := NewMCPGrantStore(filepath.Join(t.TempDir(), "grants.json"))
	if err != nil {
		t.Fatal(err)
	}
	active := session.Session{SessionRef: session.SessionRef{SessionID: "guard"}, CWD: t.TempDir(), CreatedAt: time.Now()}
	grant, err := mcpGrantFor(fixtureMCPDefinition("source"), active)
	if err != nil {
		t.Fatal(err)
	}
	revision := store.RevocationRevision()
	release, err := store.ValidateRevocation(t.Context(), revision)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if unlock, err := store.ValidateRevocation(t.Context(), revision); unlock != nil || !errors.Is(err, agent.ErrModelRequestSnapshotStale) {
		t.Fatalf("contended guard admitted: %v", err)
	}
	started, revoked := make(chan struct{}), make(chan error, 1)
	go func() { close(started); revoked <- store.Revoke(grant, "session", active.SessionID) }()
	<-started
	select {
	case err := <-revoked:
		t.Fatalf("revocation crossed commit guard: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	release()
	release()
	if err := <-revoked; err != nil {
		t.Fatal(err)
	}
	if unlock, err := store.ValidateRevocation(t.Context(), revision); unlock != nil || !errors.Is(err, agent.ErrModelRequestSnapshotStale) {
		t.Fatalf("revoked candidate admitted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if unlock, err := store.ValidateRevocation(ctx, store.RevocationRevision()); unlock != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled guard: %v", err)
	}
}

type mcpCheckpointStore struct {
	session.Service
	entered chan struct{}
	finish  chan struct{}
	fail    bool
}

func (s *mcpCheckpointStore) AppendEvent(ctx context.Context, req session.AppendEventRequest) (*session.Event, error) {
	if compact.IsCompactEvent(req.Event) {
		close(s.entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-s.finish:
		}
		if s.fail {
			return nil, errors.New("injected checkpoint failure")
		}
	}
	return s.Service.AppendEvent(ctx, req)
}

// Actual directory/grant owners guard the sole checkpoint writer with or without
// an embedding validator. It does not emulate async generation.
func TestMCPRetirementComposedGuardsCoverCanonicalCheckpoint(t *testing.T) {
	for _, tc := range []struct{ fail, parent bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprintf("failed_%t/parent_%t", tc.fail, tc.parent), func(t *testing.T) {
			server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "composition", Version: "1"}, nil)
			server.AddTool(&mcpsdk.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				return &mcpsdk.CallToolResult{}, nil
			})
			httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
			defer httpServer.Close()
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "p", Name: "local", Transport: mcp.TransportStreamableHTTP, URL: httpServer.URL}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			grants, err := NewMCPGrantStore(filepath.Join(t.TempDir(), "grants.json"))
			if err != nil {
				t.Fatal(err)
			}
			store, active := newTestSessionService(t, "guarded-retirement")
			binding := tool.BindingForDefinition(manager.Tools()[0].Definition())
			grant, err := mcpGrantFor(manager.Tools()[0].Definition(), active)
			if err != nil {
				t.Fatal(err)
			}
			prior := userTextEvent("canonical mark")
			prior.Meta = map[string]any{tool.MetadataToolExposure: tool.ExposureState{Version: 1, Active: []tool.Binding{binding}, Pending: []tool.Binding{binding}}}
			appendTestEvent(t, store, active.SessionRef, prior)
			source, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			last := source.Events[len(source.Events)-1]
			candidate := buildCompactEvent(source.Session, "generated, not adopted", compact.CompactEventData{
				ContractVersion: compact.CompactContractVersion, SummarizedThroughID: last.ID, SummarizedThroughSeq: last.Seq,
			})
			gate := &mcpCheckpointStore{Service: store, entered: make(chan struct{}), finish: make(chan struct{}), fail: tc.fail}
			defer func() {
				select {
				case <-gate.finish:
				default:
					close(gate.finish)
				}
			}()
			rt, err := New(Config{Sessions: gate, AgentFactory: chat.Factory{}, MCPGrants: grants})
			if err != nil {
				t.Fatal(err)
			}
			deferred := &deferredToolSource{source: manager}
			completed := make(chan error, 1)
			catalogRevision := deferred.CatalogRevision()
			parent := &modelRequestAuthority{catalog: deferred, refs: 1}
			if tc.parent {
				parent.validate = func(context.Context) (func(), error) { return func() {}, nil }
			}
			authority := rt.captureRequestAuthority(parent)
			defer authority.drop()
			defer parent.drop()
			checkpointCtx := context.WithValue(t.Context(), checkpointValidationKey{}, authority.validate)
			go func() {
				_, err := rt.persistCompactionArtifacts(checkpointCtx, &source.Session, source, "", compact.Result{Compacted: true, CompactEvent: candidate})
				completed <- err
			}()
			select {
			case <-gate.entered:
			case err := <-completed:
				t.Fatalf("guarded writer did not start: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("guarded writer timed out")
			}
			// Before append confirmation, even a finished summary leaves exposure.
			before, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			current, _ := exposureFromEvents(before.Events)
			if len(current.Active) != 1 {
				t.Fatal("generation or append entry retired tools")
			}
			updated := make(chan error, 1)
			go func() { updated <- manager.RevokeServer("p", "local") }()
			select {
			case err := <-updated:
				t.Fatalf("catalog update crossed actual checkpoint write: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			revoked := make(chan error, 1)
			go func() { revoked <- grants.Revoke(grant, "session", active.SessionID) }()
			select {
			case err := <-revoked:
				t.Fatalf("grant revocation crossed actual checkpoint write: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(gate.finish)
			if err := <-completed; (err != nil) != tc.fail {
				t.Fatalf("checkpoint fail=%v: %v", tc.fail, err)
			}
			if err := <-updated; err != nil {
				t.Fatal(err)
			}
			if err := <-revoked; err != nil {
				t.Fatal(err)
			}
			after, err := store.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			current, _ = exposureFromEvents(after.Events)
			want := 0
			if tc.fail {
				want = 1
			}
			if len(current.Active) != want {
				t.Fatalf("canonical exposure fail=%v: %+v", tc.fail, current)
			}
			if unlock, err := deferred.ValidateCatalog(t.Context(), catalogRevision); unlock != nil || err == nil {
				t.Fatalf("post-commit revocation did not invalidate candidate: %v", err)
			}
		})
	}
}
