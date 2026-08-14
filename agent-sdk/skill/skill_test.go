package skill

import "testing"

func TestCatalogResolvesCanonicalLocalAndNamespaceNames(t *testing.T) {
	catalog := NewCatalog([]Meta{{
		Name:      "superpowers:brainstorm",
		PluginID:  "superpowers-plugin",
		Namespace: "superpowers",
		LocalName: "brainstorm",
	}})

	for _, name := range []string{
		"superpowers:brainstorm",
		"brainstorm",
		"superpowers-plugin:brainstorm",
	} {
		got, status := catalog.Resolve(name)
		if status != ResolveMatched {
			t.Fatalf("Catalog.Resolve(%q) = %s, want %s", name, status, ResolveMatched)
		}
		if got.Name != "superpowers:brainstorm" {
			t.Fatalf("Catalog.Resolve(%q).Name = %q, want canonical name", name, got.Name)
		}
	}
}

func TestCatalogDoesNotPickAmbiguousLocalName(t *testing.T) {
	catalog := NewCatalog([]Meta{
		{Name: "one:brainstorm", Namespace: "one", LocalName: "brainstorm"},
		{Name: "two:brainstorm", Namespace: "two", LocalName: "brainstorm"},
	})

	if _, status := catalog.Resolve("brainstorm"); status != ResolveAmbiguous {
		t.Fatalf("Catalog.Resolve(brainstorm) = %s, want %s", status, ResolveAmbiguous)
	}
	if got, status := catalog.Resolve("two:brainstorm"); status != ResolveMatched || got.Name != "two:brainstorm" {
		t.Fatalf("Catalog.Resolve(two:brainstorm) = %#v, %s; want canonical two match", got, status)
	}
}

func TestCatalogNameLookupOmitsAmbiguousAliasesAndKeepsCanonicalNames(t *testing.T) {
	unambiguous := NewCatalog([]Meta{{
		Name:      "superpowers:brainstorm",
		Namespace: "superpowers",
		LocalName: "brainstorm",
	}}).NameLookup()
	if got := unambiguous["brainstorm"]; got != "superpowers:brainstorm" {
		t.Fatalf("unambiguous lookup[brainstorm] = %q, want canonical namespaced skill", got)
	}
	if got := unambiguous["superpowers:brainstorm"]; got != "superpowers:brainstorm" {
		t.Fatalf("unambiguous lookup[superpowers:brainstorm] = %q, want canonical namespaced skill", got)
	}

	lookup := NewCatalog([]Meta{
		{Name: "one:brainstorm", Namespace: "one", LocalName: "brainstorm"},
		{Name: "two:brainstorm", Namespace: "two", LocalName: "brainstorm"},
	}).NameLookup()

	if got := lookup["brainstorm"]; got != "" {
		t.Fatalf("lookup[brainstorm] = %q, want omitted ambiguous alias", got)
	}
	if got := lookup["one:brainstorm"]; got != "one:brainstorm" {
		t.Fatalf("lookup[one:brainstorm] = %q, want canonical name", got)
	}
}

func TestDisplayHelpersPreferLocalNameAndPluginDetail(t *testing.T) {
	meta := Meta{
		Name:        "superpowers:brainstorm",
		Description: "Generate options.",
		Source:      SourcePlugin,
		Namespace:   "superpowers",
		PluginID:    "superpowers-plugin",
		LocalName:   "brainstorm",
	}

	if got := DisplayName(meta); got != "brainstorm" {
		t.Fatalf("DisplayName() = %q, want local name", got)
	}
	if got := DisplayKind(meta); got != "Plugin" {
		t.Fatalf("DisplayKind() = %q", got)
	}
	if got := DisplayDetail(meta); got != "superpowers-plugin · Generate options." {
		t.Fatalf("DisplayDetail() = %q", got)
	}
	if got := DisplayDetail(Meta{Name: "plain"}); got != "" {
		t.Fatalf("DisplayDetail(regular) = %q", got)
	}
}

func TestCloneDiscoverRequestCopiesMutableSlices(t *testing.T) {
	req := DiscoverRequest{
		Dirs: []string{"one"},
		PluginBundles: []PluginBundle{{
			Plugin:   "plugin",
			Disabled: []string{"old"},
		}},
	}

	cloned := CloneDiscoverRequest(req)
	cloned.Dirs[0] = "changed"
	cloned.PluginBundles[0].Disabled[0] = "changed"

	if req.Dirs[0] != "one" {
		t.Fatalf("original Dirs mutated: %#v", req.Dirs)
	}
	if req.PluginBundles[0].Disabled[0] != "old" {
		t.Fatalf("original PluginBundles mutated: %#v", req.PluginBundles)
	}
}
