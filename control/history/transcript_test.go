package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func displayEvent(turn, kind, text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return &session.Event{Type: session.EventTypeAssistant, Message: &message,
		Scope: &session.EventScope{TurnID: turn}, Protocol: &session.EventProtocol{Update: &session.ProtocolUpdate{SessionUpdate: kind, Content: session.ProtocolTextContent(text)}}}
}

func TestTranscriptRetainsDialogueAndMailWithoutOldDetails(t *testing.T) {
	var window Transcript
	for n := range 4 {
		turn := fmt.Sprint(n)
		for _, kind := range []string{"user_message_chunk", "agent_thought_chunk", "tool_call", "agent_message_chunk"} {
			e := displayEvent(turn, kind, kind)
			if kind == "user_message_chunk" {
				e.Actor = session.ParentCommunicationActor()
			}
			before, _ := json.Marshal(e)
			window.Append(e)
			after, _ := json.Marshal(e)
			if string(before) != string(after) {
				t.Fatal("display projection mutated its source")
			}
		}
	}
	if len(window.Events()) != 12 || !window.Trimmed() {
		t.Fatalf("window size=%d", len(window.Events()))
	}
	for _, e := range window.Events() {
		if transcriptTurn(e) < "2" && !transcriptDialogue(e) {
			t.Fatal("old details survived")
		}
		if session.ProtocolUpdateOf(e).SessionUpdate == "user_message_chunk" && !reflect.DeepEqual(e.Actor, session.ParentCommunicationActor()) {
			t.Fatal("mail lost sender")
		}
	}
}

func TestTranscriptDoesNotMergeCanonicalFinalIntoLiveDeltas(t *testing.T) {
	var window Transcript
	for _, text := range []string{"hello ", "world"} {
		e := displayEvent("turn", "agent_message_chunk", text)
		e.Visibility, e.MessageID = session.VisibilityUIOnly, "message"
		window.Append(e)
	}
	final := displayEvent("turn", "agent_message_chunk", "hello world")
	final.Visibility, final.MessageID = session.VisibilityCanonical, "message"
	window.Append(final)
	events := window.Events()
	if len(events) != 2 || session.EventText(events[0]) != "hello world" || session.EventText(events[1]) != "hello world" || events[1].Visibility != session.VisibilityCanonical {
		t.Fatal("display coalescing consumed a canonical final boundary")
	}
}

func TestTranscriptBoundsLongTurnAndManyTurns(t *testing.T) {
	var window Transcript
	for n := range 10000 {
		e := displayEvent("long-turn", "tool_call", strings.Repeat("x", 1024))
		window.Append(e)
		if window.bytes > TranscriptBytes || len(window.Events()) > TranscriptEvents {
			t.Fatal("unbounded long Turn")
		}
		if n == 5000 {
			window.Append(displayEvent("long-turn", "tool_call", strings.Repeat("x", 3<<20)))
		}
	}
	for n := range 500 {
		window.Append(displayEvent(fmt.Sprint(n), "agent_message_chunk", "answer"))
	}
	if len(window.turns) > MaxTurns || len(window.Events()) > MaxTurns {
		t.Fatal("unbounded Turn history")
	}
	if session.EventText(window.Events()[len(window.Events())-1]) != "answer" {
		t.Fatal("latest answer lost")
	}
}

// Local incident corpora remain outside Git. Only a private copy is decoded;
// hashes prove that projection did not alter the source durable history.
func TestTranscriptLocalSessionCorpus(t *testing.T) {
	pattern := os.Getenv("CAELIS_HISTORY_CORPUS")
	if pattern == "" {
		t.Skip("set CAELIS_HISTORY_CORPUS to a local .events.jsonl glob")
	}
	paths, err := filepath.Glob(pattern)
	if err != nil || len(paths) == 0 {
		t.Fatalf("corpus selection: %v", err)
	}
	total, bytes := 0, int64(0)
	for _, path := range paths {
		source, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		copy, err := os.CreateTemp(t.TempDir(), "corpus-*.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		before := sha256.New()
		n, err := io.Copy(copy, io.TeeReader(source, before))
		source.Close()
		if err != nil {
			t.Fatal(err)
		}
		bytes += n
		if _, err := copy.Seek(0, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		var window Transcript
		scanner := bufio.NewScanner(copy)
		scanner.Buffer(make([]byte, 64<<10), 32<<20)
		for scanner.Scan() {
			var event session.Event
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			window.Append(&event)
			total++
			if window.bytes > TranscriptBytes {
				t.Fatal("corpus exceeded cache budget")
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		copy.Close()
		source, err = os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		after := sha256.New()
		_, err = io.Copy(after, source)
		source.Close()
		if err != nil || !reflect.DeepEqual(before.Sum(nil), after.Sum(nil)) {
			t.Fatal("durable source changed during corpus validation")
		}
	}
	t.Logf("projected %d files, %d durable events, %d source bytes into bounded windows; source hashes unchanged", len(paths), total, bytes)
}
