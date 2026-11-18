package tuiapp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/httpclient"
	"github.com/charmbracelet/x/ansi"
)

func TestConnectWizardRendersDescriptionsWithoutLinks(t *testing.T) {
	const endpoint = "https://api.deepseek.com/anthropic"
	const docs = "HTTP://ex.test/help"
	const diagnostic = "Check https://ex.test/status"
	m, _ := wizardOverlayFixture(t)
	connectPress(m, "enter")
	connectPaste(m, "deepseek")
	connectPress(m, "enter")
	m.slashArgCandidates[0].Detail = ansi.SetHyperlink(docs) + docs + ansi.ResetHyperlink()
	m.wizardOverlay.err = diagnostic

	frame := m.View().Content
	updates := renderFullscreenFramesForTest(t, m.width, m.height, frame)
	if strings.Contains(updates[0], "\x1b]8;") {
		t.Fatalf("wizard emitted an OSC 8 hyperlink:\n%q", updates[0])
	}
	plain := ansi.Strip(updates[0])
	if strings.Contains(strings.ToLower(plain), "http://") || strings.Contains(strings.ToLower(plain), "https://") {
		t.Fatalf("wizard emitted a URL that xterm can autolink:\n%q", plain)
	}
	visible := wizardVisibleText(updates[0])
	for _, text := range []string{endpoint, docs, diagnostic} {
		if !strings.Contains(visible, text) {
			t.Fatalf("missing visible text %q:\n%q", text, visible)
		}
	}
	if !strings.Contains(strings.Join(m.wizardOverlay.text.lines, "\n"), diagnostic) {
		t.Fatal("read-only text selection did not preserve the original URL")
	}
	t.Log("\n" + wizardVisibleText(frame))
}

func wizardVisibleText(frame string) string {
	return strings.ReplaceAll(ansi.Strip(frame), "\u034f", "")
}

func TestWizardOverlayDistinguishesEmptyCatalogFromFailure(t *testing.T) {
	const message = `Install Codex so "codex" is available on the Host's PATH.`
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "empty", want: "Nothing configured"},
		{name: "failure", err: &httpclient.RemoteError{StatusCode: http.StatusBadRequest, Detail: message}, want: message},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := wizardOverlayFixture(t)
			m.clearWizard()
			m.cfg.SlashArgComplete = func(context.Context, string, string, int) ([]SlashArgCandidate, error) {
				return nil, tc.err
			}
			runConnectTestCmd(m, m.openSlashArgPicker("connect"))
			plain := ansi.Strip(m.renderWizardOverlay())
			if !strings.Contains(plain, tc.want) || (tc.err != nil && (strings.Contains(plain, "Nothing configured") || strings.Contains(plain, "HTTP 400"))) {
				t.Fatalf("expected %q in the wizard frame:\n%s", tc.want, plain)
			}
			t.Log(plain)
		})
	}
}
