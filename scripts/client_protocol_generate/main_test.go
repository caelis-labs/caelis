package main

import (
	"strings"
	"testing"
)

func TestNullableExecutionSchemasKeepConcreteGeneratedTypes(t *testing.T) {
	optional := &schema{OneOf: []*schema{{Ref: "#/components/schemas/ExecutionConfig"}, {Type: "null"}}}
	if got := goType(optional, nil); got != "ExecutionConfig" {
		t.Fatalf("nullable Go type = %q", got)
	}
	if got := tsType(optional, nil); got != "ExecutionConfig | null" {
		t.Fatalf("nullable TypeScript type = %q", got)
	}
	inherit := &schema{OneOf: []*schema{{Type: "boolean"}, {Type: "null"}}}
	if got := goType(inherit, nil); got != "bool" || !goNeedsPointer(inherit, got, nil) {
		t.Fatalf("nullable inherit Go type = %q, must preserve nil vs false", got)
	}
	if got := tsType(inherit, nil); got != "boolean | null" {
		t.Fatalf("nullable inherit TypeScript type = %q", got)
	}
	var out strings.Builder
	writeGoSchema(&out, "EnvironmentConfig", &schema{Type: "object", Properties: map[string]*schema{"inherit": inherit}}, nil)
	if !strings.Contains(out.String(), "Inherit *bool") {
		t.Fatalf("generated Go lost optional nullable bool: %s", out.String())
	}
}
