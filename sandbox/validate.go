package sandbox

import "fmt"

// Validate checks that the config is well-formed.
func (c Config) Validate() error {
	if c.BackendName == "" {
		return fmt.Errorf("sandbox config: BackendName is required")
	}
	return nil
}

// Validate checks that the command request is well-formed.
func (r CommandRequest) Validate() error {
	if r.Command == "" {
		return fmt.Errorf("command request: Command is required")
	}
	return nil
}

// Clone returns a deep copy of the constraints.
func (c Constraints) Clone() Constraints {
	cp := c
	if c.Paths != nil {
		cp.Paths = make([]PathRule, len(c.Paths))
		copy(cp.Paths, c.Paths)
	}
	return cp
}
