package tuikit

import (
	"github.com/charmbracelet/colorprofile"
	"testing"
)

func TestAgentMessageDirectionColorsAcrossThemes(t *testing.T) {
	for _, name := range []string{"dark", "light", "catppuccin-mocha", "catppuccin-latte", "nord", "solarized", "dracula"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("CAELIS_THEME", name)
			t.Setenv("CAELIS_ACCENT", "#ff00ff")
			for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI} {
				theme := ResolveThemeWithState(true, false, profile)
				if theme.AgentMessageSentFg == nil || theme.AgentMessageReceivedFg == nil || colorsEqual(theme.AgentMessageSentFg, theme.AgentMessageReceivedFg) {
					t.Fatalf("indistinguishable directions: %#v", theme)
				}
				if profile == colorprofile.ANSI {
					if got := stringifyColor(theme.AgentMessageSentFg); got != "4" {
						t.Fatalf("sent ANSI = %q", got)
					}
					if got := stringifyColor(theme.AgentMessageReceivedFg); got != "6" {
						t.Fatalf("received ANSI = %q", got)
					}
				}
			}
			plain := ResolveThemeWithState(true, true, colorprofile.TrueColor)
			if plain.AgentMessageSentFg != nil || plain.AgentMessageReceivedFg != nil {
				t.Fatal("no-color retained direction colors")
			}
		})
	}
}
