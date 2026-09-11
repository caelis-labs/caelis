package acpagentbridge

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/internal/acputil"
)

func managedReplayState() appserver.SessionState {
	return appserver.SessionState{SessionID: "child-session", CWD: "/workspace", Metadata: map[string]any{
		sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
		sessionvisibility.MetadataSystemManagedParent: "parent-session",
		sessionvisibility.MetadataSystemManagedTask:   "task-1",
	}}
}

func TestRuntimeAgentManagedLoadReplaysThenRetainsExecutionOwnership(t *testing.T) {
	t.Parallel()
	state := managedReplayState()
	client := &managedHistorySessionClient{state: state}
	agent := steeringTestAgent(client)
	_, err := agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{
		SessionId: acpsdk.SessionId(state.SessionID), Cwd: state.CWD,
		Meta: managedHistoryRawMeta(t, acputil.NewSubagentSessionMeta("parent-session", "task-1")),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.reconnects.Load() != 1 {
		t.Fatal("load did not replay exactly once")
	}
	if _, err := agent.targetSession(t.Context(), state.SessionID); err != nil {
		t.Fatalf("loaded connection cannot continue: %v", err)
	}
}

func TestRuntimeAgentManagedLoadAndResumeRequireExactRelation(t *testing.T) {
	t.Parallel()
	for _, load := range []bool{true, false} {
		for _, claim := range []map[string]any{nil, acputil.NewSubagentSessionMeta("wrong", "task-1"), acputil.NewSubagentSessionMeta("parent-session", "wrong")} {
			state := managedReplayState()
			client := &managedHistorySessionClient{state: state}
			agent := steeringTestAgent(client)
			var err error
			if load {
				_, err = agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{SessionId: acpsdk.SessionId(state.SessionID), Meta: managedHistoryRawMeta(t, claim)}, nil)
			} else {
				_, err = agent.ResumeSession(t.Context(), acpsdk.ResumeSessionRequest{SessionId: acpsdk.SessionId(state.SessionID), Meta: managedHistoryRawMeta(t, claim)})
			}
			if !errors.Is(err, session.ErrSessionNotFound) {
				t.Fatalf("load=%v claim=%v: %v", load, claim, err)
			}
			if agent.ownsManagedSession(state.SessionID) || client.reconnects.Load() != 0 {
				t.Fatal("rejected relation acquired ownership or read history")
			}
		}
	}
}

func TestRuntimeAgentFailedManagedReplayDoesNotAcquireOwnership(t *testing.T) {
	t.Parallel()
	state := managedReplayState()
	expected := errors.New("replay interrupted")
	client := &managedHistorySessionClient{state: state, err: expected}
	agent := steeringTestAgent(client)
	_, err := agent.LoadSession(t.Context(), acpsdk.LoadSessionRequest{SessionId: acpsdk.SessionId(state.SessionID), Meta: managedHistoryRawMeta(t, acputil.NewSubagentSessionMeta("parent-session", "task-1"))}, nil)
	if !errors.Is(err, expected) || agent.ownsManagedSession(state.SessionID) {
		t.Fatalf("failed load: %v ownership=%v", err, agent.ownsManagedSession(state.SessionID))
	}
}

func managedHistoryRawMeta(t *testing.T, meta map[string]any) map[string]json.RawMessage {
	t.Helper()
	if len(meta) == 0 {
		return nil
	}
	result := make(map[string]json.RawMessage, len(meta))
	for key, value := range meta {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("marshal metadata %q: %v", key, err)
		}
		result[key] = raw
	}
	return result
}

type managedHistorySessionClient struct {
	appserver.SessionClient
	state      appserver.SessionState
	err        error
	reconnects atomic.Int32
}

func (c *managedHistorySessionClient) InspectSession(context.Context, appserver.StateRequest) (appserver.SessionState, error) {
	return c.state, nil
}

func (c *managedHistorySessionClient) Reconnect(context.Context, appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	c.reconnects.Add(1)
	if c.err != nil {
		return appserver.ReconnectResult{}, c.err
	}
	return appserver.ReconnectResult{
		State:        c.state,
		Subscription: emptyManagedHistorySubscription{},
	}, nil
}

type emptyManagedHistorySubscription struct{}

func (emptyManagedHistorySubscription) Deliveries() <-chan appserver.FeedDelivery {
	ch := make(chan appserver.FeedDelivery)
	close(ch)
	return ch
}

func (emptyManagedHistorySubscription) Close() error { return nil }
func (emptyManagedHistorySubscription) Err() error   { return nil }
