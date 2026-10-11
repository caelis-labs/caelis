package codex

import (
	"encoding/json"
	"net/url"
	"testing"
)

// These pure grammar fixtures cover Windows paths on any test host. They do
// not assert physical Windows app-server interoperability or read these paths.
func TestDraftNativeReferenceURIAbsolutePaths(t *testing.T) {
	for _, test := range []struct {
		name, native, uri, host, decodedPath string
	}{
		{"drive-backslash", `C:\workspace\skills\pdf\SKILL.md`, "file:///C:/workspace/skills/pdf/SKILL.md", "", "/C:/workspace/skills/pdf/SKILL.md"},
		{"drive-slash", "d:/workspace/skills/pdf/SKILL.md", "file:///d:/workspace/skills/pdf/SKILL.md", "", "/d:/workspace/skills/pdf/SKILL.md"},
		{"drive-escaped", `C:\workspace\报告 #1+100%\SKILL.md`, "file:///C:/workspace/%E6%8A%A5%E5%91%8A%20%231+100%25/SKILL.md", "", "/C:/workspace/报告 #1+100%/SKILL.md"},
		{"unc-backslash", `\\server\share\skills\pdf\SKILL.md`, "file://server/share/skills/pdf/SKILL.md", "server", "/share/skills/pdf/SKILL.md"},
		{"unc-slash", "//server/share/skills/pdf/SKILL.md", "file://server/share/skills/pdf/SKILL.md", "server", "/share/skills/pdf/SKILL.md"},
		{"unc-escaped", `\\server\share\报告 #1+100%\SKILL.md`, "file://server/share/%E6%8A%A5%E5%91%8A%20%231+100%25/SKILL.md", "server", "/share/报告 #1+100%/SKILL.md"},
		{"unc-share-root", `\\server\share`, "file://server/share", "server", "/share"},
		{"mac-escaped", "/missing/报告 #1+100%/SKILL.md", "file:///missing/%E6%8A%A5%E5%91%8A%20%231+100%25/SKILL.md", "", "/missing/报告 #1+100%/SKILL.md"},
	} {
		t.Run(test.name, func(t *testing.T) {
			uri := draftNativeReferenceURI(test.native)
			if uri != test.uri {
				t.Fatalf("native reference %q -> %q, want %q", test.native, uri, test.uri)
			}
			parsed, err := url.Parse(uri)
			if err != nil || parsed.Scheme != "file" || parsed.Host != test.host || parsed.Path != test.decodedPath || parsed.Opaque != "" || parsed.Fragment != "" || parsed.RawQuery != "" {
				t.Fatalf("reference does not round-trip as file URI: %+v, %v", parsed, err)
			}
			for _, kind := range []string{"skill", "mention"} {
				raw, _ := json.Marshal(map[string]any{"type": kind, "name": "selected native reference", "path": test.native})
				block, err := draftUserContent(raw)
				if err != nil || block.ResourceLink == nil || block.ResourceLink.Name != "selected native reference" || block.ResourceLink.Uri != uri {
					t.Fatalf("native %s reference lost name/target: %+v, %v", kind, block, err)
				}
			}
		})
	}
}

func TestDraftNativeReferenceURIKeepsURIsAndRelativeReferences(t *testing.T) {
	for _, native := range []string{
		"app://calendar?view=week#selected", "mcp://docs", "skill://team/skill", "plugin://sample@test?app=com.example.editor",
		"file:///C:/existing%20uri/SKILL.md", "file://server/share/README.md",
		"docs/README.md", `docs\README.md`, `C:relative\SKILL.md`, `\root-relative\SKILL.md`, "", "relative #1+100%/报告.md",
	} {
		t.Run(native, func(t *testing.T) {
			if uri := draftNativeReferenceURI(native); uri != native {
				t.Fatalf("reference changed from %q to %q", native, uri)
			}
		})
	}
}
