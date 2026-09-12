package tuikit

import (
	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/colorprofile"
)

// Immutable derived styles preserve upstream token roles and keep low-contrast
// comments/accents readable. Register distinct names so no other Chroma consumer
// sees its upstream style mutated and theme switches cannot share a mutable slot.
var communitySyntaxStyles = buildCommunitySyntaxStyles()

func buildCommunitySyntaxStyles() map[string]*chroma.Style {
	result := make(map[string]*chroma.Style)
	for _, name := range []string{"catppuccin-mocha", "catppuccin-latte", "nord", "dracula", "solarized-dark"} {
		source := styles.Get(name)
		background := source.Get(chroma.Background).Background
		bg := lipgloss.Color(background.String())
		builder := chroma.NewStyleBuilder("caelis-" + name)
		for _, token := range source.Types() {
			entry := source.Get(token)
			if entry.Colour.IsSet() {
				color := readablePaletteColor(lipgloss.Color(entry.Colour.String()), bg, 4.5, colorIsDark(bg), colorprofile.TrueColor)
				r, g, b, _ := rgb8(color)
				entry.Colour = chroma.NewColour(r, g, b)
			}
			builder.AddEntry(token, entry)
		}
		style, err := builder.Build()
		if err != nil {
			panic(err)
		} // All descriptors originate from parsed Chroma entries.
		result[name] = styles.Register(style)
	}
	return result
}
