package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const generatorVersion = "caelis-client-protocol-gen/v2.2.0"

var check = flag.Bool("check", false, "verify generated output without writing")

type openAPISpec struct {
	OpenAPI    string                          `json:"openapi"`
	Paths      map[string]map[string]operation `json:"paths"`
	Components struct {
		Schemas       map[string]*schema     `json:"schemas"`
		RequestBodies map[string]requestBody `json:"requestBodies"`
		Responses     map[string]response    `json:"responses"`
	} `json:"components"`
}

type operation struct {
	OperationID string              `json:"operationId"`
	RequestBody *requestBody        `json:"requestBody"`
	Responses   map[string]response `json:"responses"`
}

type requestBody struct {
	Ref     string `json:"$ref"`
	Content map[string]struct {
		Schema *schema `json:"schema"`
	} `json:"content"`
}

type response struct {
	Ref     string `json:"$ref"`
	Content map[string]struct {
		Schema *schema `json:"schema"`
	} `json:"content"`
}

type schema struct {
	Ref                  string             `json:"$ref"`
	Type                 string             `json:"type"`
	Format               string             `json:"format"`
	Properties           map[string]*schema `json:"properties"`
	Required             []string           `json:"required"`
	Items                *schema            `json:"items"`
	Enum                 []any              `json:"enum"`
	Const                any                `json:"const"`
	OneOf                []*schema          `json:"oneOf"`
	AllOf                []*schema          `json:"allOf"`
	AdditionalProperties json.RawMessage    `json:"additionalProperties"`
}

type objectShape struct {
	properties map[string]*schema
	required   map[string]bool
}

func main() {
	flag.Parse()
	root, err := os.Getwd()
	must(err)
	specPath := filepath.Join(root, "api/control/v1/openapi.json")
	data, err := os.ReadFile(specPath)
	must(err)
	var spec openAPISpec
	must(json.Unmarshal(data, &spec))
	must(validateSpec(spec))
	operations := operationIDs(spec)

	goOutput, err := generateGo(spec, operations)
	must(err)
	outputs := map[string][]byte{
		filepath.Join(root, "surfaces/appserver/generated/control_v1.gen.go"): goOutput,
		filepath.Join(root, "clients/typescript/control-v1.gen.ts"):           generateTypeScript(spec, operations),
	}
	for path, want := range outputs {
		want = append(bytes.TrimSpace(want), '\n')
		if *check {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, want) {
				must(fmt.Errorf("generated file is stale: %s", path))
			}
			continue
		}
		must(os.MkdirAll(filepath.Dir(path), 0o755))
		must(os.WriteFile(path, want, 0o644))
	}
}

func validateSpec(spec openAPISpec) error {
	if spec.OpenAPI != "3.1.0" {
		return fmt.Errorf("openapi version = %q, want 3.1.0", spec.OpenAPI)
	}
	if len(operationIDs(spec)) != 15 {
		return fmt.Errorf("operation count = %d, want 15", len(operationIDs(spec)))
	}
	required := []string{
		"CreateSessionRequest", "CloseSessionRequest", "PromptRequest", "SteerRequest", "CancelRequest",
		"ResolveApprovalRequest", "AttachParticipantRequest", "PromptParticipantRequest", "CancelParticipantRequest",
		"DetachParticipantRequest", "HandoffRequest", "CommandResult", "SessionState", "Envelope", "EventBatch",
	}
	for _, name := range required {
		if spec.Components.Schemas[name] == nil {
			return fmt.Errorf("required schema %q is missing", name)
		}
	}
	for path, methods := range spec.Paths {
		for method, operation := range methods {
			if err := validateOperationResponses(spec, method, operation); err != nil {
				return fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
			if operation.RequestBody == nil {
				continue
			}
			body, err := resolveRequestBody(spec, *operation.RequestBody)
			if err != nil {
				return fmt.Errorf("%s %s: %w", strings.ToUpper(method), path, err)
			}
			media, ok := body.Content["application/json"]
			if !ok || media.Schema == nil || media.Schema.Ref == "" {
				return fmt.Errorf("%s %s request body must reference one typed component schema", strings.ToUpper(method), path)
			}
			if _, ok := spec.Components.Schemas[refName(media.Schema.Ref)]; !ok {
				return fmt.Errorf("%s %s request schema %q is missing", strings.ToUpper(method), path, media.Schema.Ref)
			}
		}
	}
	if len(spec.Components.Schemas["Envelope"].OneOf) == 0 || len(spec.Components.Schemas["ACPUpdate"].OneOf) == 0 {
		return fmt.Errorf("envelope and ACPUpdate must be discriminated unions")
	}
	if !oneOfReferences(spec.Components.Schemas["ACPUpdate"], "ACPRawUpdate") {
		return fmt.Errorf("ACPUpdate must include ACPRawUpdate")
	}
	return nil
}

func validateOperationResponses(spec openAPISpec, method string, operation operation) error {
	required := []string{"200", "400", "401", "403"}
	if method != "get" {
		required = []string{"200", "202", "400", "401", "409"}
	}
	if operation.OperationID == "streamSessionEvents" {
		required = append(required, "500")
	}
	for _, status := range required {
		declared, ok := operation.Responses[status]
		if !ok {
			return fmt.Errorf("response %s is missing", status)
		}
		if declared.Ref != "" {
			if _, ok := spec.Components.Responses[refName(declared.Ref)]; !ok {
				return fmt.Errorf("response %s references missing component %q", status, declared.Ref)
			}
		}
	}
	return nil
}

func oneOfReferences(value *schema, name string) bool {
	if value == nil {
		return false
	}
	for _, option := range value.OneOf {
		if option != nil && refName(option.Ref) == name {
			return true
		}
	}
	return false
}

func resolveRequestBody(spec openAPISpec, body requestBody) (requestBody, error) {
	if body.Ref == "" {
		return body, nil
	}
	resolved, ok := spec.Components.RequestBodies[refName(body.Ref)]
	if !ok {
		return requestBody{}, fmt.Errorf("request body %q is missing", body.Ref)
	}
	return resolved, nil
}

func operationIDs(spec openAPISpec) []string {
	var operations []string
	for _, methods := range spec.Paths {
		for _, operation := range methods {
			if operation.OperationID != "" {
				operations = append(operations, operation.OperationID)
			}
		}
	}
	sort.Strings(operations)
	return operations
}

func generateGo(spec openAPISpec, operations []string) ([]byte, error) {
	var out strings.Builder
	out.WriteString("// Code generated by " + generatorVersion + " from api/control/v1/openapi.json; DO NOT EDIT.\n")
	out.WriteString("package generated\n\nimport (\n\t\"encoding/json\"\n\t\"time\"\n)\n\n")
	out.WriteString("const GeneratorVersion = \"" + generatorVersion + "\"\n\n")
	names := sortedSchemaNames(spec.Components.Schemas)
	for _, name := range names {
		writeGoSchema(&out, name, spec.Components.Schemas[name], spec.Components.Schemas)
	}
	out.WriteString("var OperationIDs = []string{")
	for index, operation := range operations {
		if index > 0 {
			out.WriteString(", ")
		}
		out.WriteString(strconv.Quote(operation))
	}
	out.WriteString("}\n")
	formatted, err := format.Source([]byte(out.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated Go: %w\n%s", err, out.String())
	}
	return formatted, nil
}

func writeGoSchema(out *strings.Builder, name string, value *schema, schemas map[string]*schema) {
	if value == nil {
		return
	}
	if name == "JSONValue" {
		out.WriteString("type JSONValue = any\n\n")
		return
	}
	if oneOfHasExplicitAdditionalProperties(value, schemas, map[string]bool{}) {
		// A flattened Go struct cannot distinguish which union fields belong to
		// an open variant and silently drops vendor keys. Keep the union payload
		// raw so decode/encode remains lossless while the OpenAPI schema retains
		// validation and TypeScript discrimination.
		out.WriteString("type " + name + " = json.RawMessage\n\n")
		return
	}
	if len(value.Enum) > 0 && value.Type == "string" {
		out.WriteString("type " + name + " string\n\nconst (\n")
		for _, raw := range value.Enum {
			text, _ := raw.(string)
			out.WriteString("\t" + name + goName(text) + " " + name + " = " + strconv.Quote(text) + "\n")
		}
		out.WriteString(")\n\n")
		return
	}
	shape := shapeOf(value, schemas, map[string]bool{})
	if len(shape.properties) > 0 && hasAdditionalProperties(value) {
		// Go has no native open struct. A map is the only generated DTO shape
		// that preserves both declared and vendor-defined properties without
		// hand-written marshal hooks.
		out.WriteString("type " + name + " map[string]JSONValue\n\n")
		return
	}
	if len(shape.properties) == 0 {
		if hasAdditionalProperties(value) || name == "JSONObject" {
			out.WriteString("type " + name + " map[string]any\n\n")
			return
		}
		out.WriteString("type " + name + " " + goType(value, schemas) + "\n\n")
		return
	}
	out.WriteString("type " + name + " struct {\n")
	for _, property := range sortedSchemaNames(shape.properties) {
		propertySchema := shape.properties[property]
		typeName := goType(propertySchema, schemas)
		required := shape.required[property]
		if !required && goNeedsPointer(propertySchema, typeName, schemas) {
			typeName = "*" + typeName
		}
		tag := property
		if !required {
			tag += ",omitempty"
		}
		out.WriteString("\t" + goName(property) + " " + typeName + " `json:\"" + tag + "\"`\n")
	}
	out.WriteString("}\n\n")
}

func oneOfHasExplicitAdditionalProperties(value *schema, schemas map[string]*schema, seen map[string]bool) bool {
	if value == nil || len(value.OneOf) == 0 {
		return false
	}
	for _, option := range value.OneOf {
		if schemaHasExplicitAdditionalProperties(option, schemas, seen) {
			return true
		}
	}
	return false
}

func schemaHasExplicitAdditionalProperties(value *schema, schemas map[string]*schema, seen map[string]bool) bool {
	if value == nil {
		return false
	}
	if value.Ref != "" {
		name := refName(value.Ref)
		if seen[name] {
			return false
		}
		nextSeen := cloneSeen(seen)
		nextSeen[name] = true
		return schemaHasExplicitAdditionalProperties(schemas[name], schemas, nextSeen)
	}
	if hasAdditionalProperties(value) {
		return true
	}
	for _, part := range value.AllOf {
		if schemaHasExplicitAdditionalProperties(part, schemas, seen) {
			return true
		}
	}
	return false
}

func goType(value *schema, schemas map[string]*schema) string {
	if value == nil {
		return "any"
	}
	if value.Ref != "" {
		return refName(value.Ref)
	}
	if value.Const != nil {
		switch value.Const.(type) {
		case bool:
			return "bool"
		case float64:
			return "float64"
		default:
			return "string"
		}
	}
	if len(value.Enum) > 0 {
		return "string"
	}
	switch value.Type {
	case "string":
		if value.Format == "date-time" {
			return "time.Time"
		}
		return "string"
	case "integer":
		switch value.Format {
		case "uint64":
			return "uint64"
		case "uint32":
			return "uint32"
		default:
			return "int"
		}
	case "number":
		return "float64"
	case "boolean":
		return "bool"
	case "array":
		return "[]" + goType(value.Items, schemas)
	case "object":
		return "map[string]any"
	default:
		if len(value.OneOf) > 0 || len(value.AllOf) > 0 || len(value.Properties) > 0 {
			return "map[string]any"
		}
		return "any"
	}
}

func goNeedsPointer(value *schema, typeName string, schemas map[string]*schema) bool {
	if strings.HasPrefix(typeName, "[]") || strings.HasPrefix(typeName, "map[") || typeName == "any" || typeName == "JSONValue" || typeName == "JSONObject" {
		return false
	}
	if value != nil && value.Ref != "" {
		resolved := schemas[refName(value.Ref)]
		if resolved != nil && len(resolved.Enum) > 0 {
			return false
		}
	}
	return true
}

func generateTypeScript(spec openAPISpec, operations []string) []byte {
	var out strings.Builder
	out.WriteString("// Code generated by " + generatorVersion + " from api/control/v1/openapi.json; DO NOT EDIT.\n")
	out.WriteString("export const generatorVersion = '" + generatorVersion + "' as const;\n")
	out.WriteString("export const operationIds = [")
	for index, operation := range operations {
		if index > 0 {
			out.WriteString(", ")
		}
		out.WriteString(strconv.Quote(operation))
	}
	out.WriteString("] as const;\nexport type OperationId = typeof operationIds[number];\n\n")
	for _, name := range sortedSchemaNames(spec.Components.Schemas) {
		writeTypeScriptSchema(&out, name, spec.Components.Schemas[name], spec.Components.Schemas)
	}
	return []byte(out.String())
}

func writeTypeScriptSchema(out *strings.Builder, name string, value *schema, schemas map[string]*schema) {
	if value == nil {
		return
	}
	if name == "JSONValue" {
		out.WriteString("export type JSONValue = unknown;\n\n")
		return
	}
	if len(value.OneOf) > 0 {
		parts := make([]string, 0, len(value.OneOf))
		for _, option := range value.OneOf {
			parts = append(parts, tsType(option, schemas))
		}
		out.WriteString("export type " + name + " = " + strings.Join(parts, " | ") + ";\n\n")
		return
	}
	if len(value.Enum) > 0 {
		parts := make([]string, 0, len(value.Enum))
		for _, option := range value.Enum {
			parts = append(parts, tsLiteral(option))
		}
		out.WriteString("export type " + name + " = " + strings.Join(parts, " | ") + ";\n\n")
		return
	}
	shape := shapeOf(value, schemas, map[string]bool{})
	if len(shape.properties) == 0 {
		out.WriteString("export type " + name + " = " + tsType(value, schemas) + ";\n\n")
		return
	}
	out.WriteString("export interface " + name + " {\n")
	for _, property := range sortedSchemaNames(shape.properties) {
		optional := ""
		if !shape.required[property] {
			optional = "?"
		}
		out.WriteString("  " + tsProperty(property) + optional + ": " + tsType(shape.properties[property], schemas) + ";\n")
	}
	if hasAdditionalProperties(value) {
		out.WriteString("  [key: string]: JSONValue;\n")
	}
	out.WriteString("}\n\n")
}

func tsType(value *schema, schemas map[string]*schema) string {
	if value == nil {
		return "unknown"
	}
	if value.Ref != "" {
		return refName(value.Ref)
	}
	if value.Const != nil {
		return tsLiteral(value.Const)
	}
	if len(value.Enum) > 0 {
		parts := make([]string, 0, len(value.Enum))
		for _, option := range value.Enum {
			parts = append(parts, tsLiteral(option))
		}
		return strings.Join(parts, " | ")
	}
	if len(value.OneOf) > 0 {
		parts := make([]string, 0, len(value.OneOf))
		for _, option := range value.OneOf {
			parts = append(parts, tsType(option, schemas))
		}
		return strings.Join(parts, " | ")
	}
	switch value.Type {
	case "string":
		return "string"
	case "integer", "number":
		return "number"
	case "boolean":
		return "boolean"
	case "array":
		return "Array<" + tsType(value.Items, schemas) + ">"
	case "object":
		if hasAdditionalProperties(value) && len(value.Properties) == 0 {
			return "Record<string, JSONValue>"
		}
		shape := shapeOf(value, schemas, map[string]bool{})
		var fields []string
		for _, property := range sortedSchemaNames(shape.properties) {
			optional := ""
			if !shape.required[property] {
				optional = "?"
			}
			fields = append(fields, tsProperty(property)+optional+": "+tsType(shape.properties[property], schemas))
		}
		return "{ " + strings.Join(fields, "; ") + " }"
	default:
		if len(value.AllOf) > 0 || len(value.Properties) > 0 {
			shape := shapeOf(value, schemas, map[string]bool{})
			var fields []string
			for _, property := range sortedSchemaNames(shape.properties) {
				optional := ""
				if !shape.required[property] {
					optional = "?"
				}
				fields = append(fields, tsProperty(property)+optional+": "+tsType(shape.properties[property], schemas))
			}
			return "{ " + strings.Join(fields, "; ") + " }"
		}
		return "unknown"
	}
}

func shapeOf(value *schema, schemas map[string]*schema, seen map[string]bool) objectShape {
	if value == nil {
		return objectShape{properties: map[string]*schema{}, required: map[string]bool{}}
	}
	if value.Ref != "" {
		name := refName(value.Ref)
		if seen[name] {
			return objectShape{properties: map[string]*schema{}, required: map[string]bool{}}
		}
		nextSeen := cloneSeen(seen)
		nextSeen[name] = true
		return shapeOf(schemas[name], schemas, nextSeen)
	}
	shape := objectShape{properties: map[string]*schema{}, required: map[string]bool{}}
	for property, propertySchema := range value.Properties {
		shape.properties[property] = propertySchema
	}
	for _, required := range value.Required {
		shape.required[required] = true
	}
	for _, part := range value.AllOf {
		mergeShape(&shape, shapeOf(part, schemas, seen), true)
	}
	if len(value.OneOf) > 0 {
		var intersection map[string]bool
		for _, option := range value.OneOf {
			optionShape := shapeOf(option, schemas, seen)
			mergeShape(&shape, optionShape, false)
			if intersection == nil {
				intersection = cloneRequired(optionShape.required)
			} else {
				for required := range intersection {
					if !optionShape.required[required] {
						delete(intersection, required)
					}
				}
			}
		}
		for required := range intersection {
			shape.required[required] = true
		}
	}
	return shape
}

func mergeShape(target *objectShape, source objectShape, unionRequired bool) {
	for property, propertySchema := range source.properties {
		target.properties[property] = propertySchema
	}
	if unionRequired {
		for required := range source.required {
			target.required[required] = true
		}
	}
}

func sortedSchemaNames[T any](values map[string]T) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func refName(ref string) string {
	if index := strings.LastIndex(ref, "/"); index >= 0 {
		return ref[index+1:]
	}
	return ref
}

func goName(value string) string {
	var words []string
	start := 0
	for index, r := range value {
		if r == '_' || r == '-' || r == '/' || r == '.' || r == ' ' {
			if index > start {
				words = append(words, value[start:index])
			}
			start = index + 1
		}
	}
	if start < len(value) {
		words = append(words, value[start:])
	}
	if len(words) == 0 {
		return "Value"
	}
	for index, word := range words {
		if word == "" {
			continue
		}
		runes := []rune(word)
		runes[0] = unicode.ToUpper(runes[0])
		words[index] = string(runes)
	}
	return strings.Join(words, "")
}

func tsProperty(value string) string {
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '$' {
			return strconv.Quote(value)
		}
	}
	return value
}

func tsLiteral(value any) string {
	switch typed := value.(type) {
	case string:
		return strconv.Quote(typed)
	case bool:
		return strconv.FormatBool(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return "unknown"
	}
}

func hasAdditionalProperties(value *schema) bool {
	if value == nil || len(value.AdditionalProperties) == 0 {
		return false
	}
	return string(value.AdditionalProperties) != "false"
}

func cloneSeen(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in)+1)
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneRequired(in map[string]bool) map[string]bool {
	out := make(map[string]bool, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
