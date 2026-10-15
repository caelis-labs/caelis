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

func TestAdditiveObjectsKeepDeclaredGoFields(t *testing.T) {
	value := &schema{Type: "object", Properties: map[string]*schema{"revision": {Type: "string"}}, AdditionalProperties: []byte("true"), IgnoreUnknownFields: true}
	var typed strings.Builder
	writeGoSchema(&typed, "Receipt", value, nil)
	if !strings.Contains(typed.String(), "type Receipt struct") || !strings.Contains(typed.String(), "Revision *string") {
		t.Fatalf("additive object lost typed fields: %s", typed.String())
	}
	if got := tsType(value, nil); !strings.Contains(got, "[key: string]: JSONValue") || !strings.Contains(got, "revision?: string") {
		t.Fatalf("inline additive object lost extension or typed field: %s", got)
	}
	value.IgnoreUnknownFields = false
	var opaque strings.Builder
	writeGoSchema(&opaque, "VendorObject", value, nil)
	if !strings.Contains(opaque.String(), "map[string]JSONValue") {
		t.Fatalf("vendor fields would be lost: %s", opaque.String())
	}
}

func TestAdditiveAllOfTypeScriptKeepsExtensions(t *testing.T) {
	value := &schema{IgnoreUnknownFields: true, AllOf: []*schema{{Type: "object", Properties: map[string]*schema{"operation_id": {Type: "string"}}}}}
	var out strings.Builder
	writeTypeScriptSchema(&out, "Request", value, nil)
	if !strings.Contains(out.String(), "operation_id?: string") || !strings.Contains(out.String(), "[key: string]: JSONValue") {
		t.Fatalf("allOf extension missing: %s", out.String())
	}
}
