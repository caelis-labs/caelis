package acpbridge

import (
	"iter"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestSourceEventFromAgentAdaptsNativeACPEnvelope(t *testing.T) {
	t.Parallel()

	envelope := &eventstream.Envelope{
		Kind: eventstream.KindSessionUpdate,
		Update: schema.RawUpdate{
			SessionUpdate: "vendor/custom",
			Raw:           []byte(`{"sessionUpdate":"vendor/custom"}`),
		},
	}
	canonical := &session.Event{ID: "e1", Type: session.EventTypeAssistant}
	adapted := SourceEventFromAgent(agent.SourceEvent{
		Canonical: canonical,
		Native:    envelope,
	})
	if adapted.Canonical == nil || adapted.Canonical.ID != "e1" {
		t.Fatalf("adapted canonical = %#v, want cloned assistant event", adapted.Canonical)
	}
	if adapted.Canonical == canonical {
		t.Fatal("adapted canonical should be cloned")
	}
	if adapted.ACP == nil || adapted.ACP == envelope {
		t.Fatalf("adapted ACP = %#v, want cloned envelope", adapted.ACP)
	}
	if update, ok := adapted.ACP.Update.(schema.RawUpdate); !ok || update.SessionUpdate != "vendor/custom" {
		t.Fatalf("adapted ACP update = %#v, want vendor/custom passthrough", adapted.ACP.Update)
	}
}

func TestSourceStreamFromRecognizesAgentSourceHandle(t *testing.T) {
	t.Parallel()

	handle := agentSourceHandle{events: []agent.SourceEvent{{
		Canonical: &session.Event{ID: "e1", Type: session.EventTypeAssistant},
		Native: &eventstream.Envelope{
			Kind:   eventstream.KindNotice,
			Notice: "native",
		},
	}}}
	stream := SourceStreamFrom(handle)
	if !stream.NativeACP {
		t.Fatal("SourceStreamFrom() NativeACP = false, want true for agent.SourceHandle")
	}

	var events []SourceEvent
	for event, err := range stream.Events {
		if err != nil {
			t.Fatalf("source events error = %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 {
		t.Fatalf("source events = %#v, want one adapted event", events)
	}
	if events[0].Canonical == nil || events[0].Canonical.ID != "e1" {
		t.Fatalf("canonical event = %#v, want assistant e1", events[0].Canonical)
	}
	if events[0].ACP == nil || events[0].ACP.Notice != "native" {
		t.Fatalf("ACP event = %#v, want native notice passthrough", events[0].ACP)
	}
}

func TestSourceStreamFromRecognizesLegacySourceHandle(t *testing.T) {
	t.Parallel()

	handle := legacySourceHandle{events: []SourceEvent{{
		Canonical: &session.Event{ID: "legacy", Type: session.EventTypeAssistant},
		ACP: &eventstream.Envelope{
			Kind:   eventstream.KindNotice,
			Notice: "legacy",
		},
	}}}
	stream := SourceStreamFrom(handle)
	if !stream.NativeACP {
		t.Fatal("SourceStreamFrom() NativeACP = false, want true for legacy SourceHandle")
	}

	var events []SourceEvent
	for event, err := range stream.Events {
		if err != nil {
			t.Fatalf("source events error = %v", err)
		}
		events = append(events, event)
	}
	if len(events) != 1 || events[0].Canonical == nil || events[0].Canonical.ID != "legacy" {
		t.Fatalf("legacy source events = %#v, want one legacy canonical event", events)
	}
	if events[0].ACP == nil || events[0].ACP.Notice != "legacy" {
		t.Fatalf("legacy ACP event = %#v, want notice passthrough", events[0].ACP)
	}
}

type agentSourceHandle struct {
	events []agent.SourceEvent
}

func (h agentSourceHandle) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range h.events {
			if !yield(event.Canonical, nil) {
				return
			}
		}
	}
}

func (h agentSourceHandle) SourceEvents() iter.Seq2[agent.SourceEvent, error] {
	return func(yield func(agent.SourceEvent, error) bool) {
		for _, event := range h.events {
			if !yield(agent.CloneSourceEvent(event), nil) {
				return
			}
		}
	}
}

type legacySourceHandle struct {
	events []SourceEvent
}

func (h legacySourceHandle) Events() iter.Seq2[*session.Event, error] {
	return func(yield func(*session.Event, error) bool) {
		for _, event := range h.events {
			if !yield(event.Canonical, nil) {
				return
			}
		}
	}
}

func (h legacySourceHandle) SourceEvents() iter.Seq2[SourceEvent, error] {
	return func(yield func(SourceEvent, error) bool) {
		for _, event := range h.events {
			if !yield(CloneSourceEvent(event), nil) {
				return
			}
		}
	}
}
