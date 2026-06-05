package agentprofile

import (
	"strings"
	"testing"
	"time"
)

func TestParseMarkdownUsesFrontMatterAndBody(t *testing.T) {
	profile, err := ParseMarkdown("/tmp/Reviewer.md", []byte(`---
id: Review
name: Code Reviewer
description: Finds regressions
capabilities: review, tests
source: built-in
built_in: true
system_managed: true
---

Focus on bugs, regressions, and missing validation.
`))
	if err != nil {
		t.Fatalf("ParseMarkdown() error = %v", err)
	}
	if profile.ID != "review" || profile.Name != "Code Reviewer" || profile.Description != "Finds regressions" {
		t.Fatalf("profile identity = %#v", profile)
	}
	if got := strings.Join(profile.Capabilities, ","); got != "review,tests" {
		t.Fatalf("capabilities = %q", got)
	}
	if profile.Instructions != "Focus on bugs, regressions, and missing validation." {
		t.Fatalf("instructions = %q", profile.Instructions)
	}
	if got, _ := profile.Metadata["source"].(string); got != "built-in" {
		t.Fatalf("metadata source = %#v, want built-in", profile.Metadata["source"])
	}
	if got, _ := profile.Metadata["built_in"].(bool); !got {
		t.Fatalf("metadata built_in = %#v, want true", profile.Metadata["built_in"])
	}
	if got, _ := profile.Metadata["system_managed"].(bool); !got {
		t.Fatalf("metadata system_managed = %#v, want true", profile.Metadata["system_managed"])
	}
	formatted := FormatMarkdown(profile)
	reparsed, err := ParseMarkdown("/tmp/Reviewer.md", []byte(formatted))
	if err != nil {
		t.Fatalf("ParseMarkdown(FormatMarkdown()) error = %v", err)
	}
	if got, _ := reparsed.Metadata["system_managed"].(bool); !got {
		t.Fatalf("round-trip system_managed = %#v, want true", reparsed.Metadata["system_managed"])
	}
}

func TestParseMarkdownAcceptsCRLFFrontMatter(t *testing.T) {
	profile, err := ParseMarkdown("/tmp/Filename.md", []byte("---\r\nid: windows_id\r\nname: Windows Profile\r\ndescription: CRLF profile\r\ncapabilities: review, tests\r\n---\r\n\r\nUse CRLF front matter.\r\n"))
	if err != nil {
		t.Fatalf("ParseMarkdown(CRLF) error = %v", err)
	}
	if profile.ID != "windows-id" {
		t.Fatalf("profile ID = %q, want front matter ID windows-id", profile.ID)
	}
	if profile.Name != "Windows Profile" || profile.Description != "CRLF profile" {
		t.Fatalf("profile identity = %#v", profile)
	}
	if profile.Instructions != "Use CRLF front matter." {
		t.Fatalf("instructions = %q, want CRLF body only", profile.Instructions)
	}
	if got := strings.Join(profile.Capabilities, ","); got != "review,tests" {
		t.Fatalf("capabilities = %q", got)
	}
}

func TestNormalizeBindingPreservesACPAgentName(t *testing.T) {
	binding := NormalizeBinding(Binding{
		ProfileID: "Reviewer",
		Target:    BindingTargetACP,
		ACPAgent:  " my_agent ",
	})
	if binding.ProfileID != "reviewer" {
		t.Fatalf("ProfileID = %q, want reviewer", binding.ProfileID)
	}
	if binding.ACPAgent != "my_agent" {
		t.Fatalf("ACPAgent = %q, want my_agent", binding.ACPAgent)
	}
}

func TestBindingSetUpsertAndRemove(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	set, err := UpsertBinding(BindingSet{}, Binding{
		ProfileID:       "Reviewer",
		Target:          "built-in",
		Model:           "deepseek/deepseek-v4-pro",
		ReasoningEffort: "HIGH",
	}, now)
	if err != nil {
		t.Fatalf("UpsertBinding() error = %v", err)
	}
	binding, ok := LookupBinding(set, "reviewer")
	if !ok {
		t.Fatal("LookupBinding() ok = false")
	}
	if binding.ProfileID != "reviewer" || binding.Target != BindingTargetBuiltIn || binding.ReasoningEffort != "high" {
		t.Fatalf("binding = %#v", binding)
	}
	if binding.UpdatedAt != now {
		t.Fatalf("UpdatedAt = %v, want %v", binding.UpdatedAt, now)
	}
	set = RemoveBinding(set, "reviewer")
	if _, ok := LookupBinding(set, "reviewer"); ok {
		t.Fatal("LookupBinding() ok = true after remove")
	}
}
