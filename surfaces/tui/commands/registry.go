package commands

import (
	"strings"

	driver "github.com/OnslaughtSnail/caelis/surfaces/tui/driver"
)

// CommandSpec describes one slash command exposed by the TUI command registry.
type CommandSpec struct {
	Name             string
	Usage            string
	Description      string
	Hidden           bool
	LocalDuringACP   bool
	ArgCandidates    []driver.SlashArgCandidate
	DynamicCompleter bool
}

// DefaultSpecs returns the canonical core slash command specs in display order.
func DefaultSpecs() []CommandSpec {
	specs := []CommandSpec{
		{Name: "help", Usage: "/help", Description: "Show available slash commands", LocalDuringACP: true},
		{Name: "agent", Usage: "/agent list | /agent add <builtin> | /agent install <adapter> | /agent use <agent|local> | /agent remove <agent>", Description: "Manage registered ACP agents and main-controller switching", LocalDuringACP: true, ArgCandidates: agentRootCandidates(), DynamicCompleter: true},
		{Name: "connect", Usage: "/connect", Description: "Open the guided model/provider setup wizard", DynamicCompleter: true},
		{Name: "model", Usage: "/model use <alias> | /model del <alias>", Description: "Switch or delete a configured model alias", LocalDuringACP: true, ArgCandidates: modelRootCandidates(), DynamicCompleter: true},
		{Name: "approval", Usage: "/approval [auto-review|manual]", Description: "Inspect or change approval review mode", LocalDuringACP: true, ArgCandidates: approvalCandidates()},
		{Name: "status", Usage: "/status", Description: "Show current provider, model, session, sandbox, and store info", LocalDuringACP: true},
		{Name: "doctor", Usage: "/doctor", Description: "Diagnose provider, model, session store, and sandbox readiness", LocalDuringACP: true},
		{Name: "new", Usage: "/new", Description: "Start a fresh session"},
		{Name: "resume", Usage: "/resume [session-id]", Description: "List recent sessions or resume one by id", LocalDuringACP: true, DynamicCompleter: true},
		{Name: "compact", Usage: "/compact", Description: "Compact the current session transcript"},
		{Name: "exit", Usage: "/exit", Description: "Exit the TUI", LocalDuringACP: true},
		{Name: "quit", Usage: "/quit", Description: "Exit the TUI", LocalDuringACP: true},
	}
	return specs
}

// DefaultNames returns visible command names in canonical display order.
func DefaultNames() []string {
	specs := DefaultSpecs()
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
	name = normalizeName(name)
	for _, spec := range DefaultSpecs() {
		if spec.Name == name {
			return spec, true
		}
	}
	return CommandSpec{}, false
}

// IsKnown reports whether a core command exists.
func IsKnown(name string) bool {
	_, ok := Lookup(name)
	return ok
}

// IsLocalDuringACP reports whether the TUI should dispatch this command locally
// while a remote ACP controller is active.
func IsLocalDuringACP(name string) bool {
	spec, ok := Lookup(name)
	return ok && spec.LocalDuringACP
}

// HelpText renders help text from the canonical specs. Unknown command names
// are retained so dynamic ACP child commands can appear in the same list.
func HelpText(names []string) string {
	if len(names) == 0 {
		names = DefaultNames()
	}
	lines := []string{"available commands:"}
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
			lines = append(lines, "  /"+name)
			continue
		}
		usage := strings.TrimSpace(spec.Usage)
		description := strings.TrimSpace(spec.Description)
		switch {
		case usage == "":
			lines = append(lines, "  /"+spec.Name)
		case description == "":
			lines = append(lines, "  "+usage)
		default:
			lines = append(lines, "  "+usage+"  "+description)
		}
	}
	return strings.Join(lines, "\n")
}

// RootArgCandidates returns static first-level argument candidates for command.
// Dynamic completions such as model aliases, agent catalogs, and connect wizard
// values remain owned by the driver.
func RootArgCandidates(command string) []driver.SlashArgCandidate {
	spec, ok := Lookup(command)
	if !ok || len(spec.ArgCandidates) == 0 {
		return nil
	}
	out := make([]driver.SlashArgCandidate, len(spec.ArgCandidates))
	copy(out, spec.ArgCandidates)
	return out
}

func agentRootCandidates() []driver.SlashArgCandidate {
	return []driver.SlashArgCandidate{
		{Value: "use", Display: "use", Detail: "Switch the main controller"},
		{Value: "add", Display: "add", Detail: "Register a built-in ACP agent"},
		{Value: "install", Display: "install", Detail: "Install and register an external ACP adapter"},
		{Value: "list", Display: "list", Detail: "List registered ACP agents"},
		{Value: "remove", Display: "remove", Detail: "Unregister an ACP agent"},
	}
}

func modelRootCandidates() []driver.SlashArgCandidate {
	return []driver.SlashArgCandidate{
		{Value: "use", Display: "use", Detail: "Switch current model alias"},
		{Value: "del", Display: "del", Detail: "Delete stored model alias"},
	}
}

func approvalCandidates() []driver.SlashArgCandidate {
	return []driver.SlashArgCandidate{
		{Value: "auto-review", Display: "auto-review", Detail: "Use automatic AI approval review"},
		{Value: "manual", Display: "manual", Detail: "Prompt before sensitive requests"},
	}
}

func normalizeName(name string) string {
	return strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "/")))
}
