package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

// ToolPair relates accepted intent to its single outcome. Calls synthesized from
// a legacy model envelope preserve that envelope's identity and batch index.
// They are a read projection, never a second persisted call or execution claim.
type ToolPair struct {
	Call    *Event
	Result  *Event
	BatchID string
}

// PairToolFacts pairs occurrences, including known pre-schema-3 partial batches.
// Ambiguous legacy IDs fail closed instead of assigning a result to another call.
func PairToolFacts(events []*Event) ([]ToolPair, error) {
	return pairToolFacts(toolPairInputs(events))
}

func pairToolFacts(events []*Event) ([]ToolPair, error) {
	var pairs []ToolPair
	occurrences := map[string]int{}
	sibling := map[*Event]bool{}
	for index, event := range events {
		if event == nil || event.Type != EventTypeToolCall || event.Tool == nil || sibling[event] {
			continue
		}
		if !IsMainInvocationVisibleEvent(event) && (event.Tool.Parent == nil || event.Tool.Parent.Kind != "code_mode") && (event.Scope == nil || event.Scope.Source != "runtime_tool" || event.Tool.Execution.ItemID == "") {
			continue
		}
		calls := event.Message
		if calls != nil && len(calls.ToolCalls()) > 0 && IsMainInvocationVisibleEvent(event) {
			batch := event.Tool.BatchID
			if batch == "" {
				batch = event.ID
				if batch == "" {
					batch = fmt.Sprintf("legacy-batch:%d", index)
				}
			}
			for ordinal, protocol := range calls.ToolCalls() {
				fact := CloneEvent(event)
				fact.Message = nil
				var typed *Event
				if ordinal == 0 && event.Tool.ID == protocol.ID {
					typed = event
				}
				for _, next := range events[index+1:] {
					if next == nil {
						continue
					}
					if next.Type != EventTypeToolCall || !IsMainInvocationVisibleEvent(next) {
						break
					}
					if next.Message != nil {
						break
					}
					if next.Tool != nil && next.Tool.ID == protocol.ID {
						typed = next
						sibling[next] = true
						break
					}
				}
				if typed != nil {
					fact = CloneEvent(typed)
				} else {
					if event.Tool.Execution.ItemID != "" {
						return nil, fmt.Errorf("incomplete schema-3 tool batch %q", batch)
					}
					input := map[string]any{}
					decoder := json.NewDecoder(strings.NewReader(protocol.Args))
					decoder.UseNumber()
					if err := decoder.Decode(&input); err != nil {
						return nil, err
					}
					fact.Tool = &EventTool{ID: protocol.ID, Name: protocol.Name, Input: input}
				}
				if fact.Tool.Execution.ItemID == "" {
					fact.Tool.Execution = tool.InvocationContext{SessionID: event.SessionID, TurnID: turnOf(event), ItemID: fmt.Sprintf("legacy:%s:%d", batch, ordinal)}
				}
				if ordinal == 0 {
					fact.Message = event.Message
					fact.MessageID = event.MessageID
				}
				fact.Tool.BatchID = batch
				key := occurrenceKey(fact.Tool.Execution)
				if _, exists := occurrences[key]; exists {
					return nil, fmt.Errorf("duplicate tool occurrence %q", key)
				}
				occurrences[key] = len(pairs)
				pairs = append(pairs, ToolPair{Call: fact, BatchID: batch})
			}
		} else {
			fact := CloneEvent(event)
			if fact.Tool.Execution.ItemID == "" {
				fact.Tool.Execution = tool.InvocationContext{SessionID: event.SessionID, TurnID: turnOf(event), ItemID: "legacy:" + event.ID}
			}
			if fact.Tool.BatchID == "" && IsMainInvocationVisibleEvent(fact) {
				fact.Tool.BatchID = "legacy-batch:" + event.ID
				if index > 0 && events[index-1] != nil && events[index-1].Type == EventTypeToolCall && events[index-1].Message == nil && len(pairs) > 0 {
					fact.Tool.BatchID = pairs[len(pairs)-1].BatchID
				}
			}
			key := occurrenceKey(fact.Tool.Execution)
			if _, exists := occurrences[key]; exists {
				return nil, fmt.Errorf("duplicate tool occurrence %q", key)
			}
			occurrences[key] = len(pairs)
			pairs = append(pairs, ToolPair{Call: fact, BatchID: fact.Tool.BatchID})
		}
	}
	for _, event := range events {
		if event == nil || event.Type != EventTypeToolResult {
			continue
		}
		if isLegacyResultBatch(event) {
			if err := pairLegacyBatch(pairs, event); err != nil {
				return nil, err
			}
			continue
		}
		if event.Tool == nil {
			continue
		}
		if event.Tool.Execution.ItemID != "" {
			if n, ok := occurrences[occurrenceKey(event.Tool.Execution)]; ok {
				if pairs[n].Call.Tool.ID != event.Tool.ID || pairs[n].Call.Tool.Name != event.Tool.Name {
					return nil, fmt.Errorf("tool result identity differs for occurrence %s", event.Tool.Execution.ItemID)
				}
				if original := pairs[n].Result; original != nil {
					// Older repair passes overlooked message-only result batches.
					// Prefer the earlier durable outcome to their interruption marker.
					if isLegacyResultBatch(original) && event.Tool.ErrorCode == "execution_interrupted" && event.Tool.Outcome == ToolOutcomeUnknown &&
						event.ID == "interrupted:"+event.Tool.Execution.TurnID+":"+event.Tool.Execution.ItemID && event.IdempotencyKey == event.ID {
						continue
					}
					return nil, fmt.Errorf("multiple results for occurrence %s", event.Tool.Execution.ItemID)
				}
				pairs[n].Result = event
			}
			continue
		}
		n, err := legacyResultIndex(pairs, event, event.Tool.ID)
		if err != nil {
			return nil, err
		}
		if n >= 0 {
			pairs[n].Result = event
		}
	}
	return pairs, nil
}

// A pre-occurrence model envelope may represent several outcomes in one
// message, with an optional typed fact for one member. Preserve its part order.
func isLegacyResultBatch(event *Event) bool {
	return (event.Tool == nil || event.Tool.Execution.ItemID == "") && event.Message != nil && len(event.Message.Parts) > 1 &&
		len(event.Message.ToolResults()) > 1 && IsMainInvocationVisibleEvent(event)
}

func pairLegacyBatch(pairs []ToolPair, event *Event) error {
	results := event.Message.ToolResults()
	matched := 0
	var batch string
	for _, result := range results {
		index, err := legacyResultIndex(pairs, event, result.ToolUseID)
		if err != nil {
			return err
		}
		if index < 0 {
			continue
		}
		if matched > 0 && batch != pairs[index].BatchID {
			return fmt.Errorf("legacy result message %q crosses tool batches", event.ID)
		}
		batch = pairs[index].BatchID
		pairs[index].Result = event
		matched++
	}
	if matched > 0 && matched != len(results) {
		return fmt.Errorf("legacy result message %q only partially matches its tool batch", event.ID)
	}
	return nil
}

func legacyResultIndex(pairs []ToolPair, event *Event, id string) (int, error) {
	index := -1
	for n, pair := range pairs {
		call := pair.Call
		if pair.Result != nil || call.Tool.ID != id || turnOf(call) != turnOf(event) || call.Seq > event.Seq && event.Seq > 0 {
			continue
		}
		if (call.Tool.Parent != nil) != (event.Tool != nil && event.Tool.Parent != nil) {
			continue
		}
		if call.Tool.Parent != nil && call.Tool.Parent.CallID != event.Tool.Parent.CallID {
			continue
		}
		if index >= 0 {
			return -1, fmt.Errorf("ambiguous legacy tool result %q in Turn %q", id, turnOf(event))
		}
		index = n
	}
	return index, nil
}

func occurrenceKey(execution tool.InvocationContext) string {
	return execution.SessionID + "/" + execution.TurnID + "/" + execution.ItemID
}
func turnOf(event *Event) string {
	if event.Scope != nil {
		return event.Scope.TurnID
	}
	return ""
}

// OrderToolFacts places late results next to their original model batch without
// changing durable sequence or checkpoint coverage. Orphans in a compacted raw
// tail do not reintroduce already covered calls.
func OrderToolFacts(events []*Event) ([]*Event, error) {
	events = toolPairInputs(events)
	pairs, err := pairToolFacts(events)
	if err != nil {
		return nil, err
	}
	bySeq := map[uint64][]ToolPair{}
	byPtr := map[*Event][]ToolPair{}
	batches := map[string][]ToolPair{}
	for _, pair := range pairs {
		if !IsMainInvocationVisibleEvent(pair.Call) {
			continue
		}
		key := pair.BatchID
		if key == "" {
			key = occurrenceKey(pair.Call.Tool.Execution)
		}
		batches[key] = append(batches[key], pair)
	}
	for _, group := range batches {
		seq := group[0].Call.Seq
		bySeq[seq] = append(bySeq[seq], group...)
		if seq == 0 {
			for _, event := range events {
				if event.Type == EventTypeToolCall && (event.Message == group[0].Call.Message && event.Message != nil || event.Tool != nil && event.Tool.BatchID != "" && event.Tool.BatchID == group[0].Call.Tool.BatchID || event.Message == nil && event.Tool != nil && event.Tool.ID == group[0].Call.Tool.ID) {
					byPtr[event] = group
					break
				}
			}
		}
	}
	emitted := make(map[*Event]bool)
	var out []*Event
	for _, event := range events {
		if event == nil {
			continue
		}
		if IsMainInvocationVisibleEvent(event) && (event.Type == EventTypeToolCall || event.Type == EventTypeToolResult) {
			group := bySeq[event.Seq]
			if event.Seq == 0 {
				group = byPtr[event]
			}
			if event.Type == EventTypeToolCall && len(group) > 0 {
				// Keep the original complete assistant message and typed siblings.
				for _, pair := range group {
					out = append(out, pair.Call)
				}
				for _, pair := range group {
					if pair.Result != nil && !emitted[pair.Result] {
						emitted[pair.Result] = true
						out = append(out, pair.Result)
					}
				}
			}
			continue
		}
		out = append(out, event)
	}
	return out, nil
}

func toolPairInputs(events []*Event) []*Event {
	out := make([]*Event, 0, len(events))
	for _, event := range events {
		if event != nil && event.Tool == nil && event.Message != nil {
			if calls := event.Message.ToolCalls(); (event.Type == EventTypeToolCall || event.Type == EventTypeAssistant) && len(calls) > 0 {
				event = CloneEvent(event)
				event.Tool = &EventTool{ID: calls[0].ID, Name: calls[0].Name}
				event.Type = EventTypeToolCall
			}
			if results := event.Message.ToolResults(); event.Type == EventTypeToolResult && len(results) == 1 {
				event = CloneEvent(event)
				event.Tool = &EventTool{ID: results[0].ToolUseID, Name: results[0].Name}
			}
		}
		out = append(out, event)
	}
	return out
}
