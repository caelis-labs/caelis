package model

import (
	"testing"
)

func TestMessageClone(t *testing.T) {
	m := Message{
		Role: RoleUser,
		Content: []Part{
			{Text: "hello"},
			{ToolUse: &ToolUse{CallID: "c1", Name: "tool", Args: map[string]any{"k": "v"}}},
		},
	}
	cp := m.Clone()
	cp.Content[0].Text = "modified"
	cp.Content[1].ToolUse.Args["k"] = "modified"
	if m.Content[0].Text == "modified" {
		t.Error("clone should not affect original text")
	}
	if m.Content[1].ToolUse.Args["k"] == "modified" {
		t.Error("clone should not affect original tool args")
	}
}

func TestMessageCloneInlineData(t *testing.T) {
	m := Message{
		Role: RoleUser,
		Content: []Part{
			{InlineData: &InlineData{MIMEType: "image/png", Data: []byte{1, 2, 3}}},
		},
	}
	cp := m.Clone()
	cp.Content[0].InlineData.Data[0] = 99
	if m.Content[0].InlineData.Data[0] == 99 {
		t.Error("clone should not affect original inline data")
	}
}

func TestMessageNormalize(t *testing.T) {
	m := Message{
		Role: RoleUser,
		Content: []Part{
			{Text: "hello"},
			{Text: " world"},
			{}, // empty part
			{Text: "!"},
		},
	}
	n := m.Normalize()
	if len(n.Content) != 2 {
		t.Fatalf("got %d parts, want 2", len(n.Content))
	}
	if n.Content[0].Text != "hello world" {
		t.Errorf("got %q, want %q", n.Content[0].Text, "hello world")
	}
	if n.Content[1].Text != "!" {
		t.Errorf("got %q, want %q", n.Content[1].Text, "!")
	}
}

func TestMessageNormalizeEmpty(t *testing.T) {
	m := Message{Role: RoleUser}
	n := m.Normalize()
	if len(n.Content) != 0 {
		t.Errorf("got %d parts, want 0", len(n.Content))
	}
}

func TestMessageTextContent(t *testing.T) {
	m := Message{
		Content: []Part{
			{Text: "hello"},
			{Text: " world"},
		},
	}
	if got := m.TextContent(); got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

func TestSchemaValidate(t *testing.T) {
	valid := Schema{Type: "object", Properties: map[string]Schema{
		"name": {Type: "string"},
	}}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid, got %v", err)
	}

	noType := Schema{}
	if err := noType.Validate(); err == nil {
		t.Error("expected error for missing type")
	}

	badProp := Schema{Type: "object", Properties: map[string]Schema{
		"bad": {},
	}}
	if err := badProp.Validate(); err == nil {
		t.Error("expected error for invalid property")
	}
}

func TestRequestValidate(t *testing.T) {
	valid := Request{Messages: []Message{{Role: RoleUser, Content: []Part{{Text: "hi"}}}}}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid, got %v", err)
	}

	empty := Request{}
	if err := empty.Validate(); err == nil {
		t.Error("expected error for empty messages")
	}
}

func TestToolSpecValidate(t *testing.T) {
	valid := ToolSpec{Name: "test", Schema: Schema{Type: "object"}}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid, got %v", err)
	}

	noName := ToolSpec{Schema: Schema{Type: "object"}}
	if err := noName.Validate(); err == nil {
		t.Error("expected error for missing name")
	}
}
