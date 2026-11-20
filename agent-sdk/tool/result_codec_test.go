package tool

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

func TestResultContentRoundTrip(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{"n":9007199254740993123456789,"exp":1e1000}`)} {
		result := Result{Content: []ContentBlock{Text("before")}}
		if raw != nil {
			result.Content = append(result.Content, StructuredContent{Value: raw})
		}
		result.Content = append(result.Content,
			Image{Data: []byte("image-bytes"), MIMEType: "image/png"},
			Resource{URI: "file:///empty", Text: new("")}, Text("after"))
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Result
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, result) {
			t.Fatalf("round trip = %#v, want %#v", decoded, result)
		}
		value, present := decoded.Structured()
		if present != (raw != nil) || !bytes.Equal(value, raw) {
			t.Fatalf("structured value = %s, %t, want %s, %t", value, present, raw, raw != nil)
		}
		if bytes.Contains(encoded, []byte(`"structured_content"`)) || (raw != nil && bytes.Count(encoded, raw) != 1) {
			t.Fatalf("unexpected structured encoding: %s", encoded)
		}
	}
}

func TestResultRejectsMultipleStructuredBlocks(t *testing.T) {
	result := Result{Content: []ContentBlock{StructuredContent{Value: json.RawMessage(`null`)}, Text("between"), StructuredContent{Value: json.RawMessage(`7`)}}}
	if _, err := json.Marshal(result); err == nil {
		t.Fatal("marshal accepted two structured blocks")
	}
	const wire = `{"content":[{"type":"structured","value":null},{"type":"text","text":"between"},{"type":"structured","value":7}]}`
	if err := json.Unmarshal([]byte(wire), &result); err == nil {
		t.Fatal("unmarshal accepted two structured blocks")
	}
}

func TestResultReadsHistoricalStructuredIndex(t *testing.T) {
	const raw = `{"n":9007199254740993123456789}`
	const parts = `{"kind":"text","text":{"text":"before"}},{"kind":"json","json":{"value":` + raw + `}}`
	var result Result
	if err := json.Unmarshal([]byte(`{"content":[`+parts+`],"structured_content":{"value":`+raw+`,"content_index":1}}`), &result); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"content":[{"type":"text","text":"before"},{"type":"structured","value":` + raw + `}]}`; string(encoded) != want {
		t.Fatalf("canonical result = %s, want %s", encoded, want)
	}
	if err := json.Unmarshal([]byte(`{"content":[`+parts+`]}`), &result); err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Structured(); ok {
		t.Fatal("unmarked historical JSON became business data")
	}
	if projected := ModelParts(result.Content); len(projected) != 2 || projected[1].Kind != model.PartKindJSON || string(projected[1].JSON.Value) != raw {
		t.Fatalf("unmarked historical JSON lost in model projection: %+v", projected)
	}
}
