package file

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestStoreLegacyV1DocumentFiltersACPPlacementWithoutReadRepair(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	at := time.Date(2026, time.July, 21, 10, 30, 0, 0, time.UTC)
	store := NewStore(Config{
		RootDir: root,
		Clock:   func() time.Time { return at },
	})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "legacy-user",
		PreferredSessionID: "legacy-session",
		Workspace: session.WorkspaceRef{
			Key: "legacy-workspace",
			CWD: "/tmp/legacy-workspace",
		},
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	message := model.NewTextMessage(model.RoleUser, "persisted before v2")
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: active.SessionRef,
		Event: &session.Event{
			Type:       session.EventTypeUser,
			Visibility: session.VisibilityCanonical,
			Message:    &message,
		},
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}

	sealed, err := placement.Seal(placement.Placement{
		Kind:                    placement.KindAgent,
		ProfileID:               "profile/legacy-acp",
		Agent:                   "legacy-agent",
		Model:                   "remote-model",
		ReasoningEffort:         "high",
		ReasoningEffortConfigID: "effort",
		SessionConfigValues:     map[string]string{"effort": "high"},
		ConfigFingerprint:       "legacy-config",
	})
	if err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	modelBacked, err := placement.Seal(placement.Placement{
		Kind:              placement.KindModel,
		ProfileID:         "profile/provider",
		Model:             "provider/model",
		ConfigFingerprint: "provider-config",
	})
	if err != nil {
		t.Fatalf("Seal(model) error = %v", err)
	}
	tampered := sealed
	tampered.Model = "different-model"

	documentPath, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatalf("resolveWritePath() error = %v", err)
	}
	legacy := map[string]any{
		"kind":    documentKind,
		"version": legacyV1DocumentVersion,
		"session": map[string]any{
			"app_name":      active.AppName,
			"user_id":       active.UserID,
			"session_id":    active.SessionID,
			"workspace_key": active.WorkspaceKey,
			"revision":      7,
			"cwd":           active.CWD,
			"title":         "legacy title",
			"metadata":      map[string]any{"owner": "legacy"},
			"controller": map[string]any{
				"kind":              session.ControllerKindACP,
				"controller_id":     "controller-1",
				"agent_name":        "controller-agent",
				"epoch_id":          "epoch-1",
				"remote_session_id": "remote-controller-session",
			},
			"participants": []any{
				map[string]any{
					"id": "valid-acp", "kind": session.ParticipantKindACP,
					"role": session.ParticipantRoleSidecar, "agent_name": "legacy-agent",
					"placement": sealed, "session_id": "remote-valid",
				},
				map[string]any{
					"id": "missing-placement-acp", "kind": session.ParticipantKindACP,
					"role": session.ParticipantRoleSidecar, "agent_name": "old-agent",
					"reasoning_effort": "high", "session_id": "remote-missing",
				},
				map[string]any{
					"id": "tampered-acp", "kind": session.ParticipantKindACP,
					"role": session.ParticipantRoleSidecar, "placement": tampered,
				},
				map[string]any{
					"id": "model-backed-acp", "kind": session.ParticipantKindACP,
					"role": session.ParticipantRoleSidecar, "placement": modelBacked,
				},
				map[string]any{
					"id": "child-task", "kind": session.ParticipantKindSubagent,
					"role": session.ParticipantRoleDelegated, "session_id": "child-session",
					"delegation_id": "delegation-1", "parent_turn_id": "turn-1",
				},
			},
			"created_at": at.Add(-time.Hour),
			"updated_at": at.Add(-time.Minute),
		},
		"state": map[string]any{"mode": "legacy", "nested": map[string]any{"kept": true}},
	}
	before, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent(legacy) error = %v", err)
	}
	before = append(before, '\n')
	if err := os.WriteFile(documentPath, before, 0o600); err != nil {
		t.Fatalf("WriteFile(legacy) error = %v", err)
	}

	reopened := NewStore(Config{RootDir: root, Clock: func() time.Time { return at }})
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession(v1) error = %v", err)
	}
	expectedSession := session.CloneSession(session.Session{
		SessionRef: active.SessionRef,
		Revision:   7,
		CWD:        active.CWD,
		Title:      "legacy title",
		Metadata:   map[string]any{"owner": "legacy"},
		Controller: session.ControllerBinding{
			Kind:            session.ControllerKindACP,
			ControllerID:    "controller-1",
			AgentName:       "controller-agent",
			EpochID:         "epoch-1",
			RemoteSessionID: "remote-controller-session",
		},
		Participants: []session.ParticipantBinding{
			{
				ID: "valid-acp", Kind: session.ParticipantKindACP,
				Role: session.ParticipantRoleSidecar, AgentName: "legacy-agent",
				Placement: sealed, SessionID: "remote-valid",
			},
			{
				ID: "model-backed-acp", Kind: session.ParticipantKindACP,
				Role: session.ParticipantRoleSidecar, Placement: modelBacked,
			},
			{
				ID: "child-task", Kind: session.ParticipantKindSubagent,
				Role: session.ParticipantRoleDelegated, SessionID: "child-session",
				DelegationID: "delegation-1", ParentTurnID: "turn-1",
			},
		},
		CreatedAt: at.Add(-time.Hour),
		UpdatedAt: at.Add(-time.Minute),
	})
	if !reflect.DeepEqual(loaded.Session, expectedSession) {
		t.Fatalf("legacy Session = %#v, want %#v", loaded.Session, expectedSession)
	}
	expectedState := map[string]any{"mode": "legacy", "nested": map[string]any{"kept": true}}
	if !reflect.DeepEqual(loaded.State, expectedState) {
		t.Fatalf("legacy State = %#v, want %#v", loaded.State, expectedState)
	}
	if len(loaded.Events) != 1 || session.EventText(loaded.Events[0]) != "persisted before v2" {
		t.Fatalf("legacy Events = %#v, want existing canonical event", loaded.Events)
	}
	wantReport := MigrationReport{
		FromVersion: legacyV1DocumentVersion,
		Dropped: []MigrationDrop{
			{
				SessionRef: active.SessionRef,
				Category:   legacyMigrationCategoryACPParticipant,
				Identity:   "participants[1]",
				Reason:     legacyMigrationReasonMissingPlacement,
			},
			{
				SessionRef: active.SessionRef,
				Category:   legacyMigrationCategoryACPParticipant,
				Identity:   "participants[2]",
				Reason:     legacyMigrationReasonInvalidPlacement,
			},
		},
	}
	if got := reopened.MigrationReport(); !reflect.DeepEqual(got, wantReport) {
		t.Fatalf("MigrationReport() = %#v, want %#v", got, wantReport)
	}
	detached := reopened.MigrationReport()
	detached.Dropped[0].Reason = "mutated-by-caller"
	if got := reopened.MigrationReport(); !reflect.DeepEqual(got, wantReport) {
		t.Fatalf("MigrationReport() shared caller mutation: %#v, want %#v", got, wantReport)
	}
	if _, err := reopened.Session(ctx, active.SessionRef); err != nil {
		t.Fatalf("Session(second v1 read) error = %v", err)
	}
	if got := reopened.MigrationReport(); !reflect.DeepEqual(got, wantReport) {
		t.Fatalf("MigrationReport() after repeated read = %#v, want deduplicated %#v", got, wantReport)
	}

	afterRead, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatalf("ReadFile(after read) error = %v", err)
	}
	if !bytes.Equal(afterRead, before) {
		t.Fatalf("reading v1 rewrote the document\ngot:  %s\nwant: %s", afterRead, before)
	}

	updated, err := reopened.UpdateState(ctx, session.UpdateStateRequest{
		SessionRef: active.SessionRef,
		Update: func(state map[string]any) (map[string]any, error) {
			state["migrated"] = true
			return state, nil
		},
	})
	if err != nil {
		t.Fatalf("UpdateState(v1) error = %v", err)
	}
	if updated.Revision != 8 {
		t.Fatalf("UpdateState(v1) revision = %d, want 8", updated.Revision)
	}
	written, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatalf("ReadFile(v2) error = %v", err)
	}
	var current persistedDocument
	if err := json.Unmarshal(written, &current); err != nil {
		t.Fatalf("Unmarshal(v2) error = %v", err)
	}
	if current.Version != documentVersion {
		t.Fatalf("document version after mutation = %d, want %d", current.Version, documentVersion)
	}
	if got := len(current.Session.Participants); got != 3 {
		t.Fatalf("persisted v2 participants = %#v, want only recoverable bindings", current.Session.Participants)
	}
	if got := reopened.MigrationReport(); !reflect.DeepEqual(got, wantReport) {
		t.Fatalf("MigrationReport() after v2 mutation = %#v, want retained %#v", got, wantReport)
	}

	rehydrated, err := NewStore(Config{RootDir: root}).LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession(v2) error = %v", err)
	}
	if !reflect.DeepEqual(rehydrated.Session.Participants, loaded.Session.Participants) {
		t.Fatalf("rehydrated participants = %#v, want %#v", rehydrated.Session.Participants, loaded.Session.Participants)
	}
	if got := rehydrated.State["migrated"]; got != true {
		t.Fatalf("rehydrated State migrated = %#v, want true", got)
	}
}

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

func TestDecodePersistedTransactionAppliesLegacyV1ParticipantRules(t *testing.T) {
	t.Parallel()
	modelBacked, err := placement.Seal(placement.Placement{
		Kind:              placement.KindModel,
		ProfileID:         "profile/provider",
		Model:             "provider/model",
		ConfigFingerprint: "provider-config",
	})
	if err != nil {
		t.Fatalf("Seal(model) error = %v", err)
	}

	raw, err := json.Marshal(map[string]any{
		"kind":    transactionKind,
		"version": transactionVersion,
		"document": map[string]any{
			"kind":    documentKind,
			"version": legacyV1DocumentVersion,
			"session": map[string]any{
				"session_id": "legacy-transaction",
				"participants": []any{
					map[string]any{
						"id": "unsafe-acp", "kind": session.ParticipantKindACP,
						"reasoning_effort": "high",
					},
					map[string]any{
						"id": "model-backed-acp", "kind": session.ParticipantKindACP,
						"role": session.ParticipantRoleSidecar, "placement": modelBacked,
					},
					map[string]any{
						"id": "safe-child", "kind": session.ParticipantKindSubagent,
						"session_id": "child-session",
					},
				},
			},
		},
		"events": []any{},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	record, report, err := decodePersistedTransactionWithReport(raw)
	if err != nil {
		t.Fatalf("decodePersistedTransactionWithReport() error = %v", err)
	}
	if record.Document.Version != documentVersion {
		t.Fatalf("transaction document version = %d, want %d", record.Document.Version, documentVersion)
	}
	want := []session.ParticipantBinding{
		{
			ID: "model-backed-acp", Kind: session.ParticipantKindACP,
			Role: session.ParticipantRoleSidecar, Placement: modelBacked,
		},
		{ID: "safe-child", Kind: session.ParticipantKindSubagent, SessionID: "child-session"},
	}
	if got := record.Document.Session.Participants; !reflect.DeepEqual(got, want) {
		t.Fatalf("transaction participants = %#v, want %#v", got, want)
	}
	wantReport := MigrationReport{
		FromVersion: legacyV1DocumentVersion,
		Dropped: []MigrationDrop{{
			SessionRef: session.SessionRef{SessionID: "legacy-transaction"},
			Category:   legacyMigrationCategoryACPParticipant,
			Identity:   "participants[0]",
			Reason:     legacyMigrationReasonMissingPlacement,
		}},
	}
	if !reflect.DeepEqual(report, wantReport) {
		t.Fatalf("transaction MigrationReport = %#v, want %#v", report, wantReport)
	}
}

func TestStoreRejectsFutureDocumentVersion(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ctx := context.Background()
	store := NewStore(Config{RootDir: root})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "future-user",
		PreferredSessionID: "future-session",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	path, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatalf("resolveWritePath() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	raw["version"] = documentVersion + 1
	data, err = json.Marshal(raw)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err = NewStore(Config{RootDir: root}).Session(ctx, active.SessionRef)
	if err == nil || !strings.Contains(err.Error(), "unsupported document") || !strings.Contains(err.Error(), "version 3") {
		t.Fatalf("Session(future version) error = %v, want unsupported version 3", err)
	}
}
