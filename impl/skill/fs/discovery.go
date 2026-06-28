package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/skill"
)

type Discovery struct{}

func (Discovery) Discover(ctx context.Context, req skill.DiscoverRequest) ([]skill.Meta, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return DiscoverMetaRequest(req)
}

type Loader struct{}

func (Loader) Load(ctx context.Context, ref skill.Ref) (skill.Bundle, error) {
	if err := ctx.Err(); err != nil {
		return skill.Bundle{}, err
	}
	root, skillPath, err := resolveSkillRef(ref)
	if err != nil {
		return skill.Bundle{}, err
	}
	metas, err := DiscoverMeta([]string{filepath.Dir(root)}, "")
	if err != nil {
		return skill.Bundle{}, err
	}
	name := strings.TrimSpace(ref.Name)
	for _, meta := range metas {
		if filepath.Clean(meta.Path) == skillPath || (name != "" && strings.EqualFold(meta.Name, name)) {
			return skill.Bundle{
				Meta: meta,
				Root: root,
			}, nil
		}
	}
	return skill.Bundle{}, fmt.Errorf("skill %q not found at %s", name, root)
}

func resolveSkillRef(ref skill.Ref) (string, string, error) {
	path := strings.TrimSpace(ref.Path)
	if path == "" {
		return "", "", fmt.Errorf("skill path is required")
	}
	resolved, err := ResolvePath(path)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", "", err
	}
	root := resolved
	if !info.IsDir() || strings.EqualFold(filepath.Base(resolved), "SKILL.md") {
		root = filepath.Dir(resolved)
	}
	skillPath := filepath.Join(root, "SKILL.md")
	if _, err := os.Stat(skillPath); err != nil {
		return "", "", err
	}
	return filepath.Clean(root), filepath.Clean(skillPath), nil
}

var _ skill.Discovery = Discovery{}
var _ skill.Loader = Loader{}
