package commands

import (
	"runtime"
	"strings"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
)

// CommandSpec describes one slash command exposed by the TUI command registry.
type CommandSpec struct {
	Name             string
	Usage            string
	Description      string
	Details          []string
	Hidden           bool
	LocalDuringACP   bool
	Platforms        []string
	ArgCandidates    []control.SlashArgCandidate
	DynamicCompleter bool
}

var defaultACPCommandNames = []string{"status", "compact", "review"}

// DefaultSpecs returns the canonical TUI slash command specs in display order.
// Use DefaultSharedSpecs for commands that are safe for shared prompt routers,
// and DefaultACPSpecs for commands exposed through ACP clients.
func DefaultSpecs() []CommandSpec {
	return DefaultSpecsForPlatform(runtime.GOOS)
}

func DefaultSpecsForPlatform(goos string) []CommandSpec {
	specs := defaultSpecs()
	out := specs[:0]
	for _, spec := range specs {
		if commandSpecSupportsPlatform(spec, goos) {
			out = append(out, spec)
		}
	}
	return out
}

// DefaultSharedSpecs returns slash commands whose behavior is surface-neutral.
// Wizard/modal style commands remain TUI-only and must not be exposed by ACP.
func DefaultSharedSpecs() []CommandSpec {
	return DefaultSharedSpecsForPlatform(runtime.GOOS)
}

func DefaultSharedSpecsForPlatform(goos string) []CommandSpec {
	specs := defaultSharedSpecs()
	out := specs[:0]
	for _, spec := range specs {
		if commandSpecSupportsPlatform(spec, goos) {
			out = append(out, spec)
		}
	}
	return out
}

// DefaultTUISpecs returns commands that require TUI-owned interaction or app
// lifecycle behavior.
func DefaultTUISpecs() []CommandSpec {
	return DefaultTUISpecsForPlatform(runtime.GOOS)
}

func DefaultTUISpecsForPlatform(goos string) []CommandSpec {
	specs := defaultTUISpecs()
	out := specs[:0]
	for _, spec := range specs {
		if commandSpecSupportsPlatform(spec, goos) {
			out = append(out, spec)
		}
	}
	return out
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
	if spec, ok := byName["agent"]; ok {
		spec.Details = []string{"actions: list, add <builtin>, install <adapter>, use <agent|local>, remove <agent>"}
		spec.ArgCandidates = agentTUIRootCandidates()
		byName["agent"] = spec
	}
	order := []string{"help", "agent", "subagent", "review", "connect", "plugin", "model", "status", "doctor", "new", "resume", "compact", "exit", "quit"}
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
		{Name: "help", Usage: "/help", Description: "Show commands and shortcuts", LocalDuringACP: true},
		{Name: "agent", Usage: "/agent <action>", Description: "Manage ACP agents and controller switching", LocalDuringACP: true, Details: []string{"actions: list, add <builtin>, use <agent|local>, remove <agent>"}, ArgCandidates: agentSharedRootCandidates(), DynamicCompleter: true},
		{Name: "subagent", Usage: "/subagent <action>", Description: "Manage subagents and runtime bindings", LocalDuringACP: true, Details: []string{"actions: list, bind <id> default|model|acp ..."}, ArgCandidates: subagentRootCandidates(), DynamicCompleter: true},
		{Name: "review", Usage: "/review [instructions]", Description: "Review current workspace changes with the built-in reviewer", LocalDuringACP: true},
		{Name: "model", Usage: "/model <action>", Description: "Switch or delete a configured model alias", LocalDuringACP: true, Details: []string{"actions: use <alias>, del <alias>"}, ArgCandidates: modelRootCandidates(), DynamicCompleter: true},
		{Name: "status", Usage: "/status", Description: "Show current provider, model, session, sandbox, and store info", LocalDuringACP: true},
		{Name: "doctor", Usage: "/doctor", Description: "Diagnose and repair Windows sandbox readiness", LocalDuringACP: true, Platforms: []string{"windows"}},
		{Name: "new", Usage: "/new", Description: "Start a fresh session"},
		{Name: "resume", Usage: "/resume [session-id]", Description: "List recent sessions or resume one by id", LocalDuringACP: true, DynamicCompleter: true},
		{Name: "compact", Usage: "/compact", Description: "Compact the current session transcript"},
	}
	return specs
}

func defaultTUISpecs() []CommandSpec {
	specs := []CommandSpec{
		{Name: "connect", Usage: "/connect", Description: "Open the guided model/provider setup wizard", DynamicCompleter: true},
		{Name: "plugin", Usage: "/plugin <action>", Description: "Manage Caelis plugins", LocalDuringACP: true, Details: []string{"actions: install <plugin@marketplace|path>, marketplace add|list|update|rm, manage, rm <id>"}, ArgCandidates: pluginRootCandidates(), DynamicCompleter: true},
		{Name: "exit", Usage: "/exit", Description: "Exit the TUI", LocalDuringACP: true},
		{Name: "quit", Usage: "/quit", Description: "Exit the TUI", LocalDuringACP: true},
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

// IsLocalDuringACP reports whether the TUI should dispatch this command locally
// while a remote ACP controller is active.
func IsLocalDuringACP(name string) bool {
	return IsLocalDuringACPForPlatform(name, runtime.GOOS)
}

func IsLocalDuringACPForPlatform(name string, goos string) bool {
	spec, ok := LookupForPlatform(name, goos)
	return ok && spec.LocalDuringACP
}

// HelpText renders help text from the canonical specs. Unknown command names
// are retained so dynamic ACP child commands can appear in the same list.
func HelpText(names []string) string {
	return control.FormatCommandHelp(HelpSnapshot(names))
}

// HelpSnapshot returns the current slash command catalog as domain data. It
// intentionally does not describe columns, grouping, or visual layout.
func HelpSnapshot(names []string) control.CommandHelpSnapshot {
	if len(names) == 0 {
		names = DefaultNames()
	}
	out := control.CommandHelpSnapshot{Items: make([]control.CommandHelpItem, 0, len(names))}
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
			out.Items = append(out.Items, control.CommandHelpItem{
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
		out.Items = append(out.Items, control.CommandHelpItem{
			Name:           spec.Name,
			Usage:          usage,
			Description:    strings.TrimSpace(spec.Description),
			Details:        cleanHelpDetails(spec.Details),
			Known:          true,
			LocalDuringACP: spec.LocalDuringACP,
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
func RootArgCandidates(command string) []control.SlashArgCandidate {
	return RootArgCandidatesForPlatform(command, runtime.GOOS)
}

func RootArgCandidatesForPlatform(command string, goos string) []control.SlashArgCandidate {
	spec, ok := LookupForPlatform(command, goos)
	if !ok || len(spec.ArgCandidates) == 0 {
		return nil
	}
	out := make([]control.SlashArgCandidate, len(spec.ArgCandidates))
	copy(out, spec.ArgCandidates)
	return out
}

func agentSharedRootCandidates() []control.SlashArgCandidate {
	return []control.SlashArgCandidate{
		{Value: "use", Display: "use", Detail: "Switch the main controller"},
		{Value: "add", Display: "add", Detail: "Register a built-in ACP agent"},
		{Value: "list", Display: "list", Detail: "List registered ACP agents"},
		{Value: "remove", Display: "remove", Detail: "Unregister an ACP agent"},
	}
}

func agentTUIRootCandidates() []control.SlashArgCandidate {
	return []control.SlashArgCandidate{
		{Value: "use", Display: "use", Detail: "Switch the main controller"},
		{Value: "add", Display: "add", Detail: "Register a built-in ACP agent"},
		{Value: "install", Display: "install", Detail: "Install and register an external ACP adapter"},
		{Value: "list", Display: "list", Detail: "List registered ACP agents"},
		{Value: "remove", Display: "remove", Detail: "Unregister an ACP agent"},
	}
}

func subagentRootCandidates() []control.SlashArgCandidate {
	return []control.SlashArgCandidate{
		{Value: "list", Display: "list", Detail: "List subagents and bindings"},
		{Value: "bind", Display: "bind", Detail: "Bind subagents to the session model, a model alias, or an ACP agent"},
	}
}

func modelRootCandidates() []control.SlashArgCandidate {
	return []control.SlashArgCandidate{
		{Value: "use", Display: "use", Detail: "Switch current model alias"},
		{Value: "del", Display: "del", Detail: "Delete stored model alias"},
	}
}

func pluginRootCandidates() []control.SlashArgCandidate {
	return []control.SlashArgCandidate{
		{Value: "install", Display: "install", Detail: "Install a Claude-compatible or native Caelis plugin"},
		{Value: "marketplace", Display: "marketplace", Detail: "Manage plugin marketplaces"},
		{Value: "manage", Display: "manage", Detail: "List, enable, or disable installed plugins"},
		{Value: "rm", Display: "rm", Detail: "Remove a plugin by ID"},
	}
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/")))
}
