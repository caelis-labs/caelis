package gatewayapp_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestBackgroundParticipantRejectsInvalidTaskLabelBeforeRuntime(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "native"
		if configured {
			name = "profile"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			root := t.TempDir()
			provider := &sessionProfileProvider{}
			host := startApplicationHTTPHostWithSavedModels(t, filepath.Join(root, "store"), root, provider)
			defer host.close(t)
			request := appserver.CreateSessionRequest{WriteBase: appserver.WriteBase{OperationID: "parent"}, Model: "openai/gpt-5.4-mini"}
			if configured {
				request.Model = ""
				request.Profile = &sessionprofile.Profile{Version: "test/1", Model: "openai/gpt-5.4-mini", ToolsVersion: "empty/1", Execution: "tools-only"}
			}
			created, err := host.host.CreateSession(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			before, err := host.host.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
			if err != nil {
				t.Fatal(err)
			}
			for i, label := range []string{"Read-only plugin child", "parent", strings.Repeat("x", 33)} {
				op := []string{"human-label", "reserved-label", "long-label"}[i]
				start := appserver.StartParticipantRequest{WriteBase: appserver.WriteBase{SessionID: created.SessionID, OperationID: op, ExpectedRevision: &before.Revision, ExpectedControllerEpoch: before.Controller.EpochID}, Background: true, Handle: "self", Label: label, Input: "Return a short answer."}
				for attempt := 0; attempt < 2; attempt++ {
					result, err := host.host.StartParticipant(ctx, start)
					if err == nil || result.Outcome != appserver.OutcomeRejected || result.ErrorCode != errorcode.InvalidArgument || result.Resource != nil {
						t.Fatalf("invalid background label attempt %d: outcome=%s code=%s resource=%v error=%v", attempt, result.Outcome, result.ErrorCode, result.Resource, err)
					}
				}
				receipt, err := host.host.Operation(ctx, op)
				if err != nil || receipt.Outcome != appserver.OutcomeRejected || receipt.ErrorCode != errorcode.InvalidArgument {
					t.Fatalf("rejected receipt: %+v %v", receipt, err)
				}
			}
			after, err := host.host.InspectSession(ctx, appserver.StateRequest{SessionID: created.SessionID})
			if err != nil || after.Revision != before.Revision || len(after.Participants) != 0 || provider.requests.Load() != 0 {
				t.Fatalf("invalid label changed Session or invoked provider: revision=%d/%d participants=%d provider_calls=%d error=%v", before.Revision, after.Revision, len(after.Participants), provider.requests.Load(), err)
			}
			db, err := sql.Open("sqlite", "file:"+filepath.Join(root, "store", "sessions", ".sessions.index.sqlite")+"?mode=ro")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var tasks int
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM tasks WHERE session_id = ?", created.SessionID).Scan(&tasks); err != nil || tasks != 0 {
				t.Fatalf("invalid label created Tasks: count=%d error=%v", tasks, err)
			}
		})
	}
}
