package transcript

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/web"
)

func TestResolveToolPresentationKeepsExactNameSeparateFromStandardFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		exactName       string
		kind            string
		title           string
		wantDisplay     string
		wantExploration string
		wantTitleLabel  bool
	}{
		{name: "standard read", kind: "read", title: "Read AGENTS.md", wantDisplay: "Read", wantExploration: "Read"},
		{name: "standard search", kind: "search", title: "Search ToolCall", wantDisplay: "Search", wantExploration: "Search"},
		{name: "generic other uses atomic title", kind: "other", title: "Start subagent reviewer", wantDisplay: "Start subagent reviewer", wantTitleLabel: true},
		{name: "exact builtin enriches kind", exactName: "Glob", kind: "read", title: "Read files", wantDisplay: "Glob", wantExploration: "Glob"},
		{name: "web fetch refines search kind", exactName: web.FetchToolName, kind: "search", title: "WebFetch https://example.com/a/b.md", wantDisplay: web.FetchToolName, wantExploration: "Fetch"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			presentation := ResolveToolPresentation(tt.exactName, tt.kind, tt.title)
			if presentation.Name != tt.exactName || presentation.DisplayName != tt.wantDisplay || presentation.ExplorationVerb != tt.wantExploration || presentation.TitleAsLabel != tt.wantTitleLabel {
				t.Fatalf("ResolveToolPresentation() = %#v, want exact=%q display=%q exploration=%q titleLabel=%v", presentation, tt.exactName, tt.wantDisplay, tt.wantExploration, tt.wantTitleLabel)
			}
		})
	}
}
