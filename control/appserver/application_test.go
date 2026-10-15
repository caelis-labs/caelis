package appserver

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/sessionprofile"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

func applicationTestProfile() sessionprofile.Profile {
	return sessionprofile.Profile{Version: "role/1", Instructions: "Only explicit instructions.", Model: "model", ToolsVersion: "tools/1", Execution: "tools-only"}
}
func applicationTestStore(t *testing.T) (*sessionprofile.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	store, err := sessionprofile.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func TestSessionProfileAuthorizerSharesOwnerAcrossSurfaces(t *testing.T) {
	store, _ := applicationTestStore(t)
	owner := Principal{ID: "owner"}
	sessions := inmemory.NewStore(inmemory.Config{})
	scope, _ := SessionScope(owner)
	for _, id := range []string{"owned", "forged", "ordinary", "retired"} {
		metadata := map[string]any{}
		if id == "owned" || id == "forged" {
			metadata[sessionvisibility.MetadataSystemManagedAgent] = sessionprofile.MetadataKind
		}
		if id == "retired" {
			metadata[sessionvisibility.MetadataSystemManagedAgent] = "bot"
		}
		if _, err := sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "owner", PreferredSessionID: id, Metadata: metadata}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutBinding(t.Context(), sessionprofile.Binding{Scope: scope, SessionID: "owned", Profile: applicationTestProfile(), CreationDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	a := SessionAuthorizer{Sessions: sessions, SessionProfiles: store}
	for _, tc := range []struct {
		name    string
		p       Principal
		action  Action
		id      string
		allowed bool
	}{
		{"profile-read", owner, ActionSessionInspect, "owned", true},
		{"profile-prompt", owner, ActionPrompt, "owned", true},
		{"same-owner-other-surface", Principal{ID: "owner"}, ActionPrompt, "owned", true},
		{"foreign-owner", Principal{ID: "other"}, ActionSessionInspect, "owned", false},
		{"metadata-forgery", owner, ActionPrompt, "forged", false},
		{"ordinary-read", owner, ActionSessionInspect, "ordinary", true},
		{"ordinary-prompt", owner, ActionPrompt, "ordinary", true},
		{"ordinary-create", owner, ActionSessionCreate, "", true},
		{"profile-participant", owner, ActionParticipantStart, "owned", true},
		{"profile-steer", owner, ActionSteer, "owned", true},
		{"profile-compact", owner, ActionSessionCompact, "owned", true},
		{"ignored-model-source", owner, ActionSessionModel, "owned", false},
		{"creation-bound-permission", owner, ActionSessionApprovalMode, "owned", false},
		{"retired-mode", owner, ActionPrompt, "retired", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := a.Authorize(t.Context(), tc.p, tc.action, tc.id); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
	if err := store.ArchiveBinding(t.Context(), scope, "owned"); err != nil {
		t.Fatal(err)
	}
	if err := a.Authorize(t.Context(), owner, ActionPrompt, "owned"); !errors.Is(err, ErrSessionClosed) {
		t.Fatalf("closed prompt=%v", err)
	}
	if err := a.Authorize(t.Context(), owner, ActionSessionInspect, "owned"); err != nil {
		t.Fatal(err)
	}
	if err := (ProductCommandAuthorizer{Sessions: a}).Authorize(t.Context(), owner, ActionModelDelete, ""); err != nil {
		t.Fatalf("Host permission differs by surface: %v", err)
	}
}

type applicationBoundaryBackend struct {
	effects int
	result  CommandResult
}

func (b *applicationBoundaryBackend) ExecuteControlCommand(context.Context, Principal, Action, any) (CommandResult, error) {
	b.effects++
	return b.result, nil
}

func TestSessionInputSourceValidation(t *testing.T) {
	for _, kind := range []string{"user", "application_summary", "external_material"} {
		if err := validateInputSource(kind, ""); err != nil {
			t.Fatal(kind, err)
		}
	}
	for _, kind := range []string{"", "background", "userApproved", "assistant", "reminder"} {
		if err := validateInputSource(kind, ""); errorcode.CodeOf(err) != errorcode.InvalidArgument {
			t.Fatal(kind, err)
		}
	}
	if err := validateInputSource("authorized_background", "grant"); err != nil {
		t.Fatal(err)
	}
	if err := validateInputSource("user", "grant"); err == nil {
		t.Fatal("user source acquired background authority")
	}
}
