// Package skill defines the public skill discovery and loading port.
package skill

import (
	"context"
)

type Meta struct {
	Name        string
	Description string
	Path        string
}

type Ref struct {
	Name string
	Path string
}

type Bundle struct {
	Meta Meta
	Root string
}

type DiscoverRequest struct {
	Dirs         []string
	WorkspaceDir string
}

type Discovery interface {
	Discover(context.Context, DiscoverRequest) ([]Meta, error)
}

type Loader interface {
	Load(context.Context, Ref) (Bundle, error)
}
