package tool

import "testing"

func TestCallClone(t *testing.T) {
	c := Call{CallID: "c1", Name: "tool", Args: map[string]any{"k": "v"}}
	cp := c.Clone()
	cp.Args["k"] = "modified"
	if c.Args["k"] == "modified" {
		t.Error("clone should not affect original")
	}
}

func TestCallCloneNilArgs(t *testing.T) {
	c := Call{CallID: "c1", Name: "tool"}
	cp := c.Clone()
	if cp.Args != nil {
		t.Error("clone of nil args should be nil")
	}
}

func TestResultClone(t *testing.T) {
	r := Result{
		Output:   "out",
		Parts:    []ResultPart{{Kind: "text", Text: "p1"}},
		Metadata: map[string]any{"k": "v"},
	}
	cp := r.Clone()
	cp.Parts[0].Text = "modified"
	cp.Metadata["k"] = "modified"
	if r.Parts[0].Text == "modified" {
		t.Error("clone should not affect original parts")
	}
	if r.Metadata["k"] == "modified" {
		t.Error("clone should not affect original metadata")
	}
}

func TestResultCloneData(t *testing.T) {
	r := Result{
		Parts: []ResultPart{{Data: []byte{1, 2, 3}}},
	}
	cp := r.Clone()
	cp.Parts[0].Data[0] = 99
	if r.Parts[0].Data[0] == 99 {
		t.Error("clone should not affect original data")
	}
}

func TestDefinitionValidate(t *testing.T) {
	valid := Definition{Name: "test"}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid, got %v", err)
	}

	noName := Definition{}
	if err := noName.Validate(); err == nil {
		t.Error("expected error for missing name")
	}
}
