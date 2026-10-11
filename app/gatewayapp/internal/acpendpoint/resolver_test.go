package acpendpoint

import (
	"context"
	"io"
	"os"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/adapterhost"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/endpoint"
	"github.com/caelis-labs/caelis/internal/adapterhostclient"
)

type draftGrantFixture struct {
	descriptor adapterhost.Descriptor
	request    adapterhost.GrantRequest
	issued     int
}

func (f *draftGrantFixture) Inspect(context.Context, string) (adapterhost.Descriptor, error) {
	return f.descriptor, nil
}
func (f *draftGrantFixture) IssueGrant(_ context.Context, _, _ string, request adapterhost.GrantRequest) (adapterhost.Grant, error) {
	f.request, f.issued = request, f.issued+1
	return adapterhost.Grant{Token: "fixture-one-use"}, nil
}
func (*draftGrantFixture) ServeChannel(context.Context, string, string, io.Reader, io.Writer) error {
	return nil
}

func TestHostedDraftResolutionBindsSchemaBeforeLaunchingProxy(t *testing.T) {
	const schema = "schema-v2.0.0-alpha.8"
	fixture := &draftGrantFixture{descriptor: adapterhost.Descriptor{Available: true, ACPDraftSchemas: []string{schema}}}
	root := t.TempDir()
	resolver, err := New(Config{Service: fixture, StoreDir: root, Executable: func() (string, error) { return "/fixture/caelis", nil }, ControlURL: func() string { return "http://127.0.0.1:1" }})
	if err != nil {
		t.Fatal(err)
	}
	request := endpoint.Request{AdapterID: "codex", ConnectionID: "child-session", CWD: root, ACPDraftSchema: "schema-v2.0.0-alpha.7"}
	if _, err := resolver.ResolveACPProcess(t.Context(), request); err == nil || fixture.issued != 0 {
		t.Fatalf("incompatible schema issued grant: %v, %d", err, fixture.issued)
	}
	request.ACPDraftSchema = schema
	process, err := resolver.ResolveACPProcess(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer process.Release()
	if fixture.request.ACPDraftSchema != schema || fixture.request.ConnectionID != request.ConnectionID || fixture.request.CWD != root || !slices.Equal(fixture.request.AllowedRoots, []string{root}) || !slices.Equal(fixture.request.WritableRoots, []string{root}) {
		t.Fatalf("granted profile = %#v", fixture.request)
	}
	if !slices.Contains(process.Args, "--acp-draft="+schema) {
		t.Fatalf("proxy args = %v", process.Args)
	}
	grantFileIndex := slices.Index(process.Args, "--adapter-grant-file")
	if grantFileIndex < 0 || grantFileIndex+1 >= len(process.Args) {
		t.Fatal("proxy omitted grant")
	}
	path := process.Args[grantFileIndex+1]
	grant, err := adapterhostclient.ConsumeChannelGrantFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ACPDraftSchema != schema || grant.AdapterID != request.AdapterID {
		t.Fatalf("proxy grant = %#v", grant)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("grant not consumed: %v", err)
	}
}
