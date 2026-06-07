package model

import "fmt"

// Validate checks that the schema is well-formed.
func (s Schema) Validate() error {
	if s.Type == "" {
		return fmt.Errorf("schema: type is required")
	}
	for name, prop := range s.Properties {
		if err := prop.Validate(); err != nil {
			return fmt.Errorf("schema property %q: %w", name, err)
		}
	}
	return nil
}

// Validate checks that the request is well-formed.
func (r Request) Validate() error {
	if len(r.Messages) == 0 {
		return fmt.Errorf("request: at least one message is required")
	}
	return nil
}

// Validate checks that the tool spec is well-formed.
func (ts ToolSpec) Validate() error {
	if ts.Name == "" {
		return fmt.Errorf("tool spec: name is required")
	}
	return ts.Schema.Validate()
}
