package skill

import (
	"context"
	"fmt"
	"strings"
)

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

// NewRegistry returns a filesystem-backed skill registry over discovery dirs.
func NewRegistry(dirs []string) Registry {
	return &fsRegistry{dirs: append([]string(nil), dirs...)}
}

type fsRegistry struct {
	dirs []string
}

func (r *fsRegistry) List(context.Context) ([]Bundle, error) {
	return Discover(r.dirs)
}

func (r *fsRegistry) Load(ctx context.Context, name string) (Bundle, error) {
	name = strings.TrimSpace(name)
	bundles, err := r.List(ctx)
	if err != nil {
		return Bundle{}, err
	}
	for _, one := range bundles {
		if strings.EqualFold(strings.TrimSpace(one.Name), name) {
			return one, nil
		}
	}
	return Bundle{}, fmt.Errorf("skill: %q not found", name)
}
