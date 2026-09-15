package gatewayapp

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
)

type guardianPagedOnlyStore struct {
	session.Service
	pages int
}

func TestGuardianCompactSourceAddressRetainsOriginalIdentity(t *testing.T) {
	for _, kind := range []session.EventType{session.EventTypeUser, session.EventTypeToolCall, session.EventTypeToolResult} {
		e := guardianSource(17, kind, "Preserve the original ledger.")
		e.SessionID, e.ID = "parent-session-identity", "canonical-event-identity"
		if kind == session.EventTypeToolResult {
			e.Tool.Output = map[string]any{"stdout": "middle evidence fact", "exit_code": 7}
		}
		preview := guardianProjectEvent(e)
		if preview.SessionID != e.SessionID || preview.ID != e.ID || preview.Seq != e.Seq {
			t.Fatalf("%s lost projection identity", kind)
		}
		text := session.EventText(preview)
		if !strings.Contains(text, "seq=17") || strings.Contains(text, e.SessionID) || strings.Contains(text, e.ID) {
			t.Fatalf("%s routine address is missing or repeats full identity", kind)
		}

	}
}

func (s *guardianPagedOnlyStore) Events(context.Context, session.EventsRequest) ([]*session.Event, error) {
	return nil, fmt.Errorf("full history read is forbidden in this test")
}
func (s *guardianPagedOnlyStore) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	s.pages++
	return s.Service.(session.PagedReader).EventsPage(ctx, req)
}
func (s *guardianPagedOnlyStore) EventCheckpoint(ctx context.Context, ref session.SessionRef) (session.EventCheckpoint, error) {
	return s.Service.(session.EventCheckpointReader).EventCheckpoint(ctx, ref)
}

func TestGuardianProjectionRoundTripPreservesCutAcrossApprovalCadence(t *testing.T) {
	root := t.TempDir()
	store := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := store.StartSession(t.Context(), session.StartSessionRequest{AppName: "caelis", UserID: "user", Workspace: session.WorkspaceRef{Key: "w", CWD: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	reader := &guardianPagedOnlyStore{Service: store}
	var live guardianProjection
	for i := 0; i < 130; i++ {
		event := guardianSource(0, session.EventTypeToolResult, "")
		event.ID = ""
		event.Tool.Output = map[string]any{"exit_code": i % 2, "stderr": "synthetic error", "stdout": strings.Repeat("large untrusted output\n", 1024)}
		if i == 0 || i == 129 {
			event = guardianSource(0, session.EventTypeUser, fmt.Sprintf("user constraint %d", i))
			event.ID = ""
		}
		if _, err := store.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: event}); err != nil {
			t.Fatal(err)
		}
		if i%7 == 0 {
			if _, _, err := live.read(t.Context(), reader, active.SessionRef); err != nil {
				t.Fatal(err)
			}
		}
	}
	a, cut, err := live.read(t.Context(), reader, active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	pages := reader.pages
	if _, _, err := live.read(t.Context(), reader, active.SessionRef); err != nil || reader.pages != pages {
		t.Fatalf("unchanged source rescanned pages: %d -> %d (%v)", pages, reader.pages, err)
	}
	var rebuilt guardianProjection
	reopened := &guardianPagedOnlyStore{Service: sessionfile.NewStore(sessionfile.Config{RootDir: root})}
	b, restoredCut, err := rebuilt.read(t.Context(), reopened, active.SessionRef)
	if err != nil || cut != restoredCut || !reflect.DeepEqual(guardianModelRequest(a, "next action", nil), guardianModelRequest(b, "next action", nil)) {
		t.Fatalf("model context changed after reopening source: cut=%+v/%+v err=%v", cut, restoredCut, err)
	}
	text := guardianEventsText(a)
	if !strings.Contains(text, "user constraint 0") || !strings.Contains(text, "user constraint 129") || len(text) > 16*1024*1024 {
		t.Fatalf("retained context lost user boundaries or grew unbounded: %d", len(text))
	}

}

func TestGuardianUserRetentionPreservesOriginalAndLatestConstraints(t *testing.T) {
	var events []*session.Event
	for i := 0; i < 40; i++ {
		events = append(events, guardianProjectEvent(guardianSource(uint64(i+1), session.EventTypeUser, fmt.Sprintf("constraint-%02d %s", i, strings.Repeat("x", 200)))))
	}
	retained := guardianTrimUsers(events, 2000)
	text := guardianEventsText(retained)
	if !strings.Contains(text, "constraint-00") || !strings.Contains(text, "constraint-39") || len(text) > 2100 {
		t.Fatal("retention lost original/latest user constraints or exceeded budget")
	}
}

func TestGuardianOversizedExactActionDoesNotReachProvider(t *testing.T) {
	service, active := newApprovalReviewerTestSession(t, t.Context())
	reviewer := newGuardianApprovalApprover(service)
	defer reviewer.Close()
	llm := &approvalReviewerFakeModel{}
	req := approvalReviewerTestRequest(active, llm, "inspect", map[string]any{"command": strings.Repeat("x", guardianMaxActionBytes+1)})
	decision, err := reviewer.Decide(t.Context(), req)
	if err == nil || decision.Approved || len(llm.Requests()) != 0 {
		t.Fatalf("oversized exact action reached decision provider: %+v %v", decision, err)
	}
}

func TestGuardianEvidenceFoldingPreservesUTF8AndBoundaries(t *testing.T) {
	short := strings.Repeat("证据", 300)
	if guardianFold(short, 2048) != short {
		t.Fatal("folded a fitting Unicode value")
	}
	large := "first marker " + strings.Repeat("证据", 1000000) + " last marker"
	folded := guardianFold(large, 2048)
	if !utf8.ValidString(folded) || !strings.HasPrefix(folded, "first marker") || !strings.HasSuffix(folded, "last marker") || len(folded) > 2200 || !strings.Contains(folded, "folded") {
		t.Fatal("folding lost boundaries, Unicode validity or output bound")
	}
}
