package skill

import (
	"context"
	"strings"
)

type Meta struct {
	Name        string
	Description string
	Path        string
	Source      string
	PluginID    string
	Namespace   string
	LocalName   string
}

const (
	SourceRegular = "regular"
	SourcePlugin  = "plugin"
)

type Ref struct {
	Name      string
	Path      string
	Namespace string
	PluginID  string
	LocalName string
}

type Bundle struct {
	Meta    Meta
	Root    string
	Content string
}

type DiscoverRequest struct {
	Dirs          []string
	WorkspaceDir  string
	PluginBundles []PluginBundle
}

type PluginBundle struct {
	Plugin    string
	Namespace string
	Root      string
	// PluginRoot bounds resolved Skill paths for standard packages. Root remains
	// the fixed skills/ discovery location.
	PluginRoot string
	Disabled   []string
	Enabled    bool
	// SkipInvalid keeps one malformed skill in a standard Agent Plugin from
	// hiding its other Skills or MCP servers.
	SkipInvalid bool
}

type ResolveStatus string

const (
	ResolveNotFound  ResolveStatus = "not_found"
	ResolveMatched   ResolveStatus = "matched"
	ResolveAmbiguous ResolveStatus = "ambiguous"
)

type aliasIndexEntry struct {
	indexes   []int
	canonical bool
}

// Catalog is an immutable snapshot of discovered Skill metadata. It owns
// skill-name alias resolution for tools, completion, and submission projection.
type Catalog struct {
	metas   []Meta
	aliases map[string]aliasIndexEntry
}

// NewCatalog builds a skill catalog and indexes canonical names and aliases.
// Canonical names always resolve. Local-name aliases resolve only when
// unambiguous across the catalog.
func NewCatalog(metas []Meta) Catalog {
	c := Catalog{
		metas:   append([]Meta(nil), metas...),
		aliases: make(map[string]aliasIndexEntry, len(metas)),
	}

	for i, meta := range c.metas {
		key := skillNameKey(meta.Name)
		if key == "" {
			continue
		}
		if _, exists := c.aliases[key]; !exists {
			c.aliases[key] = aliasIndexEntry{indexes: []int{i}, canonical: true}
		}
	}

	for i, meta := range c.metas {
		if strings.TrimSpace(meta.Name) == "" {
			continue
		}
		for _, alias := range nameAliases(meta) {
			key := skillNameKey(alias)
			entry := c.aliases[key]
			if !entry.canonical {
				entry.indexes = append(entry.indexes, i)
				c.aliases[key] = entry
			}
		}
	}
	return c
}

// Metas returns a copy of the catalog metadata.
func (c Catalog) Metas() []Meta {
	if len(c.metas) == 0 {
		return nil
	}
	return append([]Meta(nil), c.metas...)
}

// Resolve resolves a skill reference and distinguishes ambiguity from a
// missing name so callers can ask users or models to qualify the skill.
func (c Catalog) Resolve(name string) (Meta, ResolveStatus) {
	indexes := c.aliases[skillNameKey(name)].indexes
	switch len(indexes) {
	case 0:
		return Meta{}, ResolveNotFound
	case 1:
		return c.metas[indexes[0]], ResolveMatched
	default:
		return Meta{}, ResolveAmbiguous
	}
}

// NameLookup returns lower-cased user-enterable names mapped to canonical skill
// names. Ambiguous aliases are omitted.
func (c Catalog) NameLookup() map[string]string {
	if len(c.aliases) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.aliases))
	for key, entry := range c.aliases {
		if len(entry.indexes) == 1 {
			out[key] = strings.TrimSpace(c.metas[entry.indexes[0]].Name)
		}
	}
	return out
}

// MatchingMetas returns every catalog entry matching name by canonical name or
// alias. It is mainly used to suggest qualified names for ambiguous references.
func (c Catalog) MatchingMetas(name string) []Meta {
	indexes := c.aliases[skillNameKey(name)].indexes
	if len(indexes) == 0 {
		return nil
	}
	out := make([]Meta, 0, len(indexes))
	for _, index := range indexes {
		out = append(out, c.metas[index])
	}
	return out
}

// ClonePluginBundles returns a copy of plugin skill bundle metadata.
func ClonePluginBundles(in []PluginBundle) []PluginBundle {
	if len(in) == 0 {
		return nil
	}
	out := make([]PluginBundle, 0, len(in))
	for _, bundle := range in {
		bundle.Disabled = append([]string(nil), bundle.Disabled...)
		out = append(out, bundle)
	}
	return out
}

// MatchesName reports whether name identifies meta by canonical name or alias.
func MatchesName(meta Meta, name string) bool {
	needle := skillNameKey(name)
	if needle == "" {
		return false
	}
	for _, alias := range nameAliases(meta) {
		if skillNameKey(alias) == needle {
			return true
		}
	}
	return false
}

// nameAliases returns the user-enterable aliases for meta, including the
// canonical name, local name, and namespace-qualified local names.
func nameAliases(meta Meta) []string {
	aliases := make([]string, 0, 4)
	seen := map[string]struct{}{}
	add := func(value string) {
		value = strings.TrimSpace(value)
		key := skillNameKey(value)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		aliases = append(aliases, value)
	}

	add(meta.Name)
	localName := strings.TrimSpace(meta.LocalName)
	add(localName)
	for _, namespace := range []string{meta.Namespace, meta.PluginID} {
		namespace = strings.TrimSpace(namespace)
		if namespace != "" && localName != "" {
			add(namespace + ":" + localName)
		}
	}
	return aliases
}

func RefFromMeta(meta Meta) Ref {
	return Ref{
		Name:      strings.TrimSpace(meta.Name),
		Path:      strings.TrimSpace(meta.Path),
		Namespace: strings.TrimSpace(meta.Namespace),
		PluginID:  strings.TrimSpace(meta.PluginID),
		LocalName: strings.TrimSpace(meta.LocalName),
	}
}

func skillNameKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

type Discovery interface {
	Discover(context.Context, DiscoverRequest) ([]Meta, error)
}

type Loader interface {
	Load(context.Context, Ref) (Bundle, error)
}
