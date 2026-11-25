package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestStoreCurrentDocumentRoundTripsSealedACPPlacement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	store := NewStore(Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "current-user",
		PreferredSessionID: "current-session",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	sealed, err := placement.Seal(placement.Placement{
		Kind:              placement.KindAgent,
		ProfileID:         "profile/current-acp",
		Agent:             "current-agent",
		Model:             "current-model",
		ReasoningEffort:   "xhigh",
		ConfigFingerprint: "current-config",
	})
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	binding := session.ParticipantBinding{
		ID:                   "current-acp",
		Kind:                 session.ParticipantKindACP,
		Role:                 session.ParticipantRoleSidecar,
		AgentName:            "current-agent",
		Placement:            sealed,
		SessionID:            "remote-current",
		AttachmentGeneration: "generation-1",
	}
	if _, err := store.PutParticipant(ctx, session.PutParticipantRequest{
		SessionRef: active.SessionRef,
		Binding:    binding,
	}); err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}

	loaded, err := NewStore(Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	if got, want := loaded.Session.Participants, []session.ParticipantBinding{session.CloneParticipantBinding(binding)}; !reflect.DeepEqual(got, want) {
		t.Fatalf("round-tripped participants = %#v, want %#v", got, want)
	}

	path, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatalf("resolveWritePath() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		t.Fatalf("Unmarshal(header) error = %v", err)
	}
	if header.Version != documentVersion {
		t.Fatalf("current document version = %d, want %d", header.Version, documentVersion)
	}
}

func TestStoreUnreadableSessionLeavesOriginalBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		data   string
		log    bool
		legacy bool
	}{
		{"v1 document", `{"kind":"caelis.sdk.session","version":1,"session":{"session_id":"old"}}`, false, true},
		{"v1 retired field shape", `{"state":[],"kind":"caelis.sdk.session","version":1}`, false, true},
		{"future document", `{"kind":"caelis.sdk.session","version":3}`, false, false},
		{"malformed document", `{`, false, false},
		{"malformed log", "{\n", true, false},
		{"future log", "{\"schema\":99}\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			store := NewStore(Config{RootDir: t.TempDir()})
			req := session.StartSessionRequest{AppName: "app", UserID: "user", PreferredSessionID: "old"}
			old, err := store.StartSession(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			path := store.newDocumentPath(old)
			if tc.log {
				path = eventLogPath(path)
			}
			if err := os.WriteFile(path, []byte(tc.data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: old.SessionRef}); !errors.Is(err, session.ErrUnreadableSession) || errors.Is(err, session.ErrUnsupportedLegacyFormat) != tc.legacy {
				t.Fatalf("LoadSession damaged data = %v", err)
			}
			if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: old.SessionRef, Event: &session.Event{Type: session.EventTypeLifecycle}}); !errors.Is(err, session.ErrUnreadableSession) {
				t.Fatalf("AppendEvent to damaged data = %v", err)
			}
			req.PreferredSessionID = "fresh"
			if _, err := store.StartSession(ctx, req); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(raw, []byte(tc.data)) {
				t.Fatalf("unreadable bytes changed: %q, %v", raw, err)
			}
		})
	}
}

func TestUnreadableIndexRowPreservesPaginationAndExactReadFailure(t *testing.T) {
	ctx := t.Context()
	store := NewStore(Config{RootDir: t.TempDir()})
	for _, id := range []string{"healthy", "bad-metadata", "bad-path"} {
		if _, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "app", UserID: "user", PreferredSessionID: id}); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.openSessionIndex()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sessions SET path='../outside' WHERE session_id='bad-path'; UPDATE sessions SET metadata_json='{' WHERE session_id='bad-metadata'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reader := NewStore(Config{RootDir: store.rootDir})
	cursor := ""
	for _, id := range []string{"bad-path", "bad-metadata", "healthy"} {
		page, err := reader.ListSessions(ctx, session.ListSessionsRequest{Limit: 1, Cursor: cursor})
		if err != nil || len(page.Sessions) != 1 || page.Sessions[0].SessionID != id {
			t.Fatalf("expected summary %q, got %#v, %v", id, page, err)
		}
		_, err = reader.Session(ctx, page.Sessions[0].SessionRef)
		if id == "healthy" && err != nil || id != "healthy" && !errors.Is(err, session.ErrUnreadableSession) {
			t.Fatalf("exact read for %q: %v", id, err)
		}
		cursor = page.NextCursor
	}
	if cursor != "" {
		t.Fatal("unexpected next page")
	}
}
