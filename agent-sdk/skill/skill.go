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
	Disabled  []string
	Enabled   bool
}

type ResolveStatus string

const (
	ResolveNotFound  ResolveStatus = "not_found"
	ResolveMatched   ResolveStatus = "matched"
	ResolveAmbiguous ResolveStatus = "ambiguous"
)

type aliasIndexEntry struct {
	indexes []int
}

// Catalog is the immutable skill metadata snapshot assembled with the system
// prompt. It owns skill-name alias resolution for tools, completion, and
// submission projection.
type Catalog struct {
	metas   []Meta
	aliases map[string]aliasIndexEntry
	lookup  map[string]string
}

// NewCatalog builds a skill catalog and precomputes canonical and alias lookups.
// Canonical names always resolve. Local-name aliases resolve only when
// unambiguous across the catalog.
func NewCatalog(metas []Meta) Catalog {
	c := Catalog{
		metas: make([]Meta, 0, len(metas)),
	}
	c.metas = append(c.metas, metas...)
	if len(c.metas) == 0 {
		return c
	}
	c.aliases = make(map[string]aliasIndexEntry, len(c.metas))
	c.lookup = make(map[string]string, len(c.metas))
	canonicalKeys := map[string]struct{}{}

	for i, meta := range c.metas {
		name := strings.TrimSpace(meta.Name)
		key := skillNameKey(name)
		if key == "" {
			continue
		}
		if _, exists := c.aliases[key]; !exists {
			c.aliases[key] = aliasIndexEntry{indexes: []int{i}}
			c.lookup[key] = name
		}
		canonicalKeys[key] = struct{}{}
	}

	for i, meta := range c.metas {
		canonical := strings.TrimSpace(meta.Name)
		if canonical == "" {
			continue
		}
		for _, alias := range NameAliases(meta) {
			key := skillNameKey(alias)
			if key == "" {
				continue
			}
			if _, canonicalKey := canonicalKeys[key]; canonicalKey {
				continue
			}
			entry := c.aliases[key]
			entry.indexes = appendUniqueIndex(entry.indexes, i)
			c.aliases[key] = entry
			if len(entry.indexes) == 1 {
				c.lookup[key] = canonical
			} else {
				delete(c.lookup, key)
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
	needle := skillNameKey(name)
	if needle == "" {
		return Meta{}, ResolveNotFound
	}
	entry, ok := c.aliases[needle]
	if !ok || len(entry.indexes) == 0 {
		return Meta{}, ResolveNotFound
	}
	if len(entry.indexes) > 1 {
		return Meta{}, ResolveAmbiguous
	}
	index := entry.indexes[0]
	if index < 0 || index >= len(c.metas) {
		return Meta{}, ResolveNotFound
	}
	return c.metas[index], ResolveMatched
}

// ResolveBool resolves a skill reference and reports whether it matched.
func (c Catalog) ResolveBool(name string) (Meta, bool) {
	meta, status := c.Resolve(name)
	return meta, status == ResolveMatched
}

// NameLookup returns lower-cased user-enterable names mapped to canonical skill
// names. Ambiguous aliases are omitted.
func (c Catalog) NameLookup() map[string]string {
	if len(c.lookup) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.lookup))
	for key, value := range c.lookup {
		out[key] = value
	}
	return out
}

// MatchingMetas returns every catalog entry matching name by canonical name or
// alias. It is mainly used to suggest qualified names for ambiguous references.
func (c Catalog) MatchingMetas(name string) []Meta {
	needle := skillNameKey(name)
	if needle == "" {
		return nil
	}
	entry, ok := c.aliases[needle]
	if !ok || len(entry.indexes) == 0 {
		return nil
	}
	out := make([]Meta, 0, len(entry.indexes))
	for _, index := range entry.indexes {
		if index < 0 || index >= len(c.metas) {
			continue
		}
		out = append(out, c.metas[index])
	}
	return out
}

// CloneDiscoverRequest returns a deep-enough copy of a discovery request for
// callers that store or reuse request state across tool invocations.
func CloneDiscoverRequest(req DiscoverRequest) DiscoverRequest {
	out := req
	out.Dirs = append([]string(nil), req.Dirs...)
	out.PluginBundles = ClonePluginBundles(req.PluginBundles)
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
	return skillNameKey(meta.Name) == needle || metaAliasMatches(meta, needle)
}

// NameAliases returns the user-enterable aliases for meta, including the
// canonical name, local name, and namespace-qualified local names.
func NameAliases(meta Meta) []string {
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

func DisplayName(meta Meta) string {
	if localName := strings.TrimSpace(meta.LocalName); localName != "" {
		return localName
	}
	return strings.TrimSpace(meta.Name)
}

func DisplayDetail(meta Meta) string {
	description := strings.Join(strings.Fields(strings.TrimSpace(meta.Description)), " ")
	isPlugin := strings.EqualFold(strings.TrimSpace(meta.Source), SourcePlugin) || strings.TrimSpace(meta.PluginID) != "" || strings.TrimSpace(meta.Namespace) != ""

	if isPlugin {
		source := strings.TrimSpace(meta.PluginID)
		if source == "" {
			source = strings.TrimSpace(meta.Namespace)
		}
		if description != "" {
			if source != "" {
				return source + " · " + description
			}
			return description
		}
		return source
	}

	return description
}

// DisplayKind returns the completion badge label for skill metadata.
func DisplayKind(meta Meta) string {
	if strings.EqualFold(strings.TrimSpace(meta.Source), SourcePlugin) || strings.TrimSpace(meta.PluginID) != "" || strings.TrimSpace(meta.Namespace) != "" {
		return "Plugin"
	}
	return "Skill"
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

func metaAliasMatches(meta Meta, needle string) bool {
	for _, alias := range NameAliases(meta) {
		if skillNameKey(alias) == needle {
			return true
		}
	}
	return false
}

func skillNameKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func appendUniqueIndex(values []int, value int) []int {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

type Discovery interface {
	Discover(context.Context, DiscoverRequest) ([]Meta, error)
}

type Loader interface {
	Load(context.Context, Ref) (Bundle, error)
}
