// Package resources discovers Caelis extension resources for application
// services. It indexes resource metadata only; runtime registration stays in
// the app composition layer.
package resources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/plugin"
)

type Request struct {
	WorkspaceDir  string
	HomeDir       string
	PluginSources []config.Plugin
	SkillDirs     []string
}

type Catalog struct {
	Plugins        []PluginResource            `json:"plugins,omitempty"`
	ModelProviders []plugin.FactoryAlias       `json:"model_providers,omitempty"`
	Stores         []plugin.FactoryAlias       `json:"stores,omitempty"`
	Sandboxes      []plugin.FactoryAlias       `json:"sandbox_backends,omitempty"`
	Tools          []plugin.FactoryAlias       `json:"tools,omitempty"`
	Prompts        []plugin.PromptFragment     `json:"prompts,omitempty"`
	Skills         []plugin.SkillDescriptor    `json:"skills,omitempty"`
	ACPAgents      []plugin.ACPAgentDescriptor `json:"acp_agents,omitempty"`
	RendererHints  []plugin.RendererHint       `json:"renderer_hints,omitempty"`
	AgentFiles     []AgentFile                 `json:"agent_files,omitempty"`
	Diagnostics    []Diagnostic                `json:"diagnostics,omitempty"`
}

type PluginResource struct {
	Manifest plugin.Manifest `json:"manifest"`
	Path     string          `json:"path,omitempty"`
}

type AgentFile struct {
	ID       string `json:"id,omitempty"`
	Path     string `json:"path,omitempty"`
	Scope    string `json:"scope,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Text     string `json:"text,omitempty"`
}

const (
	DiagnosticInfo    = "info"
	DiagnosticWarning = "warning"
	DiagnosticError   = "error"
)

type Diagnostic struct {
	Severity string            `json:"severity,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	ID       string            `json:"id,omitempty"`
	Path     string            `json:"path,omitempty"`
	Message  string            `json:"message,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}

func Discover(ctx context.Context, req Request) (Catalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Catalog{}, err
	}
	workspaceDir, err := cleanOptionalPath(req.WorkspaceDir, req.HomeDir)
	if err != nil {
		return Catalog{}, err
	}
	homeDir, err := homePath(req.HomeDir)
	if err != nil {
		return Catalog{}, err
	}

	var catalog Catalog
	if err := discoverPluginSources(ctx, req.PluginSources, homeDir, &catalog); err != nil {
		return Catalog{}, err
	}
	if err := discoverAgentFiles(ctx, homeDir, workspaceDir, &catalog); err != nil {
		return Catalog{}, err
	}
	if err := discoverSkills(ctx, SkillRoots(homeDir, workspaceDir, req.SkillDirs), &catalog); err != nil {
		return Catalog{}, err
	}
	sortCatalog(&catalog)
	return CloneCatalog(catalog), nil
}

func CloneCatalog(in Catalog) Catalog {
	out := Catalog{
		Plugins:        make([]PluginResource, 0, len(in.Plugins)),
		ModelProviders: cloneAliases(in.ModelProviders),
		Stores:         cloneAliases(in.Stores),
		Sandboxes:      cloneAliases(in.Sandboxes),
		Tools:          cloneAliases(in.Tools),
		Prompts:        make([]plugin.PromptFragment, 0, len(in.Prompts)),
		Skills:         make([]plugin.SkillDescriptor, 0, len(in.Skills)),
		ACPAgents:      make([]plugin.ACPAgentDescriptor, 0, len(in.ACPAgents)),
		RendererHints:  make([]plugin.RendererHint, 0, len(in.RendererHints)),
		AgentFiles:     slices.Clone(in.AgentFiles),
		Diagnostics:    make([]Diagnostic, 0, len(in.Diagnostics)),
	}
	for _, item := range in.Plugins {
		item.Manifest = cloneManifest(item.Manifest)
		out.Plugins = append(out.Plugins, item)
	}
	for _, item := range in.Prompts {
		item.Paths = slices.Clone(item.Paths)
		out.Prompts = append(out.Prompts, item)
	}
	for _, item := range in.Skills {
		item.Paths = slices.Clone(item.Paths)
		out.Skills = append(out.Skills, item)
	}
	for _, item := range in.ACPAgents {
		item.Args = slices.Clone(item.Args)
		item.Env = maps.Clone(item.Env)
		item.Roles = slices.Clone(item.Roles)
		out.ACPAgents = append(out.ACPAgents, item)
	}
	for _, item := range in.RendererHints {
		item.Meta = maps.Clone(item.Meta)
		out.RendererHints = append(out.RendererHints, item)
	}
	for _, item := range in.Diagnostics {
		item.Meta = maps.Clone(item.Meta)
		out.Diagnostics = append(out.Diagnostics, item)
	}
	return out
}

type manifestFile struct {
	plugin.Manifest
	ModelProviders []plugin.FactoryAlias       `json:"model_providers,omitempty"`
	Stores         []plugin.FactoryAlias       `json:"stores,omitempty"`
	Sandboxes      []plugin.FactoryAlias       `json:"sandbox_backends,omitempty"`
	Tools          []plugin.FactoryAlias       `json:"tools,omitempty"`
	Prompts        []plugin.PromptFragment     `json:"prompts,omitempty"`
	Skills         []plugin.SkillDescriptor    `json:"skills,omitempty"`
	ACPAgents      []plugin.ACPAgentDescriptor `json:"acp_agents,omitempty"`
	RendererHints  []plugin.RendererHint       `json:"renderer_hints,omitempty"`
}

func discoverPluginSources(ctx context.Context, sources []config.Plugin, homeDir string, catalog *Catalog) error {
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !source.Enabled {
			addDiagnostic(catalog, Diagnostic{
				Severity: DiagnosticInfo,
				Kind:     "plugin",
				ID:       strings.TrimSpace(source.ID),
				Path:     strings.TrimSpace(source.Source),
				Message:  "plugin disabled",
			})
			continue
		}
		rawPath := strings.TrimSpace(source.Source)
		if rawPath == "" {
			return fmt.Errorf("app/resources: enabled plugin %q has empty source", source.ID)
		}
		path, err := cleanPath(rawPath, homeDir)
		if err != nil {
			return err
		}
		if info, err := os.Stat(path); err != nil {
			return err
		} else if info.IsDir() {
			path = filepath.Join(path, "plugin.json")
		}
		item, err := readPluginManifest(path)
		if err != nil {
			return err
		}
		if item.ID == "" {
			item.ID = strings.TrimSpace(source.ID)
		}
		if item.ID == "" {
			return fmt.Errorf("app/resources: plugin manifest %s missing id", path)
		}
		baseDir := filepath.Dir(path)
		manifest := cloneManifest(item.Manifest)
		catalog.Plugins = append(catalog.Plugins, PluginResource{Manifest: manifest, Path: path})
		addDiagnostic(catalog, Diagnostic{
			Severity: DiagnosticInfo,
			Kind:     "plugin",
			ID:       manifest.ID,
			Path:     path,
			Message:  "plugin loaded",
		})
		modelProviders, err := pluginAliases(manifest.ID, "model provider", item.ModelProviders)
		if err != nil {
			return err
		}
		stores, err := pluginAliases(manifest.ID, "store", item.Stores)
		if err != nil {
			return err
		}
		sandboxes, err := pluginAliases(manifest.ID, "sandbox backend", item.Sandboxes)
		if err != nil {
			return err
		}
		tools, err := pluginAliases(manifest.ID, "tool", item.Tools)
		if err != nil {
			return err
		}
		catalog.ModelProviders = append(catalog.ModelProviders, modelProviders...)
		catalog.Stores = append(catalog.Stores, stores...)
		catalog.Sandboxes = append(catalog.Sandboxes, sandboxes...)
		catalog.Tools = append(catalog.Tools, tools...)
		for i, prompt := range item.Prompts {
			prompt.ID = firstNonEmpty(prompt.ID, fmt.Sprintf("%s.prompt.%d", manifest.ID, i+1))
			prompt.Scope = firstNonEmpty(prompt.Scope, "system")
			prompt.Paths = resolveRelativePaths(baseDir, prompt.Paths)
			catalog.Prompts = append(catalog.Prompts, prompt)
		}
		for _, skill := range item.Skills {
			skill.Paths = resolveRelativePaths(baseDir, skill.Paths)
			catalog.Skills = append(catalog.Skills, skill)
		}
		agents, err := pluginACPAgents(baseDir, manifest.ID, item.ACPAgents)
		if err != nil {
			return err
		}
		catalog.ACPAgents = append(catalog.ACPAgents, agents...)
		catalog.RendererHints = append(catalog.RendererHints, cloneRendererHints(item.RendererHints)...)
	}
	return nil
}

func readPluginManifest(path string) (manifestFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return manifestFile{}, err
	}
	var item manifestFile
	if err := json.Unmarshal(raw, &item); err != nil {
		return manifestFile{}, fmt.Errorf("app/resources: parse plugin manifest %s: %w", path, err)
	}
	item.Manifest = cloneManifest(item.Manifest)
	return item, nil
}

func discoverAgentFiles(ctx context.Context, homeDir string, workspaceDir string, catalog *Catalog) error {
	candidates := []AgentFile{}
	if homeDir != "" {
		candidates = append(candidates, AgentFile{
			ID:       "agents.global",
			Path:     filepath.Join(homeDir, ".agents", "AGENTS.md"),
			Scope:    "system",
			Priority: 100,
		})
	}
	if workspaceDir != "" {
		candidates = append(candidates, AgentFile{
			ID:       "agents.workspace",
			Path:     filepath.Join(workspaceDir, "AGENTS.md"),
			Scope:    "system",
			Priority: 200,
		})
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		text, err := readOptionalText(candidate.Path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(text) == "" {
			addDiagnostic(catalog, Diagnostic{
				Severity: DiagnosticInfo,
				Kind:     "agent_file",
				ID:       candidate.ID,
				Path:     candidate.Path,
				Message:  "agent instruction file not found or empty",
			})
			continue
		}
		candidate.Text = text
		catalog.AgentFiles = append(catalog.AgentFiles, candidate)
		addDiagnostic(catalog, Diagnostic{
			Severity: DiagnosticInfo,
			Kind:     "agent_file",
			ID:       candidate.ID,
			Path:     candidate.Path,
			Message:  "agent instruction file loaded",
		})
		catalog.Prompts = append(catalog.Prompts, plugin.PromptFragment{
			ID:       candidate.ID,
			Scope:    candidate.Scope,
			Priority: candidate.Priority,
			Text:     text,
			Paths:    []string{candidate.Path},
		})
	}
	return nil
}

func discoverSkills(ctx context.Context, roots []string, catalog *Catalog) error {
	byName := map[string]plugin.SkillDescriptor{}
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return err
		}
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		info, err := os.Stat(root)
		if errors.Is(err, os.ErrNotExist) {
			addDiagnostic(catalog, Diagnostic{
				Severity: DiagnosticInfo,
				Kind:     "skill_root",
				Path:     root,
				Message:  "skill root not found",
			})
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			addDiagnostic(catalog, Diagnostic{
				Severity: DiagnosticWarning,
				Kind:     "skill_root",
				Path:     root,
				Message:  "skill root is not a directory",
			})
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		loaded := 0
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			path := filepath.Join(root, entry.Name(), "SKILL.md")
			info, err := os.Stat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if info.IsDir() {
				continue
			}
			skill, err := readSkillDescriptor(path)
			if err != nil {
				return err
			}
			if strings.TrimSpace(skill.Name) == "" {
				skill.Name = entry.Name()
			}
			skill.Paths = []string{path}
			if previous, ok := byName[strings.ToLower(skill.Name)]; ok {
				addDiagnostic(catalog, Diagnostic{
					Severity: DiagnosticInfo,
					Kind:     "skill",
					ID:       strings.TrimSpace(skill.Name),
					Path:     path,
					Message:  "skill overrides earlier discovery",
					Meta:     map[string]string{"previous_path": firstPath(previous.Paths)},
				})
			} else {
				addDiagnostic(catalog, Diagnostic{
					Severity: DiagnosticInfo,
					Kind:     "skill",
					ID:       strings.TrimSpace(skill.Name),
					Path:     path,
					Message:  "skill loaded",
				})
			}
			byName[strings.ToLower(skill.Name)] = skill
			loaded++
		}
		addDiagnostic(catalog, Diagnostic{
			Severity: DiagnosticInfo,
			Kind:     "skill_root",
			Path:     root,
			Message:  "skill root scanned",
			Meta:     map[string]string{"skills": fmt.Sprintf("%d", loaded)},
		})
	}
	keys := make([]string, 0, len(byName))
	for key := range byName {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		catalog.Skills = append(catalog.Skills, byName[key])
	}
	return nil
}

func readSkillDescriptor(path string) (plugin.SkillDescriptor, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return plugin.SkillDescriptor{}, err
	}
	name, description := parseSkillFrontMatter(string(raw))
	return plugin.SkillDescriptor{Name: name, Description: description, Paths: []string{path}}, nil
}

func parseSkillFrontMatter(text string) (string, string) {
	text = strings.TrimLeft(text, "\ufeff\r\n\t ")
	if !strings.HasPrefix(text, "---") {
		return "", ""
	}
	lines := strings.Split(text, "\n")
	name := ""
	description := ""
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	return name, description
}

func SkillRoots(homeDir string, workspaceDir string, extra []string) []string {
	var roots []string
	appendRoot := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		if strings.HasPrefix(root, "~/") && strings.TrimSpace(homeDir) != "" {
			root = filepath.Join(strings.TrimSpace(homeDir), strings.TrimPrefix(root, "~/"))
		}
		roots = append(roots, root)
	}
	if homeDir != "" {
		appendRoot(filepath.Join(homeDir, ".caelis", "skills", ".system"))
		appendRoot(filepath.Join(homeDir, ".agents", "skills"))
		appendRoot(filepath.Join(homeDir, ".caelis", "skills"))
	}
	if workspaceDir != "" {
		appendRoot(filepath.Join(workspaceDir, "skills"))
		appendRoot(filepath.Join(workspaceDir, ".agents", "skills"))
	}
	for _, root := range extra {
		appendRoot(root)
	}
	return dedupePaths(roots)
}

func dedupePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	seen := map[string]struct{}{}
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		key := filepath.Clean(path)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func sortCatalog(catalog *Catalog) {
	sort.Slice(catalog.Plugins, func(i, j int) bool {
		if catalog.Plugins[i].Manifest.ID == catalog.Plugins[j].Manifest.ID {
			return catalog.Plugins[i].Path < catalog.Plugins[j].Path
		}
		return catalog.Plugins[i].Manifest.ID < catalog.Plugins[j].Manifest.ID
	})
	sortAliases(catalog.ModelProviders)
	sortAliases(catalog.Stores)
	sortAliases(catalog.Sandboxes)
	sortAliases(catalog.Tools)
	sort.Slice(catalog.Prompts, func(i, j int) bool {
		if catalog.Prompts[i].Priority == catalog.Prompts[j].Priority {
			return catalog.Prompts[i].ID < catalog.Prompts[j].ID
		}
		return catalog.Prompts[i].Priority < catalog.Prompts[j].Priority
	})
	sort.Slice(catalog.Skills, func(i, j int) bool {
		if catalog.Skills[i].Name == catalog.Skills[j].Name {
			return strings.Join(catalog.Skills[i].Paths, "\x00") < strings.Join(catalog.Skills[j].Paths, "\x00")
		}
		return catalog.Skills[i].Name < catalog.Skills[j].Name
	})
	sort.Slice(catalog.ACPAgents, func(i, j int) bool {
		if catalog.ACPAgents[i].Name == catalog.ACPAgents[j].Name {
			return catalog.ACPAgents[i].Command < catalog.ACPAgents[j].Command
		}
		return catalog.ACPAgents[i].Name < catalog.ACPAgents[j].Name
	})
	sort.Slice(catalog.RendererHints, func(i, j int) bool {
		if catalog.RendererHints[i].EventType == catalog.RendererHints[j].EventType {
			return catalog.RendererHints[i].ToolName < catalog.RendererHints[j].ToolName
		}
		return catalog.RendererHints[i].EventType < catalog.RendererHints[j].EventType
	})
	sort.SliceStable(catalog.Diagnostics, func(i, j int) bool {
		left := diagnosticSortKey(catalog.Diagnostics[i])
		right := diagnosticSortKey(catalog.Diagnostics[j])
		return left < right
	})
}

func addDiagnostic(catalog *Catalog, diagnostic Diagnostic) {
	if catalog == nil {
		return
	}
	diagnostic = normalizeDiagnostic(diagnostic)
	if diagnostic.Severity == "" && diagnostic.Kind == "" && diagnostic.ID == "" && diagnostic.Path == "" && diagnostic.Message == "" {
		return
	}
	catalog.Diagnostics = append(catalog.Diagnostics, diagnostic)
}

func normalizeDiagnostic(in Diagnostic) Diagnostic {
	out := in
	out.Severity = normalizeDiagnosticSeverity(out.Severity)
	out.Kind = strings.ToLower(strings.TrimSpace(out.Kind))
	out.ID = strings.TrimSpace(out.ID)
	out.Path = strings.TrimSpace(out.Path)
	out.Message = strings.TrimSpace(out.Message)
	out.Meta = cloneStringMap(out.Meta)
	return out
}

func normalizeDiagnosticSeverity(severity string) string {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "", DiagnosticInfo:
		return DiagnosticInfo
	case DiagnosticWarning, "warn":
		return DiagnosticWarning
	case DiagnosticError:
		return DiagnosticError
	default:
		return strings.ToLower(strings.TrimSpace(severity))
	}
}

func diagnosticSortKey(in Diagnostic) string {
	in = normalizeDiagnostic(in)
	return in.Kind + "\x00" + in.Path + "\x00" + in.ID + "\x00" + in.Message
}

func firstPath(paths []string) string {
	for _, path := range paths {
		if path = strings.TrimSpace(path); path != "" {
			return path
		}
	}
	return ""
}

func cleanOptionalPath(path string, homeDir string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	return cleanPath(path, homeDir)
}

func cleanPath(path string, homeDir string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := homePath(homeDir)
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func homePath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = home
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func readOptionalText(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func resolveRelativePaths(baseDir string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		out = append(out, filepath.Clean(path))
	}
	return out
}

func pluginAliases(pluginID string, kind string, aliases []plugin.FactoryAlias) ([]plugin.FactoryAlias, error) {
	if len(aliases) == 0 {
		return nil, nil
	}
	out := make([]plugin.FactoryAlias, 0, len(aliases))
	for i, item := range aliases {
		next := item
		next.Name = firstNonEmpty(next.Name, fmt.Sprintf("%s.%s.%d", pluginID, strings.ReplaceAll(kind, " ", "_"), i+1))
		next.Uses = strings.TrimSpace(next.Uses)
		if next.Uses == "" {
			return nil, fmt.Errorf("app/resources: %s alias %q in plugin %q missing uses", kind, next.Name, pluginID)
		}
		next.Description = strings.TrimSpace(next.Description)
		next.Meta = maps.Clone(item.Meta)
		out = append(out, next)
	}
	return out, nil
}

func pluginACPAgents(baseDir string, pluginID string, in []plugin.ACPAgentDescriptor) ([]plugin.ACPAgentDescriptor, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make([]plugin.ACPAgentDescriptor, 0, len(in))
	for i, item := range in {
		next := item
		next.Name = firstNonEmpty(next.Name, fmt.Sprintf("%s.agent.%d", pluginID, i+1))
		next.Command = strings.TrimSpace(next.Command)
		if next.Command == "" {
			return nil, fmt.Errorf("app/resources: acp agent %q in plugin %q missing command", next.Name, pluginID)
		}
		if strings.ContainsAny(next.Command, `/\`) && !filepath.IsAbs(next.Command) {
			next.Command = filepath.Clean(filepath.Join(baseDir, next.Command))
		}
		next.WorkDir = strings.TrimSpace(next.WorkDir)
		if next.WorkDir == "" {
			next.WorkDir = baseDir
		} else if !filepath.IsAbs(next.WorkDir) {
			next.WorkDir = filepath.Clean(filepath.Join(baseDir, next.WorkDir))
		}
		next.Args = slices.Clone(item.Args)
		next.Env = maps.Clone(item.Env)
		next.Roles = slices.Clone(item.Roles)
		out = append(out, next)
	}
	return out, nil
}

func sortAliases(aliases []plugin.FactoryAlias) {
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].Name == aliases[j].Name {
			return aliases[i].Uses < aliases[j].Uses
		}
		return aliases[i].Name < aliases[j].Name
	})
}

func cloneAliases(in []plugin.FactoryAlias) []plugin.FactoryAlias {
	if len(in) == 0 {
		return nil
	}
	out := make([]plugin.FactoryAlias, 0, len(in))
	for _, item := range in {
		item.Name = strings.TrimSpace(item.Name)
		item.Uses = strings.TrimSpace(item.Uses)
		item.Description = strings.TrimSpace(item.Description)
		item.Meta = maps.Clone(item.Meta)
		out = append(out, item)
	}
	return out
}

func cloneManifest(in plugin.Manifest) plugin.Manifest {
	out := in
	out.ID = strings.TrimSpace(out.ID)
	out.Name = strings.TrimSpace(out.Name)
	out.Version = strings.TrimSpace(out.Version)
	out.Description = strings.TrimSpace(out.Description)
	out.Capabilities = slices.Clone(in.Capabilities)
	out.Meta = maps.Clone(in.Meta)
	return out
}

func cloneRendererHints(in []plugin.RendererHint) []plugin.RendererHint {
	if len(in) == 0 {
		return nil
	}
	out := make([]plugin.RendererHint, 0, len(in))
	for _, item := range in {
		item.Meta = maps.Clone(item.Meta)
		out = append(out, item)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	return maps.Clone(in)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
