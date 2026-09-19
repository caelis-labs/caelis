package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/collaboration"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/client"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/subagent"
	"github.com/caelis-labs/caelis/internal/acptest/jsonrpc"
)

type reconnectCollaborationBackend struct {
	sessions session.Service
	ref      session.SessionRef
}

func (b reconnectCollaborationBackend) List(ctx context.Context, _ string) ([]collaboration.Thread, error) {
	active, err := b.sessions.Session(ctx, b.ref)
	if err != nil {
		return nil, err
	}
	return []collaboration.Thread{{Handle: "parent", ID: active.Controller.EpochID, SessionID: active.Controller.RemoteSessionID}}, nil
}

func (reconnectCollaborationBackend) Deliver(context.Context, string, []collaboration.Message) error {
	return nil
}

type renewingControllerGrant struct{ *collaboration.Grant }

func (renewingControllerGrant) Valid() bool { return false }

func TestControllerReconnectPublishesBindingBeforeCollaboration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		renew      bool
		fresh      bool
		failCommit bool
	}{
		{name: "renew_fresh", renew: true, fresh: true},
		{name: "retry_fresh", fresh: true},
		{name: "renew_same", renew: true},
		{name: "retry_same"},
		{name: "renew_commit_failure", renew: true, fresh: true, failCommit: true},
		{name: "retry_commit_failure", fresh: true, failCommit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			sessions := sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})
			active, err := sessions.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "test", PreferredSessionID: "reconnect"})
			if err != nil {
				t.Fatal(err)
			}
			service, err := collaboration.Open(filepath.Join(t.TempDir(), "collaboration.sqlite"), reconnectCollaborationBackend{sessions: sessions, ref: active.SessionRef})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Close() })
			registry, err := subagent.NewRegistry([]subagent.AgentConfig{{Name: "helper", Command: "helper-acp"}})
			if err != nil {
				t.Fatal(err)
			}
			manager, err := NewManager(Config{Registry: registry, Collaboration: func(_ context.Context, ref session.SessionRef, binding session.ControllerBinding, cfg subagent.AgentConfig) (subagent.AgentConfig, error) {
				cfg.MCPGrant = service.Prepare(collaboration.Identity{Session: ref.SessionID, Member: "parent"}, binding.EpochID)
				return cfg, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = manager.Quiesce(context.Background()) })
			type observation struct {
				prompt  client.PromptRequest
				binding session.ControllerBinding
				err     error
			}
			seen := make(chan observation, 1)
			finishPrompt := make(chan struct{})
			defer close(finishPrompt)
			starts := 0
			remote := "remote-old"
			if tc.fresh {
				remote = "remote-fresh"
			}
			manager.startClient = func(_ context.Context, _ string, cfg subagent.AgentConfig, resume string, _ func(client.UpdateEnvelope), _ func(context.Context, client.RequestPermissionRequest) (client.RequestPermissionResponse, error)) (*client.Client, string, controllerClientState, error) {
				starts++
				id := "remote-old"
				if starts > 1 {
					id = remote
					if resume != "remote-old" {
						return nil, "", controllerClientState{}, errors.New("reconnect did not resume the old remote")
					}
				}
				grant := cfg.MCPGrant.(*collaboration.Grant)
				if err := grant.Bind(id); err != nil {
					return nil, "", controllerClientState{}, err
				}
				local, peerSide := net.Pipe()
				clientConfig := client.Config{}
				if starts > 1 {
					clientConfig.MCPGrant = grant
				} else if tc.renew {
					clientConfig.MCPGrant = renewingControllerGrant{grant}
				}
				connection, err := client.NewStreamClient(local, local, clientConfig)
				if err != nil {
					_ = local.Close()
					_ = peerSide.Close()
					return nil, "", controllerClientState{}, err
				}
				t.Cleanup(func() { _ = connection.Close(context.Background()); _ = peerSide.Close() })
				if starts == 1 {
					if !tc.renew {
						// Keep the grant live to exercise the proven-unsent prompt
						// retry independently of the readiness renewal path.
						_ = connection.Close(context.Background())
					}
					return connection, id, controllerClientState{}, nil
				}
				peer := jsonrpc.New(peerSide, peerSide)
				peerDone := make(chan error, 1)
				go func() {
					peerDone <- peer.Serve(ctx, func(callCtx context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
						if msg.Method != client.MethodSessionPrompt {
							return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
						}
						var prompt client.PromptRequest
						decodeErr := json.Unmarshal(msg.Params, &prompt)
						current, readErr := sessions.Session(callCtx, active.SessionRef)
						_, authErr := service.CallAuthenticated(callCtx, grant.Token(), collaboration.Request{Tool: "ReadMessages", Arguments: json.RawMessage(`{}`)})
						seen <- observation{prompt: prompt, binding: current.Controller, err: errors.Join(decodeErr, readErr, authErr)}
						select {
						case <-finishPrompt:
						case <-callCtx.Done():
						}
						return client.PromptResponse{StopReason: string(acpsdk.StopReasonEndTurn)}, nil
					}, nil)
				}()
				t.Cleanup(func() {
					_ = peer.Close()
					select {
					case <-peerDone:
					case <-time.After(time.Second):
						t.Error("controller test peer did not stop")
					}
				})
				return connection, id, controllerClientState{}, nil
			}
			active.Controller = session.ControllerBinding{Kind: session.ControllerKindACP, AgentName: "helper", RemoteSessionID: "remote-old", ContextSyncSeq: 2}
			binding, err := manager.Activate(ctx, controller.HandoffRequest{SessionRef: active.SessionRef, Session: active, Agent: "helper"})
			if err != nil {
				t.Fatal(err)
			}
			active, err = sessions.BindController(ctx, session.BindControllerRequest{SessionRef: active.SessionRef, Binding: binding})
			if err != nil {
				t.Fatal(err)
			}
			fence, err := sessions.AcquireSessionFence(ctx, session.AcquireSessionFenceRequest{SessionRef: active.SessionRef, OwnerID: "test-runtime"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = sessions.ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(fence))
			}()
			ctx = session.ContextWithRuntimeFence(ctx, fence)
			commitErr := errors.New("binding persistence failed")
			failCommit := tc.failCommit
			req := controller.TurnRequest{SessionRef: active.SessionRef, Session: active, TurnID: "turn", Input: "continue", Context: agent.ContextTransfer{Summary: "DELTA-CONTEXT"}, FreshContext: agent.ContextTransfer{Summary: "FULL-CONTEXT"}, ContextSyncSeq: 4,
				CommitBinding: func(commitCtx context.Context) error {
					live, _, err := manager.ActiveControllerBinding(commitCtx, active.SessionRef)
					if err != nil {
						return err
					}
					if failCommit && live.RemoteSessionID == "remote-fresh" {
						return commitErr
					}
					_, err = sessions.BindController(commitCtx, session.BindControllerRequest{SessionRef: active.SessionRef, Binding: live, MutationGuard: session.RuntimeMutationGuard(commitCtx)})
					return err
				},
			}
			if tc.failCommit {
				turn, err := manager.RunTurn(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				if err := turn.Handle.WaitCompletion(ctx); !errors.Is(err, commitErr) {
					t.Fatalf("commit failure = %v", err)
				}
				select {
				case got := <-seen:
					t.Fatalf("prompt dispatched before binding commit: %#v", got)
				default:
				}
				failCommit = false
				// The failed bootstrap must survive even when the next routed
				// increment is empty; no new remote is created on this retry.
				req.Context = agent.ContextTransfer{}
				req.FreshContext = agent.ContextTransfer{}
			}
			turn, err := manager.RunTurn(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-seen:
				if got.err != nil {
					t.Fatalf("collaboration during first prompt: %v", got.err)
				}
				checkpoint := uint64(2)
				wantContext := "DELTA-CONTEXT"
				if tc.fresh {
					checkpoint, wantContext = 0, "FULL-CONTEXT"
				}
				if got.binding.RemoteSessionID != remote || got.binding.EpochID != binding.EpochID || got.binding.ContextSyncSeq != checkpoint {
					t.Fatalf("binding before prompt completion = %#v", got.binding)
				}
				encoded, _ := json.Marshal(got.prompt.Prompt)
				if !strings.Contains(string(encoded), wantContext) || (tc.fresh && strings.Contains(string(encoded), "DELTA-CONTEXT")) || !strings.Contains(string(encoded), "caelis-collaboration") {
					t.Fatalf("prompt lost bootstrap or discovery slice: %s", encoded)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			finishPrompt <- struct{}{}
			if err := turn.Handle.WaitCompletion(ctx); err != nil {
				t.Fatal(err)
			}
			if starts != 2 {
				t.Fatalf("client starts = %d, want initial plus one reconnect", starts)
			}
		})
	}
}
