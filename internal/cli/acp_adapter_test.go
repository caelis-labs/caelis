package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/internal/adapterhostclient"
)

func TestAdapterProxyRejectsGrantSchemaMismatchBeforeConnecting(t *testing.T) {
	for _, test := range []struct{ name, granted, selected string }{
		{"stable-grant-draft-request", "", "schema-v2.0.0-alpha.8"},
		{"draft-grant-stable-request", "schema-v2.0.0-alpha.8", ""},
		{"wrong-draft", "schema-v2.0.0-alpha.7", "schema-v2.0.0-alpha.8"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, err := adapterhostclient.WriteChannelGrantFile(t.TempDir(), adapterhostclient.ChannelGrantFile{Endpoint: "http://127.0.0.1:1", AdapterID: "codex", Token: "fixture-one-use", ACPDraftSchema: test.granted})
			if err != nil {
				t.Fatal(err)
			}
			err = runAdapterACPProxy(t.Context(), gatewayapp.Config{}, productClientOptions{}, "codex", path, test.selected, strings.NewReader(""), io.Discard)
			if err == nil || !strings.Contains(err.Error(), "grant ACP schema does not match") {
				t.Fatalf("schema mismatch did not reject before connection: %v", err)
			}
		})
	}
}
