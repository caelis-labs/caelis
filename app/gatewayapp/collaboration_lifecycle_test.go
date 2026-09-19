package gatewayapp

import (
	"context"
	"database/sql"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/collaboration"
)

// collaborationLifecycleSessionTables are the Control-owned tables the
// collaboration service keeps per Session.
var collaborationLifecycleSessionTables = []string{
	"collaboration_mailbox", "collaboration_messages", "collaboration_readers",
	"collaboration_seen", "collaboration_retention", "collaboration_setups",
}

// TestCloseSessionReleasesRetainedCollaborationState pins that a committed
// Session close releases that Session's retained collaboration rows before the
// close returns, rather than leaving them for a later periodic sweep. A drained
// mailbox can still retain shared history, a reader position, a seen row, a
// retention marker and a controller setup marker; the release must also leave
// canonical Session history and every other Session's rows alone.
func TestCloseSessionReleasesRetainedCollaborationState(t *testing.T) {
	ctx := context.Background()
	provider := newHostedChildInputTestProvider(t, false)
	host := newHostedChildInputTestStack(t, provider)
	parent, child, _ := newHostedChildInputTestTopology(t, host, "collab-close")

	// Canonical history that closure must not disturb.
	appendGatewayAppEvent(t, host, parent.SessionRef, gatewayAppUserEvent("retain canonical history"))
	appendGatewayAppEvent(t, host, parent.SessionRef, gatewayAppAssistantEvent("retained reply"))
	beforeHistory := collaborationLifecycleHistory(t, host, parent.SessionRef)
	if len(beforeHistory) == 0 {
		t.Fatal("fixture has no canonical history to protect")
	}

	dbPath := filepath.Join(host.composition.authorities.storeDir, "control", "control.sqlite")
	collaborationLifecycleSeedRetained(t, ctx, dbPath, parent.SessionID)
	collaborationLifecycleSeedRetained(t, ctx, dbPath, child.SessionID)
	collaborationLifecycleSetupMarker(t, ctx, host, parent.SessionID)

	before := collaborationLifecycleRowCounts(t, dbPath, parent.SessionID)
	if before["collaboration_mailbox"] != 0 {
		t.Fatalf("fixture mailbox rows = %d, want a drained mailbox", before["collaboration_mailbox"])
	}
	for _, table := range []string{"collaboration_messages", "collaboration_readers", "collaboration_seen", "collaboration_retention", "collaboration_setups"} {
		if before[table] == 0 {
			t.Fatalf("fixture %s rows = 0, want retained state", table)
		}
	}
	beforeSibling := collaborationLifecycleRowCounts(t, dbPath, child.SessionID)

	closed, err := host.ControlClient().CloseSession(ctx, appserver.Principal{ID: host.composition.authorities.userID}, appserver.CloseSessionRequest{
		WriteBase: appserver.WriteBase{OperationID: "close-retained-collaboration", SessionID: parent.SessionID},
	})
	if err != nil || closed.Outcome != appserver.OutcomeCommitted {
		t.Fatalf("CloseSession() = %#v, %v", closed, err)
	}

	if after := collaborationLifecycleRowCounts(t, dbPath, parent.SessionID); !maps.Equal(after, collaborationLifecycleZeroCounts()) {
		t.Errorf("closed Session collaboration rows = %v, want every table empty", after)
	}
	if afterSibling := collaborationLifecycleRowCounts(t, dbPath, child.SessionID); !maps.Equal(afterSibling, beforeSibling) {
		t.Errorf("open sibling Session collaboration rows changed: before=%v after=%v", beforeSibling, afterSibling)
	}
	if afterHistory := collaborationLifecycleHistory(t, host, parent.SessionRef); !slices.Equal(afterHistory, beforeHistory) {
		t.Errorf("canonical history changed across close: before=%v after=%v", beforeHistory, afterHistory)
	}
}

func collaborationLifecycleZeroCounts() map[string]int {
	counts := make(map[string]int, len(collaborationLifecycleSessionTables))
	for _, table := range collaborationLifecycleSessionTables {
		counts[table] = 0
	}
	return counts
}

// collaborationLifecycleSeedRetained writes the retained state for one open
// Session. Rows the production service only produces alongside mailbox mail are
// written directly: the fixture must not enqueue mail, which the Host's own
// delivery worker and automatic cleanup could otherwise consume.
func collaborationLifecycleSeedRetained(t *testing.T, ctx context.Context, dbPath, sessionID string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	body := fmt.Sprintf(`{"id":"%s-message","from":"parent","to":"orbit","message":"handoff"}`, sessionID)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO collaboration_messages(session,id,body) VALUES(?,?,?)`, []any{sessionID, sessionID + "-message", body}},
		{`INSERT INTO collaboration_readers(session,instance,cursor) VALUES(?,?,0)`, []any{sessionID, sessionID + "\x00parent-thread"}},
		{`INSERT INTO collaboration_seen(session,instance,id) VALUES(?,?,?)`, []any{sessionID, sessionID + "\x00parent-thread", sessionID + "-message"}},
		{`INSERT INTO collaboration_retention(session,trimmed) VALUES(?,0)`, []any{sessionID}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed %q: %v", statement.query, err)
		}
	}
}

// collaborationLifecycleSetupMarker records a controller setup marker through
// the Host's own collaboration service; Prepare and Bind need no discovery.
func collaborationLifecycleSetupMarker(t *testing.T, ctx context.Context, host *Stack, sessionID string) {
	t.Helper()
	grant := host.CollaborationService().Prepare(collaboration.Identity{Session: sessionID, Member: "parent"}, "parent-thread")
	if err := grant.Bind("remote-" + sessionID); err != nil {
		t.Fatalf("Bind(controller grant) error = %v", err)
	}
	prompt, err := grant.PreparePrompt(ctx)
	if err != nil {
		t.Fatalf("PreparePrompt() error = %v", err)
	}
	if strings.TrimSpace(prompt) == "" {
		t.Fatal("controller setup marker was not recorded")
	}
}

func collaborationLifecycleRowCounts(t *testing.T, dbPath, sessionID string) map[string]int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	counts := make(map[string]int, len(collaborationLifecycleSessionTables))
	for _, table := range collaborationLifecycleSessionTables {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE session=?", sessionID).Scan(&count); err != nil {
			t.Fatalf("count %s rows: %v", table, err)
		}
		counts[table] = count
	}
	return counts
}

// collaborationLifecycleHistory reduces canonical history to one signature per
// event so closure can be compared against an exact event log.
func collaborationLifecycleHistory(t *testing.T, host *Stack, ref session.SessionRef) []string {
	t.Helper()
	events, err := host.composition.sessions.Events(context.Background(), session.EventsRequest{SessionRef: ref})
	if err != nil {
		t.Fatalf("Events(%s) error = %v", ref.SessionID, err)
	}
	history := make([]string, 0, len(events))
	for _, event := range events {
		if event == nil {
			history = append(history, "<nil>")
			continue
		}
		history = append(history, fmt.Sprintf("%d|%s|%s", event.Seq, event.Type, event.Text))
	}
	return history
}
