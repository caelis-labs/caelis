// Package identity owns built-in tool names and their reusable semantic traits.
package identity

import "strings"

const (
	Read       = "Read"
	Write      = "Write"
	Patch      = "Patch"
	List       = "List"
	Glob       = "Glob"
	Grep       = "Grep"
	RunCommand = "RunCommand"
	Task       = "Task"
	Plan       = "Plan"
	Skill      = "Skill"
	WebSearch  = "WebSearch"
	WebFetch   = "WebFetch"
	Spawn      = "Spawn"
	ToolSearch = "ToolSearch"
)

// Kind is the provider-neutral behavior category of a built-in tool.
type Kind string

const (
	KindOther   Kind = "other"
	KindRead    Kind = "read"
	KindEdit    Kind = "edit"
	KindSearch  Kind = "search"
	KindExecute Kind = "execute"
)

// TitleStyle identifies the shared argument summarizer for a built-in tool.
type TitleStyle string

const (
	TitleNone          TitleStyle = ""
	TitlePath          TitleStyle = "path"
	TitleSkill         TitleStyle = "skill"
	TitleQuery         TitleStyle = "query"
	TitleURL           TitleStyle = "url"
	TitleCommandAction TitleStyle = "command_action"
	TitleSpawn         TitleStyle = "spawn"
)

// ResultStyle identifies the shared result renderer for a built-in tool.
type ResultStyle string

const (
	ResultGeneric   ResultStyle = ""
	ResultRead      ResultStyle = "read"
	ResultList      ResultStyle = "list"
	ResultGlob      ResultStyle = "glob"
	ResultSearch    ResultStyle = "search"
	ResultWebSearch ResultStyle = "web_search"
	ResultWebFetch  ResultStyle = "web_fetch"
	ResultMutation  ResultStyle = "mutation"
	ResultCommand   ResultStyle = "command"
	ResultSpawn     ResultStyle = "spawn"
	ResultTask      ResultStyle = "task"
)

// Info describes one built-in identity. HistoricalOnly identities are valid
// for replay and display but must never be registered for model execution.
type Info struct {
	Name            string
	Kind            Kind
	ExplorationVerb string
	TitleStyle      TitleStyle
	ResultStyle     ResultStyle
	TerminalKnown   bool
	TerminalPanel   bool
	HistoricalOnly  bool
}

type entry struct {
	Info
	aliases []string
}

var entries = []entry{
	{Info: Info{Name: Read, Kind: KindRead, ExplorationVerb: "Read", TitleStyle: TitlePath, ResultStyle: ResultRead}, aliases: []string{"read"}},
	{Info: Info{Name: Write, Kind: KindEdit, TitleStyle: TitlePath, ResultStyle: ResultMutation}, aliases: []string{"write"}},
	{Info: Info{Name: Patch, Kind: KindEdit, TitleStyle: TitlePath, ResultStyle: ResultMutation}, aliases: []string{"patch"}},
	{Info: Info{Name: List, Kind: KindSearch, ExplorationVerb: "List", TitleStyle: TitlePath, ResultStyle: ResultList, HistoricalOnly: true}, aliases: []string{"list"}},
	{Info: Info{Name: Glob, Kind: KindSearch, ExplorationVerb: "Glob", TitleStyle: TitlePath, ResultStyle: ResultGlob}, aliases: []string{"glob"}},
	{Info: Info{Name: Grep, Kind: KindSearch, ExplorationVerb: "Search", TitleStyle: TitlePath, ResultStyle: ResultSearch}, aliases: []string{"grep", "search"}},
	{Info: Info{Name: RunCommand, Kind: KindExecute, TitleStyle: TitleCommandAction, ResultStyle: ResultCommand, TerminalKnown: true, TerminalPanel: true}, aliases: []string{"runcommand", "run_command"}},
	{Info: Info{Name: Task, Kind: KindExecute, TitleStyle: TitleCommandAction, ResultStyle: ResultTask, TerminalKnown: true}, aliases: []string{"task"}},
	{Info: Info{Name: Plan, Kind: KindOther}, aliases: []string{"plan"}},
	{Info: Info{Name: Skill, Kind: KindRead, ExplorationVerb: "Skill", TitleStyle: TitleSkill}, aliases: []string{"skill"}},
	{Info: Info{Name: WebSearch, Kind: KindSearch, ExplorationVerb: "Search", TitleStyle: TitleQuery, ResultStyle: ResultWebSearch}, aliases: []string{"websearch", "web_search"}},
	{Info: Info{Name: WebFetch, Kind: KindSearch, ExplorationVerb: "Fetch", TitleStyle: TitleURL, ResultStyle: ResultWebFetch}, aliases: []string{"webfetch", "web_fetch"}},
	{Info: Info{Name: Spawn, Kind: KindExecute, TitleStyle: TitleSpawn, ResultStyle: ResultSpawn, TerminalKnown: true, TerminalPanel: true}, aliases: []string{"spawn"}},
	{Info: Info{Name: ToolSearch, Kind: KindOther}, aliases: []string{"toolsearch", "tool_search"}},
}

var byAlias = buildAliasIndex(entries)

func buildAliasIndex(source []entry) map[string]Info {
	out := make(map[string]Info, len(source)*2)
	for _, item := range source {
		out[normalize(item.Name)] = item.Info
		for _, alias := range item.aliases {
			out[normalize(alias)] = item.Info
		}
	}
	return out
}

// Lookup resolves canonical and historical names, including display-only
// identities such as the removed List tool.
func Lookup(name string) (Info, bool) {
	info, ok := byAlias[normalize(name)]
	return info, ok
}

// LookupExecutable resolves only identities that may be configured for model
// execution. Historical display identities are intentionally excluded.
func LookupExecutable(name string) (Info, bool) {
	info, ok := Lookup(name)
	return info, ok && !info.HistoricalOnly
}

// Resolve returns the canonical identity for a built-in or historical name.
func Resolve(name string) (string, bool) {
	info, ok := Lookup(name)
	return info.Name, ok
}

// ResolveExecutable returns the canonical identity only for executable tools.
func ResolveExecutable(name string) (string, bool) {
	info, ok := LookupExecutable(name)
	return info.Name, ok
}

// CanonicalOrSelf returns a known canonical identity or the trimmed external
// name unchanged.
func CanonicalOrSelf(name string) string {
	if info, ok := Lookup(name); ok {
		return info.Name
	}
	return strings.TrimSpace(name)
}

// ExecutableOrSelf canonicalizes executable built-ins without claiming
// historical display-only or external names.
func ExecutableOrSelf(name string) string {
	if info, ok := LookupExecutable(name); ok {
		return info.Name
	}
	return strings.TrimSpace(name)
}

func normalize(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
