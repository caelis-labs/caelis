package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const defaultLimit = 8

type Tool struct {
	def     tool.Definition
	entries []entry
}

type entry struct {
	def        tool.Definition
	searchText string
}

type request struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// New returns a discovery tool for deferred MCP tools. A nil result means there
// are no deferred MCP tools to discover.
func New(tools []tool.Tool) tool.Tool {
	entries := buildEntries(tools)
	if len(entries) == 0 {
		return nil
	}
	return &Tool{
		def: tool.Definition{
			Name:        tool.ToolSearchToolName,
			Description: description(entries),
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"query"},
				"properties": map[string]any{
					"query": map[string]any{
						"type":        "string",
						"description": "Search query for deferred tools.",
						"minLength":   1,
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": fmt.Sprintf("Maximum number of tools to return. Defaults to %d.", defaultLimit),
						"minimum":     1,
					},
				},
			},
			Metadata: map[string]any{
				tool.MetadataToolKind: tool.MetadataToolKindToolSearch,
			},
		},
		entries: entries,
	}
}

func buildEntries(tools []tool.Tool) []entry {
	entries := make([]entry, 0, len(tools))
	for _, item := range tools {
		if item == nil {
			continue
		}
		def := item.Definition()
		if !tool.IsMCPDefinition(def) {
			continue
		}
		entries = append(entries, entry{
			def:        tool.CloneDefinition(def),
			searchText: searchText(def),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].def.Name < entries[j].def.Name
	})
	return entries
}

func description(entries []entry) string {
	sourceSet := map[string]bool{}
	for _, item := range entries {
		if source := sourceName(item.def); source != "" {
			sourceSet[source] = true
		}
	}
	sources := make([]string, 0, len(sourceSet))
	for source := range sourceSet {
		sources = append(sources, "- "+source)
	}
	sort.Strings(sources)
	sourceDescriptions := "None currently enabled."
	if len(sources) > 0 {
		sourceDescriptions = strings.Join(sources, "\n")
	}
	return fmt.Sprintf("# Tool discovery\n\nSearches over deferred MCP tool metadata and exposes matching tools for the next model call.\n\nYou have access to tools from the following sources:\n%s\nSome of the tools may not have been provided to you upfront, and you should use this tool (`%s`) to search for the required tools.", sourceDescriptions, tool.ToolSearchToolName)
}

func (t *Tool) Definition() tool.Definition {
	if t == nil {
		return tool.Definition{}
	}
	return tool.CloneDefinition(t.def)
}

func (t *Tool) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	if t == nil {
		return tool.Result{}, tool.NewError(tool.ErrorCodeNotFound, "tool_search is unavailable")
	}
	args, err := parseRequest(call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	matches := t.search(args.Query, args.Limit)
	definitions := make([]tool.Definition, 0, len(matches))
	for _, match := range matches {
		definitions = append(definitions, match.def)
	}
	raw, err := json.Marshal(tool.NewToolSearchResult(definitions))
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{
		ID:   call.ID,
		Name: call.Name,
		Content: []model.Part{
			model.NewJSONPart(raw),
		},
	}, nil
}

func parseRequest(raw json.RawMessage) (request, error) {
	var args request
	if len(raw) == 0 {
		return args, tool.NewError(tool.ErrorCodeInvalidInput, "tool_search query is required")
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return args, fmt.Errorf("tool_search: decode input: %w", err)
	}
	if err := tool.RejectUnknownArgs(values, "query", "limit"); err != nil {
		return args, err
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, fmt.Errorf("tool_search: decode input: %w", err)
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return args, tool.NewError(tool.ErrorCodeInvalidInput, "tool_search query is required")
	}
	if args.Limit <= 0 {
		args.Limit = defaultLimit
	}
	return args, nil
}

func (t *Tool) search(query string, limit int) []entry {
	terms := tokenize(query)
	if len(terms) == 0 || limit <= 0 {
		return nil
	}
	type scored struct {
		entry entry
		score int
	}
	scoredEntries := make([]scored, 0, len(t.entries))
	for _, item := range t.entries {
		score := scoreText(item.searchText, terms)
		if score <= 0 {
			continue
		}
		scoredEntries = append(scoredEntries, scored{entry: item, score: score})
	}
	sort.SliceStable(scoredEntries, func(i, j int) bool {
		if scoredEntries[i].score != scoredEntries[j].score {
			return scoredEntries[i].score > scoredEntries[j].score
		}
		return scoredEntries[i].entry.def.Name < scoredEntries[j].entry.def.Name
	})
	if len(scoredEntries) > limit {
		scoredEntries = scoredEntries[:limit]
	}
	out := make([]entry, 0, len(scoredEntries))
	for _, item := range scoredEntries {
		out = append(out, item.entry)
	}
	return out
}

func scoreText(text string, terms []string) int {
	text = strings.ToLower(text)
	tokens := map[string]int{}
	for _, token := range tokenize(text) {
		tokens[token]++
	}
	score := 0
	for _, term := range terms {
		if count := tokens[term]; count > 0 {
			score += 4 + count
			continue
		}
		if strings.Contains(text, term) {
			score++
		}
	}
	return score
}

func searchText(def tool.Definition) string {
	parts := []string{
		def.Name,
		strings.ReplaceAll(def.Name, "_", " "),
		def.Description,
		stringMetadata(def, tool.MetadataPluginID),
		stringMetadata(def, tool.MetadataMCPServer),
		stringMetadata(def, tool.MetadataMCPTool),
	}
	appendSchemaSearchText(def.InputSchema, &parts)
	return strings.Join(nonEmpty(parts), " ")
}

func appendSchemaSearchText(value any, parts *[]string) {
	mapped, ok := value.(map[string]any)
	if !ok {
		return
	}
	if description, _ := mapped["description"].(string); strings.TrimSpace(description) != "" {
		*parts = append(*parts, description)
	}
	properties, _ := mapped["properties"].(map[string]any)
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		*parts = append(*parts, name)
		appendSchemaSearchText(properties[name], parts)
	}
	if items, ok := mapped["items"]; ok {
		appendSchemaSearchText(items, parts)
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		variants, _ := mapped[key].([]any)
		for _, variant := range variants {
			appendSchemaSearchText(variant, parts)
		}
	}
}

func sourceName(def tool.Definition) string {
	pluginID := stringMetadata(def, tool.MetadataPluginID)
	server := stringMetadata(def, tool.MetadataMCPServer)
	switch {
	case pluginID != "" && server != "":
		return pluginID + "/" + server
	case pluginID != "":
		return pluginID
	default:
		return server
	}
}

func stringMetadata(def tool.Definition, key string) string {
	value, _ := def.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func tokenize(text string) []string {
	text = strings.ToLower(strings.TrimSpace(text))
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if field = strings.TrimSpace(field); field != "" {
			out = append(out, field)
		}
	}
	return out
}

func nonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

var _ tool.Tool = (*Tool)(nil)
