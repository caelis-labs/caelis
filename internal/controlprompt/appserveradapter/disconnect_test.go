package appserveradapter

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/appserver"
)

func TestDisconnectTargetsStopsAtFailureAndReportsCommittedPrefix(t *testing.T) {
	wantErr := errors.New("removal interrupted")
	for _, outcome := range []appserver.Outcome{appserver.OutcomeRejected, appserver.OutcomeUnknown, appserver.OutcomeCommitted} {
		t.Run(string(outcome), func(t *testing.T) {
			var calls []string
			completed, err := disconnectTargets(context.Background(), []string{"first", " FIRST ", "", "second", "third"}, func(_ context.Context, target string) error {
				calls = append(calls, target)
				if target == "second" {
					return &appserver.CommandReceiptError{Receipt: appserver.CommandResult{Outcome: outcome}, Err: wantErr}
				}
				return nil
			})
			want := []string{"first"}
			if outcome == appserver.OutcomeCommitted {
				want = append(want, "second")
			}
			if !slices.Equal(completed, want) || !slices.Equal(calls, []string{"first", "second"}) {
				t.Fatalf("completed/called = %v/%v, want %v/[first second]", completed, calls, want)
			}
			var receipt *appserver.CommandReceiptError
			if !errors.Is(err, wantErr) || !errors.As(err, &receipt) || receipt.Receipt.Outcome != outcome {
				t.Fatalf("batch lost receipt: %v", err)
			}
		})
	}
}

func TestDisconnectTargetsStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	completed, err := disconnectTargets(ctx, []string{"first", "second"}, func(_ context.Context, target string) error {
		calls = append(calls, target)
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || !slices.Equal(completed, []string{"first"}) || !slices.Equal(calls, completed) {
		t.Fatalf("completed/called/error = %v/%v/%v", completed, calls, err)
	}
}

type disconnectBatchAgentClient struct {
	appserver.AgentClient
	revision uint64
	agentIDs []string
	requests []appserver.DisconnectACPRequest
}

func (c *disconnectBatchAgentClient) DisconnectCandidates(context.Context, appserver.AgentRequest) (appserver.DisconnectCandidatesSnapshot, error) {
	snapshot := appserver.DisconnectCandidatesSnapshot{Revision: c.revision}
	for _, id := range c.agentIDs {
		snapshot.Candidates = append(snapshot.Candidates, agents.DisconnectCandidate{AgentID: id, ConnectionID: "shared", LastOnConnection: len(c.agentIDs) == 1})
	}
	return snapshot, nil
}

func (c *disconnectBatchAgentClient) DisconnectACP(_ context.Context, req appserver.DisconnectACPRequest) (appserver.CommandResult, error) {
	c.requests = append(c.requests, req)
	if req.ExpectedRevision == nil || *req.ExpectedRevision != c.revision {
		return appserver.CommandResult{Outcome: appserver.OutcomeRejected}, errors.New("stale revision")
	}
	c.revision++
	c.agentIDs = slices.DeleteFunc(c.agentIDs, func(id string) bool { return id == req.AgentID })
	return appserver.CommandResult{Outcome: appserver.OutcomeCommitted, Revision: c.revision}, nil
}

func TestDisconnectACPAgentsReadsFreshRevisionForEachSelection(t *testing.T) {
	client := &disconnectBatchAgentClient{revision: 10, agentIDs: []string{"codex", "grok"}}
	adapter := &SessionClientAdapter{agentClient: client, surface: "tui"}
	completed, err := adapter.DisconnectACPAgents(context.Background(), []string{"codex", "grok"})
	if err != nil || !slices.Equal(completed, []string{"codex", "grok"}) || len(client.agentIDs) != 0 {
		t.Fatalf("completed = %v, remaining = %v, error = %v", completed, client.agentIDs, err)
	}
	if len(client.requests) != 2 || client.requests[0].OperationID == client.requests[1].OperationID {
		t.Fatalf("disconnect requests = %#v", client.requests)
	}
}
