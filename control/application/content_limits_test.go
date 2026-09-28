package application

import (
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestContentV1AcceptedReceiptNeverTruncates(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := Binding{Scope: c.Scope, SessionID: "session", CreationDigest: "create", Profile: testProfile()}
	b.Profile.Tools[0].ResultFormat = ResultFormatContentV1
	if err := s.PutBinding(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	call := claimContent(t, s, b, "text-limit")
	text := strings.Repeat("截图<>&\n", 1000)
	result := contentResult(t, ContentBlock{Type: "text", Text: &text}, ContentBlock{Type: "image", MIMEType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(imageBytes(t, "image/jpeg"))})
	result.StructuredContent = map[string]any{"detail": strings.Repeat("<>&截图", 500)}
	if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result); err != nil {
		t.Fatal(err)
	}
	projected, err := s.projectResult(t.Context(), call.CallContext, call.Name, ResultFormatContentV1, result)
	if err != nil {
		t.Fatal(err)
	}
	bounded, info := tool.TruncateResultWithInfo(projected, tool.DefaultTruncationPolicy())
	if info.Truncated || !reflect.DeepEqual(bounded.Content, projected.Content) {
		t.Fatal("accepted text/structured/image content changed under canonical truncation")
	}
	for _, kind := range []string{"text", "structured", "serialized-escaping"} {
		t.Run(kind, func(t *testing.T) {
			call := claimContent(t, s, b, kind)
			result := contentResult(t)
			switch kind {
			case "text":
				text := strings.Repeat("x", MaxResultTextBytes)
				result = contentResult(t, ContentBlock{Type: "text", Text: &text})
			case "structured":
				result.StructuredContent = map[string]any{"data": strings.Repeat("x", MaxResultTextBytes)}
			case "serialized-escaping":
				result.StructuredContent = map[string]any{"data": strings.Repeat("<", MaxResultTextBytes/5)}
			}
			assertError(t, s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result), ErrInvalid)
		})
	}
}

func TestContentV1ResourceAndAggregateImageLimits(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := contentBinding(t, s, c, "session")
	expires := time.Now().Add(time.Minute)
	// Trailing padding bounds compressed bytes independently of raster pixels.
	data := make([]byte, MaxResourceBytes)
	copy(data, imageBytes(t, "image/png"))
	r, err := s.CreateResourceWithExpiry(t.Context(), c.Scope, b.SessionID, "large", "screen.png", "image/png", data, &expires)
	if err != nil {
		t.Fatal(err)
	}
	block := ContentBlock{Type: "resource_link", URI: ResourceURIPrefix + r.ID, Name: r.Name, MIMEType: r.MediaType, SHA256: r.SHA256}
	call := claimContent(t, s, b, "aggregate")
	result := contentResult(t, block, block, block)
	assertError(t, s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result), ErrInvalid)
	_, err = s.CreateResourceWithExpiry(t.Context(), c.Scope, b.SessionID, "oversize", "screen.png", "image/png", make([]byte, MaxResourceBytes+1), &expires)
	assertError(t, err, ErrInvalid)
	permanent, err := s.CreateResource(t.Context(), c.Scope, b.SessionID, "permanent", "screen.png", "image/png", imageBytes(t, "image/png"))
	if err != nil {
		t.Fatal(err)
	}
	block.URI, block.SHA256 = ResourceURIPrefix+permanent.ID, permanent.SHA256
	assertError(t, s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, contentResult(t, block)), ErrInvalid)
}

func TestContentSchemaNumericView(t *testing.T) {
	for _, input := range []string{"1.5", "0.1", "1.0", "18446744073709551615", "0e-9999999"} {
		value := map[string]any{"nested": []any{json.Number(input)}}
		if _, err := schemaValue(value); err != nil {
			t.Fatalf("representable numeric value %s: %v", input, err)
		}
		if value["nested"].([]any)[0] != json.Number(input) {
			t.Fatal("validation changed authoritative content")
		}
	}
	for _, input := range []string{"1e-9999999", "1e9999999", "0.12345678901234567890123456789"} {
		if _, err := schemaValue(json.Number(input)); err == nil {
			t.Fatalf("accepted unrepresentable validation number %s", input)
		}
	}
}

func TestContentV1StructuredNumbersRemainExact(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := Binding{Scope: c.Scope, SessionID: "session", CreationDigest: "create", Profile: testProfile()}
	b.Profile.Tools[0].ResultFormat = ResultFormatContentV1
	b.Profile.Tools[0].OutputSchema = map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []any{"id"}}
	if err := s.PutBinding(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	call := claimContent(t, s, b, "exact-number")
	var result CallResult
	if err := json.Unmarshal([]byte(`{"outcome":"succeeded","result_format":"content-v1","content":[],"structuredContent":{"id":9007199254740993}}`), &result); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result); err != nil {
		t.Fatal(err)
	}
	stored, err := s.GetCall(t.Context(), c.Scope, b.SessionID, call.ID)
	if err != nil || stored.Result == nil || stored.Result.StructuredContent["id"] != json.Number("9007199254740993") {
		t.Fatalf("structured number changed in receipt: %+v %v", stored.Result, err)
	}
	projected, err := s.projectResult(t.Context(), call.CallContext, call.Name, ResultFormatContentV1, *stored.Result)
	if err != nil || !strings.Contains(string(projected.Content[0].JSON.Value), `9007199254740993`) {
		t.Fatal("structured number changed in native content")
	}
}

func TestContentV1EmptyStructuredObjectSurvivesReceipt(t *testing.T) {
	for _, schema := range []bool{false, true} {
		s, path := testStore(t)
		c := testConnection(t, s, 1)
		b := Binding{Scope: c.Scope, SessionID: "session", CreationDigest: "create", Profile: testProfile()}
		b.Profile.Tools[0].ResultFormat = ResultFormatContentV1
		if schema {
			b.Profile.Tools[0].OutputSchema = map[string]any{"type": "object", "additionalProperties": false}
		}
		if err := s.PutBinding(t.Context(), b); err != nil {
			t.Fatal(err)
		}
		call := claimContent(t, s, b, "empty-structured")
		result := contentResult(t)
		result.StructuredContent = map[string]any{}
		if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		got, err := reopened.GetCall(t.Context(), c.Scope, b.SessionID, call.ID)
		if err != nil || got.Result == nil || got.Result.StructuredContent == nil || len(got.Result.StructuredContent) != 0 {
			t.Fatalf("empty structured object lost: %+v %v", got.Result, err)
		}
		if err := reopened.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result); err != nil {
			t.Fatal(err)
		}
		projected, err := reopened.projectResult(t.Context(), call.CallContext, call.Name, ResultFormatContentV1, *got.Result)
		if err != nil || !strings.Contains(string(projected.Content[0].JSON.Value), `"structuredContent":{}`) {
			t.Fatal("empty structured object absent from model receipt")
		}
	}
}

func TestContentV1ResultRejectsNullNegotiation(t *testing.T) {
	for _, raw := range []string{
		`{"outcome":"succeeded","content":[],"result_format":null}`,
		`{"outcome":"succeeded","content":[],"result_format":"content-v1","structuredContent":null}`,
		`{"outcome":"succeeded","content":[],"result_format":"content-v1","structuredContent":[]}`,
		`{"outcome":"succeeded","content":[],"result_format":"content-v1","surprise":true}`,
	} {
		var result CallResult
		if err := json.Unmarshal([]byte(raw), &result); err == nil {
			t.Fatal("invalid typed result decoded")
		}
	}
}
