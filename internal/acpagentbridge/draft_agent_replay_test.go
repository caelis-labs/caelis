package acpagentbridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caelis-labs/acp-go-sdk/experimental/v2"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/projection"
)

// Uses the PR's physical Session store and production FeedBroker fixture. The
// model emits ordinary UI-only chunks, persists its complete canonical answer,
// then allows FeedBroker's production catch-up to suppress duplicate content.
func TestDraftResumePreservesExactSpoolNarrative(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	f := newDraftNativeFixture(t)
	var wire *draftReplayWire
	connect := func() (*v2.ClientSideConnection, *DraftAgent, *draftNativePeer) {
		wire = &draftReplayWire{}
		return connectDraftNative(t, f, func(conn net.Conn) net.Conn { wire.Conn = conn; return wire })
	}
	client, agent, peer := connect()
	cwd := t.TempDir()
	created, err := client.NewSession(ctx, v2.NewSessionRequest{Cwd: v2.AbsolutePath(cwd)})
	if err != nil {
		t.Fatal(err)
	}
	peer.mu.Lock()
	peer.updates = nil
	peer.mu.Unlock()
	wire.take(t) // Discard initial idle/config before the actual live trace.
	id := string(created.SessionId)
	done := make(chan error, 1)
	go func() { _, err := client.Prompt(ctx, draftNativePrompt(id, "say hello")); done <- err }()
	var input draftNativeInput
	select {
	case input = <-f.inserts:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	f.insert(input)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	ref, feed := f.refs[id], f.feeds[id]
	f.mu.Unlock()
	const messageID = "assistant-answer"
	message := model.NewTextMessage(model.RoleAssistant, "Hello world")
	final := &session.Event{Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical, MessageID: messageID,
		Text: "Hello world", Message: &message, Scope: &session.EventScope{TurnID: input.target.TurnID},
		Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{
			SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: messageID, Content: session.ProtocolTextContent("Hello world"),
		}},
	}
	for _, text := range []string{"Hello ", "world"} {
		live := session.CloneEvent(final)
		live.Visibility = session.VisibilityUIOnly
		live.Text = text
		part := model.NewTextMessage(model.RoleAssistant, text)
		live.Message = &part
		live.Protocol.Update.Content = session.ProtocolTextContent(text)
		base := projection.EnvelopeBaseFromSessionEvent(ref, live, projection.SessionEventTransport{HandleID: input.target.HandleID, RunID: input.target.RunID, TurnID: input.target.TurnID})
		for _, env := range projection.ProjectSessionEventEnvelope(base, live) {
			if err := feed.Publish(env); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := f.store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, Event: final}); err != nil {
		t.Fatal(err)
	}
	// A second identity and durable display-only patches exercise exact and
	// canonical replay without changing the admitted model messages.
	secondMessage := model.NewTextMessage(model.RoleAssistant, "Second answer")
	second := session.CloneEvent(final)
	second.MessageID, second.Text, second.Message = "assistant-second", "Second answer", &secondMessage
	second.Protocol.Update.MessageID = second.MessageID
	second.Protocol.Update.Content = session.ProtocolTextContent(second.Text)
	live := session.CloneEvent(second)
	live.Visibility = session.VisibilityUIOnly
	base := projection.EnvelopeBaseFromSessionEvent(ref, live, projection.SessionEventTransport{HandleID: input.target.HandleID, RunID: input.target.RunID, TurnID: input.target.TurnID})
	for _, env := range projection.ProjectSessionEventEnvelope(base, live) {
		if err := feed.Publish(env); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, Event: second}); err != nil {
		t.Fatal(err)
	}
	patch := func(messageID string, replacement bool, meta map[string]any) {
		t.Helper()
		event := &session.Event{Type: session.EventTypeAssistant, Visibility: session.VisibilityMirror, MessageID: messageID,
			Scope: &session.EventScope{TurnID: input.target.TurnID}, Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate,
				Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), MessageID: messageID,
					Content: session.ProtocolTextContent(""), Replacement: replacement, ReplacedFields: []string{"_meta"}, Meta: meta}}}
		stored, err := f.store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, Event: event})
		if err != nil {
			t.Fatal(err)
		}
		base := projection.EnvelopeBaseFromSessionEvent(ref, stored, projection.SessionEventTransport{HandleID: input.target.HandleID, RunID: input.target.RunID, TurnID: input.target.TurnID})
		for _, env := range projection.ProjectSessionEventEnvelope(base, stored) {
			if err := feed.Publish(env); err != nil {
				t.Fatal(err)
			}
		}
	}
	patch(messageID, false, map[string]any{"phase": "reviewed"})
	patch(messageID, false, nil)
	patch(second.MessageID, true, nil)
	f.finish(id, false) // Production Prime reconciles and suppresses canonical duplicates.
	waitIdle := func() {
		t.Helper()
		for {
			peer.mu.Lock()
			idle := false
			for _, n := range peer.updates {
				if n.SessionId == created.SessionId && n.Update.StateUpdate != nil && n.Update.StateUpdate.StateUpdate.Idle != nil {
					idle = true
				}
			}
			peer.mu.Unlock()
			if idle {
				return
			}
			select {
			case <-peer.changed:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
	}
	waitIdle()
	loaded, err := f.store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	before := compact.PromptEventsFromLatestCompact(loaded.Events)
	var modelText []string
	for _, event := range before {
		if message, ok := session.ModelMessageOf(event); ok && message.Role == model.RoleAssistant {
			modelText = append(modelText, message.TextContent())
		}
	}
	if !reflect.DeepEqual(modelText, []string{"Hello world", "Second answer"}) {
		t.Fatalf("model context=%v", modelText)
	}

	transcript := newDraftReplayTranscript()
	check := func(label string, transcript *draftReplayTranscript, peer *draftNativePeer, exact bool) {
		t.Helper()
		updates := wire.take(t)
		transcript.apply(t, updates)
		if transcript.text[messageID] != "Hello world" || transcript.text[second.MessageID] != "" {
			t.Fatalf("%s rendered=%v", label, transcript.text)
		}
		if len(transcript.meta[messageID]) != 0 || len(transcript.meta[second.MessageID]) != 0 {
			t.Fatalf("%s metadata=%v", label, transcript.meta)
		}
		var deltas []string
		var metadataPatch, metadataClear, bodyClear bool
		for _, update := range updates {
			var fields map[string]json.RawMessage
			if err := draftJSONConvert(update.Update, &fields); err != nil {
				t.Fatal(err)
			}
			var kind, id string
			_ = json.Unmarshal(fields["sessionUpdate"], &kind)
			_ = json.Unmarshal(fields["messageId"], &id)
			if id == messageID && kind == "agent_message_chunk" {
				deltas = append(deltas, draftReplayBlockText(t, fields["content"]))
			}
			if id == messageID && kind == "agent_message" && len(fields["content"]) == 0 {
				metadataPatch = metadataPatch || strings.Contains(string(fields["_meta"]), "reviewed")
				metadataClear = metadataClear || string(fields["_meta"]) == "null"
			}
			bodyClear = bodyClear || id == second.MessageID && kind == "agent_message" && len(fields["content"]) > 0
			if _, leaked := fields["replacedFields"]; leaked {
				t.Fatal("native metadata presence leaked into standard draft")
			}
		}
		if exact && !reflect.DeepEqual(deltas, []string{"Hello ", "world"}) {
			t.Fatalf("%s exact spool deltas=%v", label, deltas)
		}
		if !metadataPatch || !metadataClear || !bodyClear {
			t.Fatalf("%s patch=%v metaClear=%v bodyClear=%v", label, metadataPatch, metadataClear, bodyClear)
		}
	}
	check("live", transcript, peer, true)
	resume := v2.ResumeSessionRequest{SessionId: created.SessionId, Cwd: v2.AbsolutePath(cwd), ReplayFrom: &v2.ReplayFrom{Start: &v2.ReplayFromStartInline{Type: "start"}}}
	for _, label := range []string{"same client resume", "same client repeat resume"} {
		if _, err := client.ResumeSession(ctx, resume); err != nil {
			t.Fatal(err)
		}
		check(label, transcript, peer, true)
	}
	_ = client.Close()
	_ = agent.Close()
	client, agent, peer = connect()
	if _, err := client.ResumeSession(ctx, resume); err != nil {
		t.Fatal(err)
	}
	check("new client exact history", newDraftReplayTranscript(), peer, true)
	_ = client.Close()
	_ = agent.Close()
	_ = feed.Close()
	codec, err := appserver.NewCursorCodec(appserver.CursorCodecConfig{Secret: make([]byte, 32)})
	if err != nil {
		t.Fatal(err)
	}
	// A fresh broker without a spool forces the production canonical replacement
	// path rather than accidentally replaying a warm trace a second time.
	fallback, err := appserver.NewFeedBroker(appserver.FeedBrokerConfig{SessionRef: ref, Reader: f.store, CursorCodec: codec})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.feeds[id] = fallback
	f.mu.Unlock()
	client, _, peer = connect()
	if _, err := client.ResumeSession(ctx, resume); err != nil {
		t.Fatal(err)
	}
	check("canonical fallback", newDraftReplayTranscript(), peer, false)
	loaded, err = f.store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: ref})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, compact.PromptEventsFromLatestCompact(loaded.Events)) {
		t.Fatal("replay changed native model context")
	}
}

type draftReplayTranscript struct {
	text map[string]string
	meta map[string]map[string]json.RawMessage
}

func newDraftReplayTranscript() *draftReplayTranscript {
	return &draftReplayTranscript{text: make(map[string]string), meta: make(map[string]map[string]json.RawMessage)}
}

// Apply the standard full-message patch and exact chunk semantics received by
// an ordinary peer, retaining its existing transcript across repeated Resume.
func (r *draftReplayTranscript) apply(t *testing.T, updates []v2.UpdateSessionNotification) {
	t.Helper()
	for _, update := range updates {
		var fields map[string]json.RawMessage
		if err := draftJSONConvert(update.Update, &fields); err != nil {
			t.Fatal(err)
		}
		var kind, id string
		_ = json.Unmarshal(fields["sessionUpdate"], &kind)
		_ = json.Unmarshal(fields["messageId"], &id)
		if id == "" || !strings.HasPrefix(kind, "agent_message") {
			continue
		}
		if meta, present := fields["_meta"]; present {
			var value map[string]json.RawMessage
			if err := json.Unmarshal(meta, &value); err != nil {
				t.Fatal(err)
			}
			r.meta[id] = value
		}
		content, present := fields["content"]
		if !present {
			continue
		}
		if strings.HasSuffix(kind, "_chunk") {
			r.text[id] += draftReplayBlockText(t, content)
			continue
		}
		r.text[id] = ""
		var blocks []json.RawMessage
		if err := json.Unmarshal(content, &blocks); err != nil {
			t.Fatal(err)
		}
		for _, block := range blocks {
			r.text[id] += draftReplayBlockText(t, block)
		}
	}
}

func draftReplayBlockText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &block); err != nil {
		t.Fatal(err)
	}
	if block.Type == "text" {
		return block.Text
	}
	return ""
}

// The SDK's typed marshaler cannot retain absent/null/empty distinctions.
// Observe the actual bytes entering its client decoder without altering them.
type draftReplayWire struct {
	net.Conn
	mu      sync.Mutex
	pending []byte
	updates []v2.UpdateSessionNotification
	err     error
}

func (w *draftReplayWire) Read(data []byte) (int, error) {
	n, err := w.Conn.Read(data)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending = append(w.pending, data[:n]...)
	for {
		end := bytes.IndexByte(w.pending, '\n')
		if end < 0 {
			break
		}
		var frame struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if decodeErr := json.Unmarshal(w.pending[:end], &frame); decodeErr != nil {
			w.err = decodeErr
		} else if frame.Method == v2.ClientMethodSessionUpdate {
			var params struct {
				SessionID v2.SessionId    `json:"sessionId"`
				Update    json.RawMessage `json:"update"`
			}
			if decodeErr := json.Unmarshal(frame.Params, &params); decodeErr != nil {
				w.err = decodeErr
			} else {
				raw := append(json.RawMessage(nil), params.Update...)
				w.updates = append(w.updates, v2.UpdateSessionNotification{SessionId: params.SessionID, Update: v2.SessionUpdate{Other: &raw}})
			}
		}
		w.pending = w.pending[end+1:]
	}
	return n, err
}

func (w *draftReplayWire) take(t *testing.T) []v2.UpdateSessionNotification {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		t.Fatal(w.err)
	}
	updates := w.updates
	w.updates = nil
	return updates
}
