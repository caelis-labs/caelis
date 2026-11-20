package file

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func replaceRetiredEventLog(t *testing.T, s *Store, active session.Session, raw []byte) {
	t.Helper()
	path, err := s.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(eventLogPath(path), append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
func TestRetiredCodecFileReaderRejectsFutureNestedSchema(t *testing.T) {
	for _, source := range []int{0, 1, 2, 3} {
		for _, journal := range []int{0, 1} {
			for _, kind := range []string{"execution", "tool_execution", "pause_token"} {
				t.Run(fmt.Sprintf("event=%d/journal=%d/%s", source, journal, kind), func(t *testing.T) {
					s, active := newEventPageIndexFixture(t, 0)
					event, err := s.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal}})
					if err != nil {
						t.Fatal(err)
					}
					raw, err := json.Marshal(event)
					if err != nil {
						t.Fatal(err)
					}
					var object map[string]any
					if err = json.Unmarshal(raw, &object); err != nil {
						t.Fatal(err)
					}
					object["schema"] = source
					object["journal"] = map[string]any{"schema": journal, kind: map[string]any{"schema": 999}}
					raw, err = json.Marshal(object)
					if err != nil {
						t.Fatal(err)
					}
					replaceRetiredEventLog(t, s, active, raw)
					if _, err = NewStore(Config{RootDir: s.rootDir}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true}); err == nil {
						t.Fatal("FileStore accepted future nested schema")
					}
				})
			}
		}
	}
}
func TestRetiredCodecLegacyJournalOnlyMediaFileReopen(t *testing.T) {
	s, active := newEventPageIndexFixture(t, 0)
	image := tool.Image{Data: []byte(strings.Repeat("abc", 4096)), MIMEType: "image/png", Name: "legacy-only"}
	full := tool.Result{Content: []tool.ContentBlock{image, image, tool.StructuredContent{Value: json.RawMessage(`{"n":9007199254740993123456789}`)}}}
	event, err := s.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Tool: &session.EventTool{ID: "legacy", Name: "Read", Result: &full}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := session.MarshalEventJSON(event)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	if len(object["media"]) == 0 {
		t.Fatal("fixture did not pack shared journal-only media")
	}
	var typed map[string]json.RawMessage
	if err = json.Unmarshal(object["tool"], &typed); err != nil {
		t.Fatal(err)
	}
	delete(object, "tool")
	object["schema"] = json.RawMessage(`2`)
	// Reconstruct the historical result wire shape inside the retired journal.
	var result struct {
		Content []struct {
			MediaRef string `json:"media_ref"`
		} `json:"content"`
	}
	if err := json.Unmarshal(typed["result"], &result); err != nil {
		t.Fatal(err)
	}
	const historicalImage = `{"kind":"media","media":{"modality":"image","source":{"kind":"event_media","data":%q},"mime_type":"image/png","name":"legacy-only"}}`
	legacyResult := json.RawMessage(fmt.Sprintf(`{"content":[`+historicalImage+`,`+historicalImage+`,{"kind":"json","json":{"value":{"n":9007199254740993123456789}}}],"structured_content":{"value":{"n":9007199254740993123456789},"content_index":2}}`, result.Content[0].MediaRef, result.Content[1].MediaRef))
	object["journal"], err = json.Marshal(map[string]any{"schema": 1, "kind": "tool_execution", "tool_execution": map[string]any{"schema": 1, "key": map[string]any{"tool_call_id": "legacy"}, "tool_name": "Read", "status": "succeeded", "result": legacyResult}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	replaceRetiredEventLog(t, s, active, raw)
	cold := NewStore(Config{RootDir: s.rootDir})
	loaded, err := cold.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 1 || loaded.Events[0].Tool == nil || !reflect.DeepEqual(loaded.Events[0].Tool.Result, &full) {
		t.Fatal("legacy journal-only complete media/result changed")
	}
	// Add a new-schema event and reopen without modifying the legacy JSONL row.
	_, err = cold.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeUser, Message: retiredMessage(model.NewTextMessage(model.RoleUser, "new schema"))}})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = NewStore(Config{RootDir: s.rootDir}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Events) != 2 || !reflect.DeepEqual(loaded.Events[0].Tool.Result, &full) {
		t.Fatal("mixed log changed legacy full result")
	}
}
func retiredMessage(m model.Message) *model.Message { return &m }

func TestRetiredCodecHistoricalReviewFileReopen(t *testing.T) {
	s, active := newEventPageIndexFixture(t, 0)
	event, err := s.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err = json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	object["schema"] = 2
	object["journal"] = map[string]any{"schema": 1, "kind": "pause_token", "pause_token": map[string]any{"schema": 1, "token_id": "historical-approval", "session_id": active.SessionID, "run_id": "old-run", "turn_id": "old-turn", "tool_call_id": "original-call", "tool_name": "Write", "revision": 2, "status": "resolved", "outcome": "selected", "option_id": "allow", "approved": true, "review_text": "historical review survives"}}
	raw, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	replaceRetiredEventLog(t, s, active, raw)
	cold := NewStore(Config{RootDir: s.rootDir})
	page, err := cold.EventMetadataPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, Visibility: session.EventPageClientReplay, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || !page.Events[0].ReviewedApproval {
		t.Fatalf("index2 lost historical review: %+v", page)
	}
	loaded, err := NewStore(Config{RootDir: s.rootDir}).LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	review := session.ResolvedApprovalReview(loaded.Events[0])
	if review == nil || review.RequestID != "historical-approval" || review.ToolCallID != "original-call" || review.ReviewText != "historical review survives" {
		t.Fatalf("historical review changed: %+v", review)
	}
}
