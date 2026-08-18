package gatewayapp

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	sdkruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/kernel"
)

func TestGuardianMainSessionSummaryIsMarkedWithoutBecomingDirectUserEvidence(t *testing.T) {
	t.Parallel()

	const (
		summaryText    = "The user requested the prepared fix and authorized its narrow commit."
		directUserText = "Run the focused checks before committing."
	)
	parentEvents := guardianMainSummaryParentEvents(summaryText, directUserText)
	items, err := buildGuardianPromptItems(parentEvents, guardianPromptMode{
		ParentCompact: guardianParentCompactIdentityFromEvents(parentEvents),
	}, guardianMainSummaryApprovalRequest())
	if err != nil {
		t.Fatalf("buildGuardianPromptItems() error = %v", err)
	}

	for _, want := range []string{
		"[1] main session summary:",
		guardianMainSessionSummaryMarker,
		"not a new direct user message",
		summaryText,
		"[2] user:",
		directUserText,
	} {
		if !strings.Contains(items.Text, want) {
			t.Fatalf("Guardian prompt missing %q:\n%s", want, items.Text)
		}
	}
	if len(items.UserEvidence) != 1 {
		t.Fatalf("UserEvidence = %#v, want only the direct user message", items.UserEvidence)
	}
	if got := items.UserEvidence[0]; got != directUserText {
		t.Fatalf("direct user evidence = %q, want %q", got, directUserText)
	}
	if strings.Contains(items.UserEvidence[0], guardianMainSessionSummaryMarker) || strings.Contains(items.UserEvidence[0], summaryText) {
		t.Fatalf("main Session summary was promoted to direct user evidence: %q", items.UserEvidence[0])
	}
	if event := guardianUserEvent(session.Session{}, items.Text); event.Actor.Kind != session.ActorKindSystem {
		t.Fatalf("Guardian prompt actor = %#v, want Control-authored system identity", event.Actor)
	}
}

func TestGuardianMainSessionSummarySurvivesGuardianAutoCompact(t *testing.T) {
	t.Parallel()

	const (
		summaryText     = "The user requested the prepared fix and authorized its narrow commit."
		directUserText  = "Run the focused checks before committing."
		currentApproval = "CURRENT_APPROVAL exact tool=RUN_COMMAND cmd=git commit -m fix"
	)
	parentEvents := guardianMainSummaryParentEvents(summaryText, directUserText)
	items, err := buildGuardianPromptItems(parentEvents, guardianPromptMode{
		ParentCompact: guardianParentCompactIdentityFromEvents(parentEvents),
	}, guardianMainSummaryApprovalRequest())
	if err != nil {
		t.Fatalf("buildGuardianPromptItems() error = %v", err)
	}

	history := make([]*session.Event, 0, 6)
	for index := 0; index < 3; index++ {
		oldPrompt := items.Text + "\n" + strings.Repeat("historical Guardian context ", 80)
		oldAssessment := strings.Repeat("prior Guardian assessment ", 80)
		user := model.NewTextMessage(model.RoleUser, oldPrompt)
		assistant := model.NewTextMessage(model.RoleAssistant, oldAssessment)
		history = append(history,
			&session.Event{
				Type:       session.EventTypeUser,
				Actor:      session.ActorRef{Kind: session.ActorKindSystem, Name: guardianSceneID},
				Message:    &user,
				Text:       oldPrompt,
				Compaction: &session.EventCompactionContext{UserEvidence: append([]string(nil), items.UserEvidence...)},
			},
			&session.Event{Type: session.EventTypeAssistant, Message: &assistant, Text: oldAssessment},
		)
	}

	probe := &guardianMainSummaryCompactionProbe{}
	runner := newSystemManagedAgentRuntime(nil)
	result, err := runner.Run(context.Background(), systemManagedAgentRunRequest{
		AgentID: guardianSceneID,
		Model:   probe,
		ParentSession: session.Session{SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "parent-summary-session", WorkspaceKey: "workspace-1",
		}},
		Events:            history,
		Input:             currentApproval,
		InputUserEvidence: []string{"The user now requested the exact commit."},
		Compaction: sdkruntime.CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.2,
			ForceWatermarkRatio:        0.3,
			DefaultContextWindowTokens: 256,
			ReserveOutputTokens:        32,
			SafetyMarginTokens:         16,
			SegmentTokenBudget:         512,
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	compactCalls, normalCalls, compactReq, normalReq := probe.snapshot()
	if compactCalls != 1 || normalCalls != 1 {
		t.Fatalf("model calls = compact %d / normal %d, want 1/1", compactCalls, normalCalls)
	}
	for _, want := range []string{
		`"source":"user"`,
		guardianMainSessionSummaryMarker,
		summaryText,
		directUserText,
	} {
		if !guardianMainSummaryRequestContains(compactReq, want) {
			t.Fatalf("Guardian compaction request missing %q: %#v", want, compactReq.Messages)
		}
	}
	frames := guardianMainSummarySourceFrames(compactReq)
	seenDirectUser := false
	for _, frame := range frames {
		if frame.Source != "user" {
			continue
		}
		if strings.Contains(frame.Payload, guardianMainSessionSummaryMarker) || strings.Contains(frame.Payload, summaryText) {
			t.Fatalf("main Session summary was promoted to source=user: %#v", frame)
		}
		if strings.Contains(frame.Payload, directUserText) {
			seenDirectUser = true
		}
	}
	if !seenDirectUser {
		t.Fatalf("compaction frames = %#v, want direct main-Session user evidence", frames)
	}
	if guardianMainSummaryRequestContains(compactReq, currentApproval) {
		t.Fatalf("Guardian compaction summarized the current exact approval: %#v", compactReq.Messages)
	}
	for _, want := range []string{"CONTEXT CHECKPOINT", guardianMainSessionSummaryMarker, currentApproval} {
		if !guardianMainSummaryRequestContains(normalReq, want) {
			t.Fatalf("post-compact Guardian request missing %q: %#v", want, normalReq.Messages)
		}
	}
	if result.Text != `{"outcome":"allow"}` {
		t.Fatalf("result.Text = %q, want compact allow assessment", result.Text)
	}
}

func TestSystemManagedAgentCompactContextUsesOnlyTransientStaging(t *testing.T) {
	t.Parallel()

	probe := &guardianMainSummaryCompactionProbe{}
	user := model.NewTextMessage(model.RoleUser, "historical Guardian approval request")
	assistant := model.NewTextMessage(model.RoleAssistant, "historical validated Guardian assessment")
	runner := newSystemManagedAgentRuntime(nil)
	result, err := runner.CompactContext(context.Background(), systemManagedAgentCompactRequest{
		AgentID: guardianSceneID,
		Purpose: systemManagedAgentPurposeApprovalReview,
		Model:   probe,
		ParentSession: session.Session{SessionRef: session.SessionRef{
			AppName: "caelis", UserID: "user-1", SessionID: "parent-transient-compact", WorkspaceKey: "workspace-1",
		}},
		Events: []*session.Event{
			{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &user, Text: user.TextContent()},
			{Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, Message: &assistant, Text: assistant.TextContent()},
		},
		Compaction: sdkruntime.CompactionConfig{
			Enabled:                    true,
			DefaultContextWindowTokens: 32_768,
			ReserveOutputTokens:        2_048,
			SafetyMarginTokens:         1_024,
		},
	})
	if err != nil {
		t.Fatalf("CompactContext() error = %v", err)
	}
	if !result.Compacted || len(result.Events) != 1 || session.EventTypeOf(result.Events[0]) != session.EventTypeCompact {
		t.Fatalf("CompactContext() result = %#v, want one process-local checkpoint", result)
	}
	if !strings.Contains(session.EventText(result.Events[0]), "CONTEXT CHECKPOINT") {
		t.Fatalf("checkpoint = %q, want compact model output", session.EventText(result.Events[0]))
	}
	compactCalls, normalCalls, _, _ := probe.snapshot()
	if compactCalls != 1 || normalCalls != 0 {
		t.Fatalf("model calls = compact %d / normal %d, want 1/0", compactCalls, normalCalls)
	}
}

func guardianMainSummaryParentEvents(summaryText string, directUserText string) []*session.Event {
	summary := model.NewTextMessage(model.RoleUser, summaryText)
	directUser := model.NewTextMessage(model.RoleUser, directUserText)
	return []*session.Event{
		{
			ID: "compact-2", Seq: 2, Type: session.EventTypeCompact, Visibility: session.VisibilityCanonical,
			Message: &summary, Text: summaryText,
			Meta: map[string]any{compact.MetaKeyCompact: compact.CompactEventDataValue(compact.CompactEventData{
				ContractVersion: compact.CompactContractVersion, SummarizedThroughID: "parent-1", SummarizedThroughSeq: 1,
			})},
		},
		{
			ID: "user-3", Seq: 3, Type: session.EventTypeUser, Visibility: session.VisibilityCanonical,
			Message: &directUser, Text: directUserText,
		},
	}
}

func guardianMainSummaryApprovalRequest() kernel.ApprovalReviewRequest {
	return kernel.ApprovalReviewRequest{Approval: &kernel.ApprovalPayload{
		ToolName: "RunCommand",
		Reason:   "commit the prepared fix",
		RawInput: map[string]any{"cmd": "git commit -m fix"},
		Status:   kernel.ApprovalStatusPending,
	}}
}

type guardianMainSummaryCompactionProbe struct {
	mu              sync.Mutex
	compactionCalls int
	normalCalls     int
	compactionReq   *model.Request
	normalReq       *model.Request
}

func (*guardianMainSummaryCompactionProbe) Name() string {
	return "guardian-main-summary-compaction-probe"
}

func (*guardianMainSummaryCompactionProbe) Capabilities() model.Capabilities {
	return model.Capabilities{Streaming: true, StructuredOutput: true}
}

func (m *guardianMainSummaryCompactionProbe) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.mu.Lock()
	if strings.Contains(guardianMainSummaryRequestInstructions(req), "CONTEXT CHECKPOINT COMPACTION") {
		m.compactionCalls++
		m.compactionReq = model.CloneRequest(req)
		m.mu.Unlock()
		return guardianMainSummaryTextResponse(`CONTEXT CHECKPOINT

## Current Objective
- review the next exact approval request safely

## User Constraints
- [MAIN SESSION SUMMARY] The parent summary preserves the user's narrow commit authorization.
- Direct user evidence requires focused checks before committing.

## Durable Decisions
- prior Guardian decisions do not grant blanket authorization

## Verified Facts
- Guardian history exceeded the local context budget

## Current Progress
- prior dialogue was compacted

## Open Questions / Risks
- assess the current exact action

## Next Actions
1. evaluate the pending approval request

## Active Tasks
- none

## Active Participants
- none

## Latest Blockers
- none

## Operational Notes
- Guardian context remains process-local`)
	}
	m.normalCalls++
	m.normalReq = model.CloneRequest(req)
	m.mu.Unlock()
	return guardianMainSummaryTextResponse(`{"outcome":"allow"}`)
}

func (m *guardianMainSummaryCompactionProbe) snapshot() (int, int, *model.Request, *model.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.compactionCalls, m.normalCalls, model.CloneRequest(m.compactionReq), model.CloneRequest(m.normalReq)
}

func guardianMainSummaryRequestInstructions(req *model.Request) string {
	if req == nil {
		return ""
	}
	parts := make([]string, 0, len(req.Instructions))
	for _, part := range req.Instructions {
		if part.Text != nil {
			parts = append(parts, part.Text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func guardianMainSummaryRequestContains(req *model.Request, text string) bool {
	if req == nil {
		return false
	}
	for _, message := range req.Messages {
		if strings.Contains(message.TextContent(), text) {
			return true
		}
	}
	return false
}

type guardianMainSummarySourceFrame struct {
	Source  string `json:"source"`
	Payload string `json:"payload"`
}

func guardianMainSummarySourceFrames(req *model.Request) []guardianMainSummarySourceFrame {
	if req == nil {
		return nil
	}
	frames := make([]guardianMainSummarySourceFrame, 0, 4)
	for _, message := range req.Messages {
		for _, line := range strings.Split(message.TextContent(), "\n") {
			const prefix = "CAELIS_SOURCE_FRAME_V1 "
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			var frame guardianMainSummarySourceFrame
			if json.Unmarshal([]byte(strings.TrimPrefix(line, prefix)), &frame) == nil {
				frames = append(frames, frame)
			}
		}
	}
	return frames
}

func guardianMainSummaryTextResponse(text string) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: &model.Response{
			Status: model.ResponseStatusCompleted, TurnComplete: true, StepComplete: true,
			Message: model.NewTextMessage(model.RoleAssistant, text),
		}}, nil)
	}
}
