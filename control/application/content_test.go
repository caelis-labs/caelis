package application

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func imageBytes(t *testing.T, mime string) []byte {
	t.Helper()
	var data bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	var err error
	if mime == "image/jpeg" {
		err = jpeg.Encode(&data, img, nil)
	} else {
		err = png.Encode(&data, img)
	}
	if err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func contentResult(t *testing.T, blocks ...ContentBlock) CallResult {
	t.Helper()
	if blocks == nil {
		blocks = []ContentBlock{}
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	return CallResult{Outcome: "succeeded", ResultFormat: ResultFormatContentV1, Content: raw, StructuredContent: map[string]any{"recorded": true}}
}

func contentBinding(t *testing.T, s *Store, c Connection, id string) Binding {
	t.Helper()
	b := Binding{Scope: c.Scope, SessionID: id, CreationDigest: "create-" + id, Profile: testProfile()}
	b.Profile.Tools[0].ResultFormat = ResultFormatContentV1
	b.Profile.Tools[0].OutputSchema = map[string]any{"type": "object", "properties": map[string]any{"recorded": map[string]any{"type": "boolean"}}, "required": []any{"recorded"}, "additionalProperties": false}
	if err := s.PutBinding(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	return b
}

func claimContent(t *testing.T, s *Store, b Binding, item string) Call {
	t.Helper()
	c := testCall(b)
	c.ItemID = item
	c.ConfigurationRevision = 1
	id := enqueueTest(t, s, c)
	call, err := s.ClaimCall(t.Context(), b.Scope, b.SessionID, id)
	if err != nil {
		t.Fatal(err)
	}
	return call
}

func TestContentV1CallbackProjectionAndReceipt(t *testing.T) {
	s, path := testStore(t)
	c := testConnection(t, s, 1)
	b := contentBinding(t, s, c, "session")
	call := claimContent(t, s, b, "item")
	before, after := "before screenshot", "after screenshot"
	encoded := base64.StdEncoding.EncodeToString(imageBytes(t, "image/png"))
	result := contentResult(t, ContentBlock{Type: "text", Text: &before}, ContentBlock{Type: "image", MIMEType: "image/png", Data: encoded}, ContentBlock{Type: "text", Text: &after})
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
	defer func() { _ = reopened.Close() }()
	if err = reopened.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result); err != nil {
		t.Fatalf("lost acknowledgement retry: %v", err)
	}
	_, err = reopened.ClaimCall(t.Context(), c.Scope, b.SessionID, call.ID)
	assertError(t, err, ErrAlreadyClaimed)
	configuration, err := reopened.Configuration(t.Context(), c.Scope, b.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := reopened.ToolsForConfiguration(t.Context(), b, configuration, call.Source)
	if err != nil {
		t.Fatal(err)
	}
	got, err := tools[0].Call(t.Context(), tool.Call{ID: call.CallID, Name: call.Name, Input: call.Arguments, Execution: tool.InvocationContext{SessionID: b.SessionID, TurnID: call.TurnID, ItemID: call.ItemID}})
	if err != nil || got.IsError || got.ID != call.CallID || len(got.Content) != 4 {
		t.Fatalf("projection shape: %+v, %v", got, err)
	}
	if got.Content[0].Text.Text != before || got.Content[1].Media.MimeType != "image/png" || got.Content[1].Media.Source.Data != encoded || got.Content[2].Text.Text != after {
		t.Fatal("typed order or image bytes changed")
	}
	var receipt map[string]any
	if err := json.Unmarshal(got.Content[3].JSON.Value, &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt["outcome"] != "succeeded" || receipt["receipt_id"] != call.ID || !reflect.DeepEqual(receipt["structuredContent"], result.StructuredContent) {
		t.Fatalf("effect receipt changed: %v", receipt)
	}
	provenance := got.Metadata["application_call"].(map[string]any)
	if provenance["call_id"] != call.CallID || provenance["session_id"] != b.SessionID || provenance["application_id"] != c.ApplicationID {
		t.Fatalf("native provenance changed: %v", provenance)
	}
	for _, outcome := range []string{"failed", "unknown"} {
		next := claimContent(t, reopened, b, outcome)
		result.Outcome, result.StructuredContent = outcome, nil
		if err := reopened.CompleteCall(t.Context(), b.Scope, b.SessionID, next.ID, result); err != nil {
			t.Fatal(err)
		}
		out, err := reopened.projectResult(t.Context(), next.CallContext, next.Name, ResultFormatContentV1, result)
		if err != nil || !out.IsError || !bytes.Contains(out.Content[3].JSON.Value, []byte(`"outcome":"`+outcome+`"`)) {
			t.Fatalf("lost %s receipt: %+v %v", outcome, out, err)
		}
	}
}

func TestContentV1RejectsMalformedResultsWithoutCompleting(t *testing.T) {
	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := contentBinding(t, s, c, "session")
	validImage := ContentBlock{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(imageBytes(t, "image/png"))}
	cases := map[string]CallResult{
		"legacy downgrade": testResult(),
		"null union":       {Outcome: "succeeded", ResultFormat: ResultFormatContentV1, Content: json.RawMessage(`null`)},
		"object union":     {Outcome: "succeeded", ResultFormat: ResultFormatContentV1, Content: json.RawMessage(`{}`)},
		"unknown arm":      {Outcome: "succeeded", ResultFormat: ResultFormatContentV1, Content: json.RawMessage(`[{"type":"audio","data":"AAA="}]`)},
		"extra field":      {Outcome: "succeeded", ResultFormat: ResultFormatContentV1, Content: json.RawMessage(`[{"type":"text","text":"ok","data":""}]`)},
		"null text":        {Outcome: "succeeded", ResultFormat: ResultFormatContentV1, Content: json.RawMessage(`[{"type":"text","text":null}]`)},
		"null data":        {Outcome: "succeeded", ResultFormat: ResultFormatContentV1, Content: json.RawMessage(`[{"type":"image","data":null,"mimeType":"image/png"}]`)},
		"base64":           contentResult(t, ContentBlock{Type: "image", MIMEType: "image/png", Data: "not-base64"}),
		"oversize inline":  contentResult(t, ContentBlock{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(make([]byte, MaxInlineImageBytes+1))}),
		"header only":      contentResult(t, ContentBlock{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(imageBytes(t, "image/png")[:40])}),
		"wrong MIME":       contentResult(t, ContentBlock{Type: "image", MIMEType: "image/jpeg", Data: validImage.Data}),
		"unknown MIME":     contentResult(t, ContentBlock{Type: "image", MIMEType: "application/octet-stream", Data: validImage.Data}),
		"MIME parameters":  contentResult(t, ContentBlock{Type: "image", MIMEType: "image/png;charset=utf8", Data: validImage.Data}),
		"file path":        contentResult(t, ContentBlock{Type: "resource_link", URI: "/etc/passwd", Name: "screen", MIMEType: "image/png", SHA256: strings.Repeat("a", 64)}),
		"URL":              contentResult(t, ContentBlock{Type: "resource_link", URI: "https://example.com/screen.png", Name: "screen", MIMEType: "image/png", SHA256: strings.Repeat("a", 64)}),
	}
	badSchema := contentResult(t, validImage)
	badSchema.StructuredContent = map[string]any{"recorded": "SCREEN_SECRET_SENTINEL"}
	cases["schema"] = badSchema
	wide := append([]byte(nil), imageBytes(t, "image/png")...)
	binary.BigEndian.PutUint32(wide[16:20], 32001)
	binary.BigEndian.PutUint32(wide[20:24], 501)
	binary.BigEndian.PutUint32(wide[29:33], crc32.ChecksumIEEE(wide[12:29]))
	cases["pixel limit"] = contentResult(t, ContentBlock{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(wide)})
	missingSchema := contentResult(t, validImage)
	missingSchema.StructuredContent = nil
	cases["missing structured"] = missingSchema
	blocks := make([]ContentBlock, MaxContentBlocks+1)
	for i := range blocks {
		text := ""
		blocks[i] = ContentBlock{Type: "text", Text: &text}
	}
	cases["block limit"] = contentResult(t, blocks...)
	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			call := claimContent(t, s, b, name)
			err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result)
			if err == nil {
				t.Fatal("invalid result accepted")
			}
			if strings.Contains(err.Error(), "SCREEN_SECRET_SENTINEL") || strings.Contains(err.Error(), validImage.Data) {
				t.Fatal("diagnostic leaked screenshot or structured content")
			}
			got, err := s.GetCall(t.Context(), c.Scope, b.SessionID, call.ID)
			if err != nil || got.State != "claimed" || got.Result != nil {
				t.Fatalf("invalid result changed receipt: %v %v", got.State, err)
			}
		})
	}
}

func TestContentV1ResourcesScopeExpiryAndSnapshot(t *testing.T) {
	s, _ := testStore(t)
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	c := testConnection(t, s, 1)
	b := contentBinding(t, s, c, "session")
	expires := now.Add(time.Minute)
	data := imageBytes(t, "image/png")
	r, err := s.CreateResourceWithExpiry(t.Context(), c.Scope, b.SessionID, "upload", "screen.png", "image/png", data, &expires)
	if err != nil {
		t.Fatal(err)
	}
	block := ContentBlock{Type: "resource_link", URI: ResourceURIPrefix + r.ID, Name: r.Name, MIMEType: r.MediaType, SHA256: r.SHA256}
	result := contentResult(t, block)
	call := claimContent(t, s, b, "good")
	if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result); err != nil {
		t.Fatal(err)
	}
	out, err := s.projectResult(t.Context(), call.CallContext, call.Name, ResultFormatContentV1, result)
	if err != nil || len(out.Content) != 2 || out.Content[0].Kind != model.PartKindMedia || !bytes.Contains(out.Content[1].JSON.Value, []byte(r.ID)) {
		t.Fatalf("resource not resolved with provenance: %+v %v", out, err)
	}
	otherSession := contentBinding(t, s, c, "other-session")
	foreign := testConnection(t, s, 2)
	foreignSession := contentBinding(t, s, foreign, "foreign-session")
	for _, scope := range []Binding{otherSession, foreignSession} {
		call := claimContent(t, s, scope, "cross-scope")
		assertError(t, s.CompleteCall(t.Context(), scope.Scope, scope.SessionID, call.ID, result), ErrNotFound)
	}
	for _, field := range []string{"id", "sha", "mime", "name"} {
		bad := block
		switch field {
		case "id":
			bad.URI = ResourceURIPrefix + "app-resource-missing"
		case "sha":
			bad.SHA256 = strings.Repeat("a", 64)
		case "mime":
			bad.MIMEType = "image/jpeg"
		case "name":
			bad.Name = "different.png"
		}
		call := claimContent(t, s, b, field)
		if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, contentResult(t, bad)); err == nil {
			t.Fatalf("mismatched %s accepted", field)
		}
	}
	now = expires
	if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result); err != nil {
		t.Fatalf("duplicate receipt revalidated expired resource: %v", err)
	}
	next := claimContent(t, s, b, "expired")
	assertError(t, s.CompleteCall(t.Context(), c.Scope, b.SessionID, next.ID, result), ErrResourceExpired)
	expired, err := s.projectResult(t.Context(), call.CallContext, call.Name, ResultFormatContentV1, result)
	if err != nil || !expired.IsError || len(expired.Content) != 1 || !bytes.Contains(expired.Content[0].JSON.Value, []byte(`"outcome":"succeeded"`)) || !bytes.Contains(expired.Content[0].JSON.Value, []byte(`"content_error"`)) {
		t.Fatalf("expiry changed effect outcome or silently dropped content: %+v %v", expired, err)
	}
	if out.Content[0].Media.Source.Data != base64.StdEncoding.EncodeToString(data) {
		t.Fatal("historical snapshot changed on resource expiry")
	}
}

func TestContentV1CatalogAndLegacyCompatibility(t *testing.T) {
	profile := testProfile()
	profile.Tools[0].OutputSchema = map[string]any{"type": "object"}
	assertError(t, ValidateProfile(profile), ErrInvalid)
	profile.Tools[0].ResultFormat = "content-v2"
	assertError(t, ValidateProfile(profile), ErrUnsupported)
	profile.Tools[0].ResultFormat = ResultFormatContentV1
	profile.Tools[0].OutputSchema["$ref"] = "https://example.com/schema"
	assertError(t, ValidateProfile(profile), ErrInvalid)

	s, _ := testStore(t)
	c := testConnection(t, s, 1)
	b := testBinding(t, s, c, "legacy")
	call := claimContent(t, s, b, "legacy")
	result := contentResult(t, ContentBlock{Type: "image", MIMEType: "image/png", Data: base64.StdEncoding.EncodeToString(imageBytes(t, "image/png"))})
	assertError(t, s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result), ErrInvalid)
	result.ResultFormat, result.StructuredContent = "", nil
	if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, call.ID, result); err != nil {
		t.Fatal(err)
	}
	out, err := s.projectResult(t.Context(), call.CallContext, call.Name, "", result)
	if err != nil || len(out.Content) != 1 || out.Content[0].Kind != model.PartKindText {
		t.Fatal("legacy image-shaped JSON incorrectly became visual input")
	}
	tools := b.Profile.Tools
	tools[0].ResultFormat = ResultFormatContentV1
	_, err = s.UpdateConfiguration(t.Context(), c.Scope, b.SessionID, UpdateConfigurationRequest{OperationID: "change-format", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Tools: &tools}}, nil)
	assertError(t, err, ErrConflict)
}

func TestContentV1UnknownLifecycleCannotReissue(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		for _, reason := range []string{"cancel", "revoke", "restart"} {
			t.Run(reason+map[bool]string{true: "/claimed", false: "/pending"}[claimed], func(t *testing.T) {
				s, path := testStore(t)
				c := testConnection(t, s, 1)
				b := contentBinding(t, s, c, "session")
				cc := testCall(b)
				id := enqueueTest(t, s, cc)
				if claimed {
					if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, id); err != nil {
						t.Fatal(err)
					}
				}
				switch reason {
				case "cancel":
					if err := s.CancelCall(t.Context(), c.Scope, b.SessionID, id); err != nil {
						t.Fatal(err)
					}
				case "revoke":
					if err := s.Revoke(t.Context(), c.Scope); err != nil {
						t.Fatal(err)
					}
				case "restart":
					if err := s.Close(); err != nil {
						t.Fatal(err)
					}
					var err error
					s, err = Open(path)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = s.Close() }()
				}
				call, err := s.GetCall(t.Context(), c.Scope, b.SessionID, id)
				if err != nil {
					t.Fatal(err)
				}
				result, done, err := terminalResult(call)
				if !done || (!claimed && !errors.Is(err, ErrCancelled)) {
					t.Fatalf("terminal %s %v", call.State, err)
				}
				if claimed {
					out, err := s.projectResult(t.Context(), call.CallContext, call.Name, ResultFormatContentV1, result)
					if err != nil || !out.IsError || !bytes.Contains(out.Content[0].JSON.Value, []byte(`"outcome":"unknown"`)) {
						t.Fatal("unknown effect lost")
					}
				}
				if err := s.CompleteCall(t.Context(), c.Scope, b.SessionID, id, contentResult(t)); err == nil {
					t.Fatal("late result revived uncertain effect")
				}
				if _, err := s.ClaimCall(t.Context(), c.Scope, b.SessionID, id); err == nil {
					t.Fatal("effect reissued")
				}
			})
		}
	}
}
