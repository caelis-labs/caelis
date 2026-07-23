package fs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/skill"
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
	raw, err := os.ReadFile(skillPath)
	if err != nil {
		return skill.Bundle{}, err
	}
	meta, body, err := parseSkillContent(skillPath, raw)
	if err != nil {
		return skill.Bundle{}, err
	}
	meta, err = metaForSkillRef(meta, ref)
	if err != nil {
		return skill.Bundle{}, err
	}
	return skill.Bundle{
		Meta:    meta,
		Root:    root,
		Content: strings.TrimSpace(body),
	}, nil
}

func metaForSkillRef(meta skill.Meta, ref skill.Ref) (skill.Meta, error) {
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		return meta, nil
	}
	if skill.MatchesName(meta, name) {
		meta.Name = name
		return meta, nil
	}
	namespace, localName, ok := strings.Cut(name, ":")
	if ok && strings.EqualFold(strings.TrimSpace(localName), strings.TrimSpace(meta.LocalName)) && refNamespaceMatches(ref, namespace) {
		meta.Name = name
		meta.Namespace = firstNonEmptyString(ref.Namespace, strings.TrimSpace(namespace))
		meta.PluginID = strings.TrimSpace(ref.PluginID)
		meta.LocalName = firstNonEmptyString(strings.TrimSpace(ref.LocalName), meta.LocalName)
		return meta, nil
	}
	return skill.Meta{}, fmt.Errorf("skill ref %q does not match %s", name, meta.Path)
}

func refNamespaceMatches(ref skill.Ref, namespace string) bool {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return false
	}
	return strings.EqualFold(namespace, strings.TrimSpace(ref.Namespace)) ||
		strings.EqualFold(namespace, strings.TrimSpace(ref.PluginID))
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func resolveSkillRef(ref skill.Ref) (string, string, error) {
	path := strings.TrimSpace(ref.Path)
	if path == "" {
		return "", "", fmt.Errorf("skill path is required")
	}
	root, err := RootDir(path)
	if err != nil {
		return "", "", err
	}
	skillPath := filepath.Join(root, "SKILL.md")
	return filepath.Clean(root), filepath.Clean(skillPath), nil
}

var _ skill.Discovery = Discovery{}
var _ skill.Loader = Loader{}
