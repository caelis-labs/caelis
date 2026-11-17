package session

import "testing"

func TestApprovalReviewReplayExcludesUnreviewedJournalAndModelContext(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status           ApprovalRecordStatus
		review           string
		approved, replay bool
	}{
		{"approved", ApprovalRecordResolved, "approved", true, true},
		{"denied", ApprovalRecordResolved, "denied: outside scope", false, true},
		{"manual", ApprovalRecordResolved, "", true, false},
		{"pending", ApprovalRecordPending, "reviewing", false, false},
		{"cancelled", ApprovalRecordCancelled, "cancelled", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := &Event{Seq: 1, Type: EventTypeLifecycle, Visibility: VisibilityJournal,
				Approval: &ApprovalRecord{
					ToolCallID: "call-1", Status: tc.status, Approved: tc.approved, ReviewText: tc.review,
				},
			}
			if IsClientReplayEvent(event) != tc.replay {
				t.Fatalf("replay = %v, want %v", IsClientReplayEvent(event), tc.replay)
			}
			page := PageEvents([]*Event{event}, EventPageRequest{Visibility: EventPageClientReplay})
			if (len(page.Events) == 1) != tc.replay || page.NextSeq != 1 {
				t.Fatalf("client replay page = %#v", page)
			}
			if IsCanonicalHistoryEvent(event) || IsMainInvocationVisibleEvent(event) {
				t.Fatal("approval journal entered model context")
			}
		})
	}
}
