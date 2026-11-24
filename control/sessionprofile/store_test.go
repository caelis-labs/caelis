package sessionprofile

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "control.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}
func testProfile() Profile {
	return Profile{Version: "v1", Instructions: "Use the Session capabilities.", Model: "test/model", ToolsVersion: "tools-v1", Execution: "tools-only"}
}

type testPrincipal struct{ Scope }

func testOwner(t *testing.T, s *Store, seed int) testPrincipal {
	t.Helper()
	return testPrincipal{PrincipalScope(fmt.Sprintf("principal-%d", seed))}
}
func testBinding(t *testing.T, s *Store, c testPrincipal, id string) Binding {
	t.Helper()
	b := Binding{Scope: c.Scope, SessionID: id, Profile: testProfile(), CreationDigest: "creation-" + id}
	if err := s.PutBinding(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	return b
}
func assertError(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestA05ProfileRejectsInheritedAndIncompleteAuthority(t *testing.T) {
	cases := []struct {
		name   string
		change func(*Profile)
		want   error
	}{
		{"version", func(p *Profile) { p.Version = "" }, ErrInvalid},
		{"model", func(p *Profile) { p.Model = "" }, ErrInvalid},
		{"tools-version", func(p *Profile) { p.ToolsVersion = "" }, ErrInvalid},
		{"execution", func(p *Profile) { p.Execution = "" }, ErrInvalid},
		{"all-inheritance", func(p *Profile) { p.Inherit = Inheritance{true, true, true, true} }, ErrUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { p := testProfile(); tc.change(&p); assertError(t, ValidateProfile(p), tc.want) })
	}
	err := ValidateSource(Source{Kind: "background", OperationID: "job"})
	assertError(t, err, ErrUnsupported)
	if errorcode.CodeOf(err) != errorcode.Unsupported {
		t.Fatal("unsupported code missing")
	}
}

func TestA08ResourcesImmutableScopedVerifiedAndReopen(t *testing.T) {
	s, path := testStore(t)
	c := testOwner(t, s, 1)
	b := testBinding(t, s, c, "s")
	other := testOwner(t, s, 2)
	otherBinding := testBinding(t, s, c, "other-session")
	testBinding(t, s, other, "foreign")
	content := []byte("immutable resource\x00bytes")
	r, err := s.CreateResource(t.Context(), c.Scope, b.SessionID, "resource-op", "../../outside", "application/octet-stream", content)
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.CreateResource(t.Context(), c.Scope, b.SessionID, "resource-op", "../../outside", "application/octet-stream", content)
	if err != nil || r != same {
		t.Fatalf("resource retry %+v %v", same, err)
	}
	_, err = s.CreateResource(t.Context(), c.Scope, b.SessionID, "resource-op", "../../outside", "application/octet-stream", []byte("changed"))
	assertError(t, err, ErrConflict)
	for _, foreign := range []struct {
		scope   Scope
		session string
	}{{other.Scope, b.SessionID}, {c.Scope, otherBinding.SessionID}} {
		_, _, err = s.ReadResource(t.Context(), foreign.scope, foreign.session, r.ID)
		assertError(t, err, ErrNotFound)
	}
	_, err = s.CreateResource(t.Context(), c.Scope, b.SessionID, "oversized", "x", "text/plain", make([]byte, MaxResourceBytes+1))
	assertError(t, err, ErrInvalid)
	if err = s.ArchiveBinding(t.Context(), c.Scope, b.SessionID); err != nil {
		t.Fatal(err)
	}
	got, read, err := s.ReadResource(t.Context(), c.Scope, b.SessionID, r.ID)
	if err != nil || got != r || !bytes.Equal(read, content) {
		t.Fatalf("revoked resource read %+v %q %v", got, read, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, read, err = reopened.ReadResource(t.Context(), c.Scope, b.SessionID, r.ID)
	if err != nil || got != r || !bytes.Equal(read, content) {
		t.Fatalf("resource reopen %+v %q %v", got, read, err)
	}
	if _, err = reopened.db.Exec(`UPDATE session_resources SET data=? WHERE id=?`, []byte("corrupted"), r.ID); err != nil {
		t.Fatal(err)
	}
	_, _, err = reopened.ReadResource(t.Context(), c.Scope, b.SessionID, r.ID)
	assertError(t, err, ErrInvalid)
}
