package skill

import "context"

// Registry manages skill bundles.
type Registry interface {
	// List returns all available skill bundles.
	List(context.Context) ([]Bundle, error)

	// Load returns a skill bundle by name.
	Load(context.Context, string) (Bundle, error)
}

// Bundle represents a skill bundle with its prompt and metadata.
type Bundle struct {
	Name        string
	Description string
	Content     string
	Path        string
	Metadata    map[string]any
}
