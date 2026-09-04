package controlprompt

import (
	"runtime"
	"strings"
)

// CommandSpec describes one slash command in the shared Control prompt catalog.
type CommandSpec struct {
	Name             string
	Usage            string
	Description      string
	Details          []string
	Hidden           bool
	Platforms        []string
	ArgCandidates    []SlashArgCandidate
	DynamicCompleter bool
}

var defaultACPCommandNames = []string{"status", "breeze", "orbit", "zenith", "compact", "review"}

// DefaultSpecs returns the canonical TUI slash command specs in display order.
// Use DefaultSharedSpecs for commands that are safe for shared prompt routers,
// and DefaultACPSpecs for commands exposed through ACP clients.
func DefaultSpecs() []CommandSpec {
	return DefaultSpecsForPlatform(runtime.GOOS)
}

func DefaultSpecsForPlatform(goos string) []CommandSpec {
	return filterSpecsForPlatform(defaultSpecs(), goos)
}

// DefaultSharedSpecs returns slash commands whose behavior is surface-neutral.
// Wizard/modal style commands remain TUI-only and must not be exposed by ACP.
func DefaultSharedSpecs() []CommandSpec {
	return DefaultSharedSpecsForPlatform(runtime.GOOS)
}

func DefaultSharedSpecsForPlatform(goos string) []CommandSpec {
	return filterSpecsForPlatform(defaultSharedSpecs(), goos)
}

// DefaultTUISpecs returns commands that require TUI-owned interaction or app
// lifecycle behavior.
func DefaultTUISpecs() []CommandSpec {
	return DefaultTUISpecsForPlatform(runtime.GOOS)
}

func DefaultTUISpecsForPlatform(goos string) []CommandSpec {
	return filterSpecsForPlatform(defaultTUISpecs(), goos)
}

// DefaultACPSpecs returns the narrow slash command set exposed through ACP
// clients. Session lifecycle and configuration flows should use ACP APIs or
// client UI instead of slash commands.
func DefaultACPSpecs() []CommandSpec {
	return DefaultACPSpecsForPlatform(runtime.GOOS)
}

func DefaultACPSpecsForPlatform(goos string) []CommandSpec {
	byName := map[string]CommandSpec{}
	for _, spec := range DefaultSharedSpecsForPlatform(goos) {
		byName[spec.Name] = spec
	}
	out := make([]CommandSpec, 0, len(defaultACPCommandNames))
	for _, name := range defaultACPCommandNames {
		if spec, ok := byName[name]; ok {
			out = append(out, spec)
		}
	}
	return out
}

func defaultSpecs() []CommandSpec {
	byName := map[string]CommandSpec{}
	for _, spec := range append(defaultSharedSpecs(), defaultTUISpecs()...) {
		byName[spec.Name] = spec
	}
	order := []string{"help", "review", "breeze", "orbit", "zenith", "connect", "disconnect", "subagent", "plugin", "model", "status", "doctor", "new", "resume", "compact", "exit", "quit"}
	specs := make([]CommandSpec, 0, len(order))
	for _, name := range order {
		if spec, ok := byName[name]; ok {
			specs = append(specs, spec)
		}
	}
	return specs
}

func defaultSharedSpecs() []CommandSpec {
	specs := []CommandSpec{
		{Name: "help", Usage: "/help", Description: "Show commands and shortcuts"},
		{Name: "review", Usage: "/review [instructions]", Description: "Review current workspace changes with the system-managed Reviewer"},
		{Name: "breeze", Usage: "/breeze <prompt>", Description: "Run the bound Breeze profile"},
		{Name: "orbit", Usage: "/orbit <prompt>", Description: "Run the bound Orbit profile"},
		{Name: "zenith", Usage: "/zenith <prompt>", Description: "Run the bound Zenith profile"},
		{Name: "model", Usage: "/model <model> <effort> [fast]", Description: "Choose the model, effort, and optional GPT fast mode for the current or next session", DynamicCompleter: true},
		{Name: "status", Usage: "/status", Description: "Show current provider, model, session, sandbox, and store info"},
		{Name: "doctor", Usage: "/doctor", Description: "Diagnose and repair Windows sandbox readiness", Platforms: []string{"windows"}},
		{Name: "new", Usage: "/new", Description: "Start a fresh session"},
		{Name: "resume", Usage: "/resume [session-id]", Description: "List recent sessions or resume one by id", DynamicCompleter: true},
		{Name: "compact", Usage: "/compact", Description: "Compact the current session transcript"},
	}
	return specs
}

func defaultTUISpecs() []CommandSpec {
	specs := []CommandSpec{
		{Name: "connect", Usage: "/connect", Description: "Connect a model provider or local ACP Agent", DynamicCompleter: true},
		{Name: "disconnect", Usage: "/disconnect", Description: "Disconnect provider models or local ACP Agents", DynamicCompleter: true},
		{Name: "subagent", Usage: "/subagent <action>", Description: "List or bind delegation profiles and system Agents", DynamicCompleter: true, Details: []string{"actions: list; bind <breeze|orbit|zenith> <self|agent> [effort]; bind <guardian|reviewer> <default|model-agent> [effort]"}},
		{Name: "plugin", Usage: "/plugin <action>", Description: "Manage Caelis plugins", Details: []string{"actions: install <plugin@marketplace|path>, marketplace add|list|update|rm, manage, rm <id>"}, ArgCandidates: pluginRootCandidates(), DynamicCompleter: true},
		{Name: "exit", Usage: "/exit", Description: "Exit the TUI"},
		{Name: "quit", Usage: "/quit", Description: "Exit the TUI"},
	}
	return specs
}

func commandSpecSupportsPlatform(spec CommandSpec, goos string) bool {
	if len(spec.Platforms) == 0 {
		return true
	}
	goos = strings.ToLower(strings.TrimSpace(goos))
	for _, platform := range spec.Platforms {
		if strings.EqualFold(strings.TrimSpace(platform), goos) {
			return true
		}
	}
	return false
}

func filterSpecsForPlatform(specs []CommandSpec, goos string) []CommandSpec {
	out := specs[:0]
	for _, spec := range specs {
		if commandSpecSupportsPlatform(spec, goos) {
			out = append(out, spec)
		}
	}
	return out
}

// DefaultNames returns visible command names in canonical display order.
func DefaultNames() []string {
	return DefaultNamesForPlatform(runtime.GOOS)
}

func DefaultNamesForPlatform(goos string) []string {
	specs := DefaultSpecsForPlatform(goos)
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Hidden {
			continue
		}
		out = append(out, spec.Name)
	}
	return out
}

func DefaultSharedNames() []string {
	return DefaultSharedNamesForPlatform(runtime.GOOS)
}

func DefaultSharedNamesForPlatform(goos string) []string {
	specs := DefaultSharedSpecsForPlatform(goos)
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Hidden {
			continue
		}
		out = append(out, spec.Name)
	}
	return out
}

// WithoutNames removes named slash commands while preserving the caller's
// display order. Names may be supplied with or without a leading slash.
func WithoutNames(names []string, excluded ...string) []string {
	blocked := make(map[string]struct{}, len(excluded))
	for _, name := range excluded {
		name = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/")))
		if name != "" {
			blocked[name] = struct{}{}
		}
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/")))
		if _, remove := blocked[key]; remove {
			continue
		}
		out = append(out, name)
	}
	return out
}

func DefaultACPNames() []string {
	return DefaultACPNamesForPlatform(runtime.GOOS)
}

func DefaultACPNamesForPlatform(goos string) []string {
	specs := DefaultACPSpecsForPlatform(goos)
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Hidden {
			continue
		}
		out = append(out, spec.Name)
	}
	return out
}

// Lookup returns a core command spec by name.
func Lookup(name string) (CommandSpec, bool) {
	return LookupForPlatform(name, runtime.GOOS)
}

func LookupForPlatform(name string, goos string) (CommandSpec, bool) {
	name = normalizeName(name)
	for _, spec := range DefaultSpecsForPlatform(goos) {
		if spec.Name == name {
			return spec, true
		}
	}
	return CommandSpec{}, false
}

func LookupShared(name string) (CommandSpec, bool) {
	return LookupSharedForPlatform(name, runtime.GOOS)
}

func LookupSharedForPlatform(name string, goos string) (CommandSpec, bool) {
	name = normalizeName(name)
	for _, spec := range DefaultSharedSpecsForPlatform(goos) {
		if spec.Name == name {
			return spec, true
		}
	}
	return CommandSpec{}, false
}

func LookupACP(name string) (CommandSpec, bool) {
	return LookupACPForPlatform(name, runtime.GOOS)
}

func LookupACPForPlatform(name string, goos string) (CommandSpec, bool) {
	name = normalizeName(name)
	for _, spec := range DefaultACPSpecsForPlatform(goos) {
		if spec.Name == name {
			return spec, true
		}
	}
	return CommandSpec{}, false
}

// IsKnown reports whether a core command exists.
func IsKnown(name string) bool {
	return IsKnownForPlatform(name, runtime.GOOS)
}

func IsKnownForPlatform(name string, goos string) bool {
	_, ok := LookupForPlatform(name, goos)
	return ok
}

func IsSharedKnown(name string) bool {
	return IsSharedKnownForPlatform(name, runtime.GOOS)
}

func IsSharedKnownForPlatform(name string, goos string) bool {
	_, ok := LookupSharedForPlatform(name, goos)
	return ok
}

func IsACPKnown(name string) bool {
	return IsACPKnownForPlatform(name, runtime.GOOS)
}

func IsACPKnownForPlatform(name string, goos string) bool {
	_, ok := LookupACPForPlatform(name, goos)
	return ok
}

// HelpSnapshot returns the current slash command catalog as domain data. It
// intentionally does not describe columns, grouping, or visual layout.
func HelpSnapshot(names []string) CommandHelpSnapshot {
	if len(names) == 0 {
		names = DefaultNames()
	}
	out := CommandHelpSnapshot{Items: make([]CommandHelpItem, 0, len(names))}
	seen := map[string]struct{}{}
	for _, command := range names {
		name := normalizeName(command)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		spec, known := Lookup(name)
		if !known {
			out.Items = append(out.Items, CommandHelpItem{
				Name:        name,
				Usage:       "/" + name + " <prompt>",
				Description: "Send a prompt to the registered ACP agent",
				Dynamic:     true,
				Known:       false,
			})
			continue
		}
		usage := strings.TrimSpace(spec.Usage)
		if usage == "" {
			usage = "/" + spec.Name
		}
		out.Items = append(out.Items, CommandHelpItem{
			Name:        spec.Name,
			Usage:       usage,
			Description: strings.TrimSpace(spec.Description),
			Details:     cleanHelpDetails(spec.Details),
			Known:       true,
		})
	}
	return out
}

func cleanHelpDetails(details []string) []string {
	if len(details) == 0 {
		return nil
	}
	out := make([]string, 0, len(details))
	for _, detail := range details {
		if detail = strings.TrimSpace(detail); detail != "" {
			out = append(out, detail)
		}
	}
	return out
}

// RootArgCandidates returns static first-level argument candidates for command.
// Dynamic completions such as model aliases, agent catalogs, and connect wizard
// values remain owned by the driver.
func RootArgCandidates(command string) []SlashArgCandidate {
	return RootArgCandidatesForPlatform(command, runtime.GOOS)
}

func RootArgCandidatesForPlatform(command string, goos string) []SlashArgCandidate {
	spec, ok := LookupForPlatform(command, goos)
	if !ok || len(spec.ArgCandidates) == 0 {
		return nil
	}
	out := make([]SlashArgCandidate, len(spec.ArgCandidates))
	copy(out, spec.ArgCandidates)
	return out
}

func pluginRootCandidates() []SlashArgCandidate {
	return []SlashArgCandidate{
		{Value: "install", Display: "install", Detail: "Install a Claude-compatible or native Caelis plugin"},
		{Value: "marketplace", Display: "marketplace", Detail: "Manage plugin marketplaces"},
		{Value: "manage", Display: "manage", Detail: "List, enable, or disable installed plugins"},
		{Value: "rm", Display: "rm", Detail: "Remove a plugin by ID"},
	}
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/")))
}
