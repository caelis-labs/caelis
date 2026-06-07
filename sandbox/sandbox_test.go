package sandbox

import "testing"

func TestConfigValidate(t *testing.T) {
	valid := Config{BackendName: "host"}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid, got %v", err)
	}

	noName := Config{}
	if err := noName.Validate(); err == nil {
		t.Error("expected error for missing backend name")
	}
}

func TestCommandRequestValidate(t *testing.T) {
	valid := CommandRequest{Command: "echo"}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid, got %v", err)
	}

	noCmd := CommandRequest{}
	if err := noCmd.Validate(); err == nil {
		t.Error("expected error for missing command")
	}
}

func TestConstraintsClone(t *testing.T) {
	c := Constraints{
		Paths: []PathRule{
			{Path: "/a", Access: PathAccessRead},
			{Path: "/b", Access: PathAccessWrite},
		},
	}
	cp := c.Clone()
	cp.Paths[0].Path = "/modified"
	if c.Paths[0].Path == "/modified" {
		t.Error("clone should not affect original")
	}
}
