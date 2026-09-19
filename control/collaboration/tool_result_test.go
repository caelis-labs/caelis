package collaboration

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sync"
	"testing"
)

func TestToolResultsKeepMailReferencesAndObservationCursors(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "mail.sqlite"), &observationBackend{revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	call := func(member, name, args string) json.RawMessage {
		t.Helper()
		raw, err := s.Call(t.Context(), Identity{"work", member}, Request{Tool: name, Arguments: json.RawMessage(args)})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	var listed []map[string]any
	if err := json.Unmarshal(call("a", "ListThreads", `{}`), &listed); err != nil || len(listed) != 3 {
		t.Fatalf("list = %#v, %v", listed, err)
	}
	for _, thread := range listed {
		if thread["id"] != nil || thread["revision"] != nil || thread["handle"] == nil {
			t.Fatalf("list repeated internal identity: %#v", thread)
		}
	}
	var receipt map[string]any
	if err := json.Unmarshal(call("a", "SendMessage", `{"to":"b","message":"Review complete."}`), &receipt); err != nil || len(receipt) != 2 || receipt["status"] != "queued" {
		t.Fatalf("receipt = %#v, %v", receipt, err)
	}
	var received struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(call("b", "SendMessage", `{"to":"a","message":"Progress update."}`), &received); err != nil || len(received.Messages) != 1 {
		t.Fatalf("received mail = %#v, %v", received, err)
	}
	mail := received.Messages[0]
	if mail["id"] != receipt["id"] || mail["from"] != "a" || mail["message"] != "Review complete." || mail["to"] != nil {
		t.Fatalf("mail = %#v", mail)
	}
	args, _ := json.Marshal(map[string]any{"to": "parent", "message": "Thanks.", "reply_to": receipt["id"]})
	call("b", "SendMessage", string(args))
	var waited struct {
		Reason   string           `json:"reason"`
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(call("parent", "WaitThread", `{"timeout_seconds":0}`), &waited); err != nil || waited.Reason != "message" || len(waited.Messages) != 1 || waited.Messages[0]["reply_to"] != receipt["id"] {
		t.Fatalf("waited mail = %#v, %v", waited, err)
	}
	if raw := call("parent", "WaitThread", `{"timeout_seconds":0}`); string(raw) != `{"reason":"timeout"}` {
		t.Fatalf("mail consumed twice: %s", raw)
	}
	var read map[string]any
	if err := json.Unmarshal(call("parent", "ReadThread", `{"handle":"b"}`), &read); err != nil || read["cursor"] != float64(2) || read["output"] != "public result" || read["thread"] != nil || read["id"] != nil {
		t.Fatalf("read = %#v, %v", read, err)
	}
	if raw := call("parent", "WaitThread", `{"threads":[{"handle":"b","after":2}],"timeout_seconds":0}`); string(raw) != `{"reason":"timeout"}` {
		t.Fatalf("repeated observation = %s", raw)
	}
}

func TestCollaborationToolSetsFollowCallerRole(t *testing.T) {
	s := openTestService(t, &testBackend{})
	for _, controller := range []bool{false, true} {
		var names []string
		for _, definition := range Definitions(controller) {
			names = append(names, definition.Name)
		}
		want := []string{"ReadMessages", "ListThreads", "SendMessage"}
		if controller {
			want = append(want, "ReadThread", "WaitThread")
		}
		if !slices.Equal(names, want) {
			t.Fatalf("controller=%v tools=%v, want %v", controller, names, want)
		}
	}
	for _, member := range []string{"a", "parent"} {
		if _, err := s.Call(t.Context(), Identity{"work", member}, Request{Tool: "ReceiveMessages", Arguments: json.RawMessage(`{}`)}); err == nil {
			t.Fatalf("%s called removed ReceiveMessages", member)
		}
	}

}

func TestSendMessageKeepsSendAndMailboxFailuresIndependent(t *testing.T) {
	s := openTestService(t, &testBackend{})
	// A malformed persisted inbox entry fails the take transaction after send
	// succeeds. The outgoing acknowledgement must remain usable and silent.
	if _, err := s.db.Exec(`INSERT INTO collaboration_mailbox(session,recipient,body) VALUES('work','a','invalid json')`); err != nil {
		t.Fatal(err)
	}
	raw, err := s.Call(t.Context(), Identity{"work", "a"}, Request{Tool: "SendMessage", Arguments: json.RawMessage(`{"to":"b","message":"progress"}`)})
	if err != nil {
		t.Fatal(err)
	}
	var receipt map[string]any
	if err := json.Unmarshal(raw, &receipt); err != nil || len(receipt) != 2 || receipt["status"] != "queued" {
		t.Fatalf("mailbox failure leaked into send acknowledgement: %s, %v", raw, err)
	}
	outgoing, err := s.Receive(t.Context(), Identity{"work", "b"})
	if err != nil || len(outgoing) != 1 || outgoing[0].ID != receipt["id"] {
		t.Fatalf("successful send was lost: %v, %v", outgoing, err)
	}
	var body string
	if err := s.db.QueryRow(`SELECT body FROM collaboration_mailbox WHERE recipient='a'`).Scan(&body); err != nil || body != "invalid json" {
		t.Fatalf("failed take consumed inbox: %q, %v", body, err)
	}
	pending, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "pending", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Call(t.Context(), Identity{"work", "b"}, Request{Tool: "SendMessage", Arguments: json.RawMessage(`{"to":"missing","message":"invalid send"}`)}); err == nil {
		t.Fatal("invalid send succeeded")
	}
	mail, err := s.Receive(t.Context(), Identity{"work", "b"})
	if err != nil || len(mail) != 1 || mail[0] != pending {
		t.Fatalf("failed send consumed inbox: %v, %v", mail, err)
	}
}

func TestSendMessageAndAutomaticDeliveryConsumeEachMailOnce(t *testing.T) {
	b := &batchBackend{}
	s, err := Open(filepath.Join(t.TempDir(), "mail.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	positions := map[string]int{}
	for index := range 65 {
		m, err := s.Send(t.Context(), Identity{"work", []string{"a", "c"}[index%2]}, "b", "incoming", "")
		if err != nil {
			t.Fatal(err)
		}
		positions[m.ID] = index
	}
	start := make(chan struct{})
	replies := make(chan []toolMessage, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() {
			<-start
			raw, err := s.Call(t.Context(), Identity{"work", "b"}, Request{Tool: "SendMessage", Arguments: json.RawMessage(`{"to":"a","message":"progress"}`)})
			if err != nil {
				t.Error(err)
				return
			}
			var reply toolSendResult
			if err := json.Unmarshal(raw, &reply); err != nil {
				t.Error(err)
				return
			}
			replies <- reply.Messages
		})
	}
	workers.Go(func() {
		<-start
		// This backend reports an unknown outcome after recording its batch.
		// A concurrent pull must not recover those already consumed messages.
		_ = s.deliverRecipient(t.Context(), Identity{"work", "b"})
	})
	close(start)
	workers.Wait()
	close(replies)
	seen := map[string]bool{}
	check := func(batch []toolMessage) {
		t.Helper()
		previous := -1
		for _, m := range batch {
			index, exists := positions[m.ID]
			if !exists || seen[m.ID] || index <= previous || m.From != []string{"a", "c"}[index%2] || m.Text != "incoming" {
				t.Fatalf("duplicate, reordered or damaged mail: %#v", m)
			}
			previous, seen[m.ID] = index, true
		}
	}
	for reply := range replies {
		check(reply)
	}
	for _, batch := range b.batches {
		check(messageToolViews(batch))
	}
	if len(seen) != len(positions) {
		t.Fatalf("delivered %d of %d messages", len(seen), len(positions))
	}
	if err := s.deliverRecipient(t.Context(), Identity{"work", "b"}); err != nil {
		t.Fatalf("already consumed mail was retried: %v", err)
	}
	if remaining, err := s.Receive(t.Context(), Identity{"work", "b"}); err != nil || len(remaining) != 0 {
		t.Fatalf("delivered mail remained queued: %v, %v", remaining, err)
	}
}
