package tuiapp

import (
	"context"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func narrativeTestSource() narrativeSourceIdentity {
	return newNarrativeSourceIdentity("test-message", "test-event", "test-projection")
}

func parentToolStreamMeta(callID string, toolName string) map[string]any {
	return testMeta.WithRuntimeSection(nil, testMeta.RuntimeStream, map[string]any{
		testMeta.RuntimeStreamParentCallID: strings.TrimSpace(callID),
		testMeta.RuntimeStreamParentTool:   strings.TrimSpace(toolName),
	})
}

func applyACPEnvelopeForTest(t *testing.T, model *Model, env eventstream.Envelope) *Model {
	t.Helper()
	next, _ := model.handleACPEventEnvelope(env)
	typed, ok := next.(*Model)
	if !ok {
		t.Fatalf("model = %T, want *Model", next)
	}
	return typed
}

func applyTranscriptEventForTest(t *testing.T, model *Model, event TranscriptEvent) *Model {
	t.Helper()
	next, _ := model.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: []TranscriptEvent{event}})
	typed, ok := next.(*Model)
	if !ok {
		t.Fatalf("model = %T, want *Model", next)
	}
	return typed
}

func requireMainACPTurnBlockForTest(t *testing.T, model *Model) *MainACPTurnBlock {
	t.Helper()
	if id := strings.TrimSpace(model.mainTimelineTailID); id != "" {
		if block, _ := model.doc.Find(id).(*MainACPTurnBlock); block != nil {
			return block
		}
	}
	for _, docBlock := range model.doc.Blocks() {
		if block, ok := docBlock.(*MainACPTurnBlock); ok {
			return block
		}
	}
	t.Fatal("main ACP turn block missing")
	return nil
}

func requireSubagentOutputViewForTest(t *testing.T, model *Model, callID string) *subagentOutputView {
	t.Helper()
	if model != nil {
		if view := model.subagentOutputViews[strings.TrimSpace(callID)]; view != nil && view.block != nil {
			return view
		}
	}
	t.Fatalf("subagent output view %q missing", callID)
	return nil
}

func subagentOutputNarrativeTextForTest(view *subagentOutputView, kind SubagentEventKind) string {
	if view == nil || view.block == nil {
		return ""
	}
	var text strings.Builder
	for _, event := range view.block.Events {
		if event.Kind == kind {
			text.WriteString(event.Text)
		}
	}
	return text.String()
}

func mainACPTurnBlocksForTest(model *Model) []*MainACPTurnBlock {
	if model == nil || model.doc == nil {
		return nil
	}
	var blocks []*MainACPTurnBlock
	for _, docBlock := range model.doc.Blocks() {
		if block, ok := docBlock.(*MainACPTurnBlock); ok {
			blocks = append(blocks, block)
		}
	}
	return blocks
}

func physicalTranscriptEventsForTest(events []SubagentEvent) []SubagentEvent {
	physical := make([]SubagentEvent, 0, len(events))
	for _, event := range events {
		if event.Kind != SESemanticBoundary {
			physical = append(physical, event)
		}
	}
	return physical
}

func countUserNarrativeBlocksForTest(model *Model, text string) int {
	if model == nil || model.doc == nil {
		return 0
	}
	count := 0
	for _, docBlock := range model.doc.Blocks() {
		if user, ok := docBlock.(*UserNarrativeBlock); ok && strings.TrimSpace(user.Raw) == strings.TrimSpace(text) {
			count++
		}
	}
	return count
}

func mainACPBlockContainsText(block *MainACPTurnBlock, text string) bool {
	text = strings.TrimSpace(text)
	if block == nil || text == "" {
		return false
	}
	for _, event := range block.Events {
		if strings.Contains(event.Text, text) {
			return true
		}
	}
	return false
}

func mainACPBlockHasToolOutput(block *MainACPTurnBlock, text string) bool {
	text = strings.TrimSpace(text)
	if block == nil || text == "" {
		return false
	}
	for _, event := range block.Events {
		if event.Kind == SEToolCall && strings.Contains(event.Output, text) {
			return true
		}
	}
	return false
}

type eventstreamIntegrationTurn struct {
	events <-chan eventstream.Envelope
}

func (t *eventstreamIntegrationTurn) HandleID() string { return "handle-1" }
func (t *eventstreamIntegrationTurn) RunID() string    { return "run-1" }
func (t *eventstreamIntegrationTurn) TurnID() string   { return "turn-1" }

func (t *eventstreamIntegrationTurn) Events() <-chan eventstream.Envelope {
	return t.events
}

func (t *eventstreamIntegrationTurn) SubmitApproval(context.Context, controlprompt.ApprovalDecision) error {
	return nil
}

func (t *eventstreamIntegrationTurn) Cancel() {}

func (t *eventstreamIntegrationTurn) Close() error { return nil }
