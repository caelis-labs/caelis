package mcpconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeDropsEmptyNamesAndClones(t *testing.T) {
	enabled := false
	in := Servers{
		" context7 ": {
			Command: "npx",
			Args:    []string{"-y", "@upstash/context7-mcp"},
			Env:     map[string]string{"CONTEXT7_API_KEY": "secret"},
			Enabled: &enabled,
		},
		"   ": {Command: "ignored"},
	}
	got := Normalize(in)
	want := Servers{
		"context7": {
			Command: "npx",
			Args:    []string{"-y", "@upstash/context7-mcp"},
			Env:     map[string]string{"CONTEXT7_API_KEY": "secret"},
			Enabled: &enabled,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Normalize() = %#v, want %#v", got, want)
	}
	got["context7"].Env["CONTEXT7_API_KEY"] = "mutated"
	if in[" context7 "].Env["CONTEXT7_API_KEY"] != "secret" {
		t.Fatal("Normalize() must clone env")
	}
}

func TestValidateEnabledServers(t *testing.T) {
	disabled := false
	tests := []struct {
		name    string
		servers Servers
		wantErr string
	}{
		{
			name: "stdio command",
			servers: Servers{
				"docs": {Command: "npx", Args: []string{"-y", "demo"}},
			},
		},
		{
			name: "http url",
			servers: Servers{
				"remote": {Type: "http", URL: "https://mcp.example.com/mcp"},
			},
		},
		{
			name: "disabled skips command",
			servers: Servers{
				"docs": {Enabled: &disabled},
			},
		},
		{
			name: "stdio missing command",
			servers: Servers{
				"docs": {},
			},
			wantErr: "requires a command",
		},
		{
			name: "http missing url",
			servers: Servers{
				"remote": {Transport: "http"},
			},
			wantErr: "requires a URL",
		},
		{
			name: "path separator name",
			servers: Servers{
				"user/docs": {Command: "npx"},
			},
			wantErr: "path separator",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.servers)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateIdentitiesRejectsNormalizedDuplicates(t *testing.T) {
	err := ValidateIdentities(Servers{
		"docs":   {Command: "npx"},
		" docs ": {Command: "other"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate MCP server") {
		t.Fatalf("ValidateIdentities() error = %v, want duplicate", err)
	}
}

func TestReservedNamespace(t *testing.T) {
	if !ReservedNamespace(NamespaceUser) || !ReservedNamespace(NamespaceProject) {
		t.Fatal("catalog namespaces must be reserved")
	}
	if ReservedNamespace("drawio") || ReservedNamespace("user") {
		t.Fatal("ordinary plugin IDs must not be reserved")
	}
}

func TestServerConfigJSONRoundTrip(t *testing.T) {
	enabled := true
	raw, err := json.Marshal(Servers{
		"context7": {
			Command: "npx",
			Args:    []string{"-y", "@upstash/context7-mcp"},
			Enabled: &enabled,
		},
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var got Servers
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got["context7"].Command != "npx" || !got["context7"].IsEnabled() {
		t.Fatalf("round-trip = %#v", got)
	}
}
