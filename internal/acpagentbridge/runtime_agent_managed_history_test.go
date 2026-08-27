package acpagentbridge

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acp "github.com/caelis-labs/caelis/protocol/acp/schema"
)

var managedHistoryTestToken = strings.Repeat("ab", 32)

func TestRuntimeAgentProductLoadAcceptsExactManagedHistoryClaimWithoutOwnership(t *testing.T) {
	t.Parallel()

	state := appserver.SessionState{
		SessionID: "child-session",
		CWD:       "/workspace",
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: "parent-session",
			sessionvisibility.MetadataSystemManagedTask:   "task-1",
		},
	}
	client := &managedHistorySessionClient{state: state}
	agent := steeringTestAgent(client)
	agent.managedHistoryToken = managedHistoryTestToken
	claim := managedHistoryClaim("parent-session", "task-1", managedHistoryTestToken)

	if _, err := agent.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionID: state.SessionID,
		CWD:       state.CWD,
		Meta:      claim,
	}, nil); err != nil {
		t.Fatalf("LoadSession(exact managed history claim) error = %v", err)
	}
	if got := client.reconnects.Load(); got != 1 {
		t.Fatalf("Reconnect() calls = %d, want 1", got)
	}
	if agent.ownsManagedSession(state.SessionID) {
		t.Fatal("read-only session/load claimed managed Session execution ownership")
	}
	if _, err := agent.ResumeSession(context.Background(), acp.ResumeSessionRequest{
		SessionID: state.SessionID,
		CWD:       state.CWD,
		Meta:      claim,
	}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("ResumeSession(history claim) error = %v, want Session not found", err)
	}
}

func TestRuntimeAgentProductLoadRejectsMismatchedManagedHistoryClaim(t *testing.T) {
	t.Parallel()

	state := appserver.SessionState{
		SessionID: "child-session",
		CWD:       "/workspace",
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: "parent-session",
			sessionvisibility.MetadataSystemManagedTask:   "task-1",
		},
	}
	client := &managedHistorySessionClient{state: state}
	agent := steeringTestAgent(client)
	agent.managedHistoryToken = managedHistoryTestToken

	for _, meta := range []map[string]any{
		nil,
		managedHistoryClaim("parent-session", "task-other", managedHistoryTestToken),
		managedHistoryClaim("parent-session", "task-1", strings.Repeat("cd", 32)),
		managedHistoryClaim("parent-session", "task-1", ""),
	} {
		if _, err := agent.LoadSession(context.Background(), acp.LoadSessionRequest{
			SessionID: state.SessionID,
			CWD:       state.CWD,
			Meta:      meta,
		}, nil); !errors.Is(err, session.ErrSessionNotFound) {
			t.Fatalf("LoadSession(metadata=%#v) error = %v, want Session not found", meta, err)
		}
	}
	// Product ACP does not gain lifecycle-load permission merely because this
	// bridge instance previously created or prompted the managed child.
	agent.managedSessions[state.SessionID] = struct{}{}
	if _, err := agent.LoadSession(context.Background(), acp.LoadSessionRequest{
		SessionID: state.SessionID,
		CWD:       state.CWD,
		Meta:      managedHistoryClaim("parent-session", "task-1", ""),
	}, nil); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("LoadSession(owned managed Session without capability) error = %v, want Session not found", err)
	}
	if got := client.reconnects.Load(); got != 0 {
		t.Fatalf("Reconnect() calls = %d, want 0 for rejected claims", got)
	}
}

func TestRuntimeAgentProductResumeKeepsExecutionAndHistoryBridgesDisjoint(t *testing.T) {
	t.Parallel()

	state := appserver.SessionState{
		SessionID: "child-session",
		CWD:       "/workspace",
		Metadata: map[string]any{
			sessionvisibility.MetadataSystemManagedAgent:  sessionvisibility.SystemManagedAgentSubagent,
			sessionvisibility.MetadataSystemManagedParent: "parent-session",
			sessionvisibility.MetadataSystemManagedTask:   "task-1",
		},
	}
	claim := managedHistoryClaim("parent-session", "task-1", "")
	execution := steeringTestAgent(&managedHistorySessionClient{state: state})
	if _, err := execution.ResumeSession(context.Background(), acp.ResumeSessionRequest{
		SessionID: state.SessionID,
		CWD:       state.CWD,
		Meta:      claim,
	}); err != nil {
		t.Fatalf("ResumeSession(exact execution relation) error = %v", err)
	}
	if !execution.ownsManagedSession(state.SessionID) {
		t.Fatal("execution reconnect did not reclaim managed Session ownership")
	}

	history := steeringTestAgent(&managedHistorySessionClient{state: state})
	history.managedHistoryToken = managedHistoryTestToken
	if _, err := history.ResumeSession(context.Background(), acp.ResumeSessionRequest{
		SessionID: state.SessionID,
		CWD:       state.CWD,
		Meta:      managedHistoryClaim("parent-session", "task-1", managedHistoryTestToken),
	}); !errors.Is(err, session.ErrSessionNotFound) {
		t.Fatalf("ResumeSession(read-only history bridge) error = %v, want Session not found", err)
	}
	if history.ownsManagedSession(state.SessionID) {
		t.Fatal("read-only history bridge acquired managed Session ownership")
	}
}

func managedHistoryClaim(parentSessionID, taskID, token string) map[string]any {
	return metautil.WithCompactRuntimeSection(nil, metautil.RuntimeSession, map[string]any{
		metautil.RuntimeSessionKind:         metautil.RuntimeSessionKindSubagent,
		metautil.RuntimeSessionParentID:     parentSessionID,
		metautil.RuntimeTaskID:              taskID,
		metautil.RuntimeSessionHistoryToken: token,
	})
}

type managedHistorySessionClient struct {
	appserver.SessionClient
	state      appserver.SessionState
	reconnects atomic.Int32
}

func (c *managedHistorySessionClient) InspectSession(context.Context, appserver.StateRequest) (appserver.SessionState, error) {
	return c.state, nil
}

func (c *managedHistorySessionClient) Reconnect(context.Context, appserver.ReconnectRequest) (appserver.ReconnectResult, error) {
	c.reconnects.Add(1)
	return appserver.ReconnectResult{
		State:        c.state,
		Subscription: emptyManagedHistorySubscription{},
	}, nil
}

type emptyManagedHistorySubscription struct{}

func (emptyManagedHistorySubscription) Backfill() <-chan eventstream.Envelope {
	ch := make(chan eventstream.Envelope)
	close(ch)
	return ch
}

func (emptyManagedHistorySubscription) Events() <-chan eventstream.Envelope {
	ch := make(chan eventstream.Envelope)
	close(ch)
	return ch
}

func (emptyManagedHistorySubscription) BackfillDone() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (emptyManagedHistorySubscription) Close() error       { return nil }
func (emptyManagedHistorySubscription) Err() error         { return nil }
func (emptyManagedHistorySubscription) LastCursor() string { return "" }
