package tuiapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/surfaces/internal/statusbar"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestFooterOmitsYoloBadgeByDefault(t *testing.T) {
	t.Parallel()

	model := newFooterYoloTestModel(Config{Workspace: "caelis"})
	footer := ansi.Strip(model.footerRowText())
	if strings.Contains(footer, statusbar.FooterYoloLabel) {
		t.Fatalf("footer = %q, want no YOLO badge by default", footer)
	}
}

func TestFooterPlacesFastModeLightningNextToModel(t *testing.T) {
	t.Parallel()

	model := newFooterYoloTestModel(Config{Workspace: "caelis"})
	model.statusView.Model = "openai-codex/gpt-5.6-sol [xhigh]"
	model.statusView.FastMode = true
	footer := ansi.Strip(model.footerRowText())
	if !strings.Contains(footer, "⚡openai-codex/gpt-5.6-sol [xhigh]") {
		t.Fatalf("footer = %q, want lightning adjacent to model", footer)
	}
	if strings.Contains(footer, "⚡ openai-codex") {
		t.Fatalf("footer = %q, contains an extra space after lightning", footer)
	}
}

func TestFooterShowsYoloBadgeFromLaunchConfig(t *testing.T) {
	t.Parallel()

	model := newFooterYoloTestModel(Config{
		Workspace:      "caelis",
		FullAccessMode: true,
	})
	model.statusModel = "gpt-5.6"
	footer := ansi.Strip(model.footerRowText())
	if !strings.HasPrefix(strings.TrimSpace(footer), statusbar.FooterYoloLabel+" · ") {
		t.Fatalf("footer = %q, want YOLO prefix before model/workspace", footer)
	}
	if strings.Contains(footer, "DANGER") || strings.Contains(footer, "not a security boundary") {
		t.Fatalf("footer = %q, want compact badge without transcript warning copy", footer)
	}
}

func TestFooterShowsYoloBadgeFromStatusSnapshot(t *testing.T) {
	t.Parallel()

	model := newFooterYoloTestModel(Config{Workspace: "caelis"})
	model.statusView.FullAccess = true
	footer := ansi.Strip(model.footerRowText())
	if !strings.Contains(footer, statusbar.FooterYoloLabel) {
		t.Fatalf("footer = %q, want YOLO badge from live status", footer)
	}
}

func TestFooterKeepsYoloBadgeAfterHistoryClear(t *testing.T) {
	t.Parallel()

	model := newFooterYoloTestModel(Config{
		Workspace:       "caelis",
		ShowWelcomeCard: true,
		FullAccessMode:  true,
	})
	model.handleUserMessageMsg(UserMessageMsg{Text: "hello"})
	next, _ := model.Update(ClearHistoryMsg{})
	model = next.(*Model)
	footer := ansi.Strip(model.footerRowText())
	if !strings.Contains(footer, statusbar.FooterYoloLabel) {
		t.Fatalf("footer after /new = %q, want persistent YOLO badge", footer)
	}
	for _, block := range model.doc.Blocks() {
		if slash, ok := block.(*slashOutputBlock); ok {
			if strings.Contains(slashOutputPlainForTest(slash.lines), "YOLO mode is active") {
				t.Fatalf("transcript still contains YOLO paragraph: %#v", slash.lines)
			}
		}
	}
}

func TestFooterYoloBadgeUsesWarningColor(t *testing.T) {
	model := NewModel(Config{
		Workspace:      "caelis",
		FullAccessMode: true,
	})
	model.width = 100
	model.height = 32
	model.ready = true
	model.theme = tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model.themeCacheKey = ""
	model.statusModel = "gpt-5.6"

	styled := model.renderFooterRowStyledText()
	warningFG := sgrForegroundCode(t, model.theme.WarnStyle().GetForeground())
	if got := normalizeInlineStyleText(textWithSGRForeground(styled, warningFG)); got != statusbar.FooterYoloLabel {
		t.Fatalf("warning foreground covered %q, want only the YOLO badge", got)
	}
}

func TestWelcomeCardDoesNotSeedYoloTranscriptWarning(t *testing.T) {
	t.Parallel()

	model := newWelcomeTestModel(t, 80, 24, Config{FullAccessMode: true})
	plain := strings.Join(model.viewportPlainLines, "\n")
	if strings.Contains(plain, "DANGER") || strings.Contains(plain, "YOLO mode is active") {
		t.Fatalf("welcome transcript still contains YOLO warning paragraph:\n%s", plain)
	}
	footer := ansi.Strip(model.footerRowText())
	if !strings.Contains(footer, statusbar.FooterYoloLabel) {
		t.Fatalf("footer = %q, want YOLO badge while welcome is visible", footer)
	}
}

func newFooterYoloTestModel(cfg Config) *Model {
	cfg.NoColor = true
	cfg.NoAnimation = true
	model := NewModel(cfg)
	model.width = 100
	model.height = 32
	model.ready = true
	return model
}
