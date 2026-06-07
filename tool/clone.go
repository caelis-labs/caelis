package tool

// Clone returns a deep copy of the call.
func (c Call) Clone() Call {
	cp := c
	if c.Args != nil {
		cp.Args = make(map[string]any, len(c.Args))
		for k, v := range c.Args {
			cp.Args[k] = v
		}
	}
	return cp
}

// Clone returns a deep copy of the result.
func (r Result) Clone() Result {
	cp := r
	if r.Parts != nil {
		cp.Parts = make([]ResultPart, len(r.Parts))
		for i, p := range r.Parts {
			cp.Parts[i] = p.Clone()
		}
	}
	if r.Metadata != nil {
		cp.Metadata = make(map[string]any, len(r.Metadata))
		for k, v := range r.Metadata {
			cp.Metadata[k] = v
		}
	}
	return cp
}

// Clone returns a deep copy of the result part.
func (p ResultPart) Clone() ResultPart {
	cp := p
	if p.Data != nil {
		cp.Data = make([]byte, len(p.Data))
		copy(cp.Data, p.Data)
	}
	return cp
}

// Validate checks that the definition is well-formed.
func (d Definition) Validate() error {
	if d.Name == "" {
		return ErrInvalidDefinition("name is required")
	}
	return nil
}

// ErrInvalidDefinition is returned when a tool definition fails validation.
type ErrInvalidDefinition string

func (e ErrInvalidDefinition) Error() string {
	return "invalid tool definition: " + string(e)
}
