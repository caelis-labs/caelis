package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const (
	defaultLimit                  = 8
	maxLimit                      = 16
	maxQueryRunes                 = 256
	maxDescriptionSources         = 6
	maxToolSearchDescriptionRunes = 4096
	maxSourceMetadataRunes        = 256
)

type Tool struct {
	def     tool.Definition
	entries []entry
	source  tool.Source
	ranker  Ranker
}

type entry struct {
	def tool.Definition
}

type request struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

// New returns a discovery tool for deferred MCP tools. A nil result means there
// are no deferred MCP tools to discover.
func New(tools []tool.Tool) tool.Tool {
	return NewWithRanker(tools, nil)
}

// NewWithRanker supplies an explicit selector for a fixed catalog. Passing
// nil uses the restricted model selector, just like NewSource.
func NewWithRanker(tools []tool.Tool, ranker Ranker) tool.Tool {
	entries := buildEntries(tools)
	if len(entries) == 0 {
		return nil
	}
	t := newTool(entries)
	t.ranker = ranker
	return t
}

func newTool(entries []entry) *Tool {
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
						"maxLength":   maxQueryRunes,
					},
					"limit": map[string]any{
						"type":        "integer",
						"description": fmt.Sprintf("Maximum number of tools to return. Defaults to %d.", defaultLimit),
						"minimum":     1,
						"maximum":     maxLimit,
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

// NewSource discovers only ready MCP tools from an asynchronous source. It is
// present while the source is empty so later ready tools can be discovered.
// Without an explicit ranker, search uses a restricted model selector.
func NewSource(source tool.Source, rankers ...Ranker) tool.Tool {
	if source == nil {
		return nil
	}
	t := newTool(nil)
	t.source = source
	if len(rankers) > 0 {
		t.ranker = rankers[0]
	}
	return t
}

func (t *Tool) currentEntries() []entry {
	if t.source != nil {
		return buildEntries(t.source.Tools())
	}
	return t.entries
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
		entries = append(entries, entry{def: tool.CloneDefinition(def)})
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
		sources = append(sources, source)
	}
	sort.Strings(sources)
	omitted := 0
	if len(sources) > maxDescriptionSources {
		omitted = len(sources) - maxDescriptionSources
		sources = sources[:maxDescriptionSources]
	}
	for index := range sources {
		sources[index] = "- " + sources[index]
	}
	if omitted > 0 {
		sources = append(sources, fmt.Sprintf("- %d additional sources omitted", omitted))
	}
	sourceDescriptions := "None currently ready."
	if len(sources) > 0 {
		sourceDescriptions = strings.Join(sources, "\n")
	}
	return truncateRunes("Find deferred MCP tools by name or capability; matching tools become callable on the next model request. Sources:\n"+sourceDescriptions, maxToolSearchDescriptionRunes)
}

func (t *Tool) Definition() tool.Definition {
	if t == nil {
		return tool.Definition{}
	}
	def := tool.CloneDefinition(t.def)
	if t.source != nil {
		def.Description = description(t.currentEntries())
	}
	return def
}

func (t *Tool) Call(ctx context.Context, call tool.Call) (tool.Result, error) {
	if t == nil {
		return tool.Result{}, tool.NewError(tool.ErrorCodeNotFound, "ToolSearch is unavailable")
	}
	args, err := parseRequest(call.Input)
	if err != nil {
		return tool.Result{}, err
	}
	snapshot := Tool{entries: t.currentEntries(), source: t.source, ranker: t.ranker}
	matches, err := snapshot.rank(ctx, args.Query, args.Limit, SearchModel{
		Model: call.RuntimeModel, Reasoning: call.RuntimeReasoning, ServiceTier: call.RuntimeServiceTier,
	})
	if err != nil {
		return tool.Result{}, err
	}
	result := tool.ToolSearchResult{Tools: make([]tool.ToolSearchDiscoveredTool, 0, len(matches))}
	for _, match := range matches {
		result.Tools = append(result.Tools, tool.NewToolSearchDiscoveredTool(match.def))
	}
	result.Count = len(result.Tools)
	for tool.EstimateToolSearchResultPromptTokens(result) > tool.MaxToolSearchResultPromptTokens && len(result.Tools) > 0 {
		result.Tools = result.Tools[:len(result.Tools)-1]
		result.Count = len(result.Tools)
		result.Truncated = true
		result.OmittedCount++
	}
	raw, err := json.Marshal(result)
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

// readSchema validates that one explicit name still has the definition in the
// current scoped source captured for this search. A changed or removed schema
// cannot be substituted into an in-progress selector conversation.
func (t *Tool) checkSource(ctx context.Context) error {
	if checker, ok := t.source.(interface{ CheckSearchScope(context.Context) error }); ok {
		return checker.CheckSearchScope(ctx)
	}
	return nil
}

func (t *Tool) readSchema(ctx context.Context, name string) (tool.Definition, error) {
	if err := t.checkSource(ctx); err != nil {
		return tool.Definition{}, err
	}
	for _, candidate := range t.entries {
		if candidate.def.Name != name {
			continue
		}
		if t.source != nil {
			current := buildEntries(t.source.Tools())
			for _, item := range current {
				if item.def.Name == name && sameSearchDefinition(item.def, candidate.def) {
					return tool.CloneDefinition(candidate.def), nil
				}
			}
			return tool.Definition{}, fmt.Errorf("ToolSearch schema for %q changed or became unavailable", name)
		}
		return tool.CloneDefinition(candidate.def), nil
	}
	return tool.Definition{}, fmt.Errorf("ToolSearch schema for %q is outside the current scope", name)
}

// Replay aliases only translate historical tool names. They do not change the
// selected tool's schema, execution identity, or current authorization scope.
func sameSearchDefinition(a, b tool.Definition) bool {
	a, b = tool.CloneDefinition(a), tool.CloneDefinition(b)
	delete(a.Metadata, tool.MetadataReplayAliases)
	delete(b.Metadata, tool.MetadataReplayAliases)
	if len(a.Metadata) == 0 {
		a.Metadata = nil
	}
	if len(b.Metadata) == 0 {
		b.Metadata = nil
	}
	return reflect.DeepEqual(a, b)
}

func parseRequest(raw json.RawMessage) (request, error) {
	var args request
	if len(raw) == 0 {
		return args, tool.NewError(tool.ErrorCodeInvalidInput, "ToolSearch query is required")
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return args, fmt.Errorf("decode ToolSearch input: %w", err)
	}
	if err := tool.RejectUnknownArgs(values, "query", "limit"); err != nil {
		return args, err
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, fmt.Errorf("decode ToolSearch input: %w", err)
	}
	args.Query = strings.TrimSpace(args.Query)
	if args.Query == "" {
		return args, tool.NewError(tool.ErrorCodeInvalidInput, "ToolSearch query is required")
	}
	if utf8.RuneCountInString(args.Query) > maxQueryRunes {
		return args, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("ToolSearch query must be at most %d characters", maxQueryRunes))
	}
	_, limitProvided := values["limit"]
	if limitProvided && args.Limit < 1 {
		return args, tool.NewError(tool.ErrorCodeInvalidInput, "ToolSearch limit must be at least 1")
	}
	if args.Limit > maxLimit {
		return args, tool.NewError(tool.ErrorCodeInvalidInput, fmt.Sprintf("ToolSearch limit must be at most %d", maxLimit))
	}
	if !limitProvided {
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
		score := scoreText(searchText(item.def), terms)
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
	return truncateRunes(value, maxSourceMetadataRunes)
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit]))
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
