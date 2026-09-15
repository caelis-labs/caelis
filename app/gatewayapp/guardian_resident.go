package gatewayapp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const guardianResidentLaneCount = 4

// One root owns bounded, reusable execution lanes. Conversation commits still
// belong to guardianConversationManager; a busy lane never sees sibling work.
type guardianResident struct {
	mu         sync.Mutex
	closed     bool
	ctx        context.Context
	cancel     context.CancelFunc
	active     sync.WaitGroup
	lanes      chan *guardianQueries
	projection guardianProjection
}

func (r *guardianApprovalReviewer) acquireResident(ctx context.Context, ref session.SessionRef) (*guardianResident, *guardianQueries, func(), error) {
	r.resourcesMu.Lock()
	if r.closed {
		r.resourcesMu.Unlock()
		return nil, nil, nil, fmt.Errorf("guardian is closed")
	}
	if r.residents == nil {
		r.residents = map[string]*guardianResident{}
	}
	resident := r.residents[ref.SessionID]
	if resident == nil {
		lifetime, cancel := context.WithCancel(context.Background())
		resident = &guardianResident{ctx: lifetime, cancel: cancel, lanes: make(chan *guardianQueries, guardianResidentLaneCount)}
		for range guardianResidentLaneCount {
			resident.lanes <- nil
		}
		r.residents[ref.SessionID] = resident
	}
	resident.mu.Lock()
	if resident.closed {
		resident.mu.Unlock()
		r.resourcesMu.Unlock()
		return nil, nil, nil, context.Canceled
	}
	resident.active.Add(1)
	resident.mu.Unlock()
	r.resourcesMu.Unlock()
	var q *guardianQueries
	select {
	case q = <-resident.lanes:
	case <-ctx.Done():
		resident.active.Done()
		return nil, nil, nil, ctx.Err()
	case <-resident.ctx.Done():
		resident.active.Done()
		return nil, nil, nil, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		resident.lanes <- q
		resident.active.Done()
		return nil, nil, nil, err
	}
	if resident.ctx.Err() != nil {
		resident.lanes <- q
		resident.active.Done()
		return nil, nil, nil, context.Canceled
	}
	if q == nil {
		// Prefer an idle initialized lane over an unused concurrency slot.
		// Scan every immediately available slot; peeking once would repeatedly
		// initialize cold lanes while a warm one waits behind empty slots.
		empty := 0
	search:
		for {
			select {
			case ready := <-resident.lanes:
				empty++
				if ready != nil {
					q = ready
					break search
				}
			default:
				break search
			}
		}
		for range empty {
			resident.lanes <- nil
		}
	}
	if q == nil {
		q = &guardianQueries{network: r.queryNetwork}
		if runner, ok := r.systemAgents.(*systemManagedAgentRuntime); ok {
			q.runner = &systemManagedAgentRuntime{config: runner.config, resident: true}
		}
	}
	return resident, q, func() { resident.lanes <- q; resident.active.Done() }, nil
}

func (r *guardianResident) close() error {
	r.mu.Lock()
	r.closed = true
	r.cancel()
	r.mu.Unlock()
	r.active.Wait()
	var first error
	for i := 0; i < cap(r.lanes); i++ {
		if q := <-r.lanes; q != nil {
			if err := q.close(); first == nil {
				first = err
			}
		}
	}
	return first
}

func (r *guardianApprovalReviewer) Close() error {
	r.resourcesMu.Lock()
	if r.closed {
		r.resourcesMu.Unlock()
		return nil
	}
	r.closed = true
	residents := r.residents
	r.residents = nil
	r.resourcesMu.Unlock()
	var first error
	for _, resident := range residents {
		if err := resident.close(); first == nil {
			first = err
		}
	}
	r.reviews.Wait()
	return first
}

// Metrics contain no command, evidence body, credentials or model reasoning.
type guardianReviewMetrics struct {
	Model             string         `json:"model"`
	Origin            string         `json:"origin"`
	UsageReported     bool           `json:"usage_reported"`
	InputTokens       int            `json:"input_tokens"`
	CachedInputTokens int            `json:"cached_input_tokens"`
	OutputTokens      int            `json:"output_tokens"`
	ReviewID          string         `json:"review_id"`
	TotalMS           int64          `json:"total_ms"`
	QueueMS           int64          `json:"queue_ms"`
	ControlQueueMS    int64          `json:"control_queue_ms"`
	LaneQueueMS       int64          `json:"lane_queue_ms"`
	MaxRequestTokens  int            `json:"max_request_tokens"`
	InputBudgetTokens int            `json:"input_budget_tokens"`
	PrepareMS         int64          `json:"prepare_ms"`
	ModelMS           int64          `json:"model_ms"`
	ToolMS            int64          `json:"tool_ms"`
	SetupMS           int64          `json:"setup_ms"`
	ModelCalls        int            `json:"model_calls"`
	ToolCalls         int            `json:"tool_calls"`
	ToolErrors        int            `json:"tool_errors"`
	ToolErrorKinds    map[string]int `json:"tool_error_kinds,omitempty"`
	EvidenceBytes     int            `json:"evidence_bytes"`
	TruncatedResults  int            `json:"truncated_results"`
	RuntimeReused     bool           `json:"runtime_reused"`
	ContextTrimmed    bool           `json:"context_trimmed"`
	SandboxReused     bool           `json:"sandbox_reused"`
	SourceThrough     uint64         `json:"source_through"`
	Outcome           string         `json:"outcome"`
	OptionID          string         `json:"option_id,omitempty"`
}

func guardianElapsed(start time.Time) int64 { return time.Since(start).Milliseconds() }
