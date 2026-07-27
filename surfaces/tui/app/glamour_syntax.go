package tuiapp

import (
	"image/color"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/colorprofile"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func init() {
	registerCaelisChromaStyle(tuikit.Theme{
		Name:    "caelis-dusk",
		IsDark:  true,
		Profile: colorprofile.TrueColor,
	})
	registerCaelisChromaStyle(tuikit.Theme{
		Name:    "caelis-dawn",
		IsDark:  false,
		Profile: colorprofile.TrueColor,
	})
}

// registerCaelisChromaStyle installs one immutable, theme-specific style
// before rendering starts. Separate names avoid Glamour's single custom-style
// registry slot retaining the first light or dark palette rendered.
func registerCaelisChromaStyle(theme tuikit.Theme) {
	palette := tuikit.SyntaxPaletteForTheme(theme)
	styles.Register(caelisChromaStyle(palette))
}

func caelisChromaStyle(palette tuikit.SyntaxPalette) *chroma.Style {
	text := chromaColorDescriptor(palette.Text)
	comment := chromaColorDescriptor(palette.Comment)
	keyword := chromaColorDescriptor(palette.Keyword)
	function := chromaColorDescriptor(palette.Function)
	stringLiteral := chromaColorDescriptor(palette.String)
	number := chromaColorDescriptor(palette.Number)
	operator := chromaColorDescriptor(palette.Operator)
	deleted := chromaColorDescriptor(palette.Deleted)
	inserted := chromaColorDescriptor(palette.Inserted)

	return chroma.MustNewStyle(palette.ChromaTheme, chroma.StyleEntries{
		chroma.Text:                text,
		chroma.Error:               deleted,
		chroma.Comment:             comment,
		chroma.CommentPreproc:      comment,
		chroma.Keyword:             keyword,
		chroma.KeywordReserved:     keyword,
		chroma.KeywordNamespace:    keyword,
		chroma.KeywordType:         keyword,
		chroma.Operator:            operator,
		chroma.Punctuation:         text,
		chroma.Name:                text,
		chroma.NameBuiltin:         function,
		chroma.NameTag:             function,
		chroma.NameAttribute:       function,
		chroma.NameClass:           function,
		chroma.NameConstant:        function,
		chroma.NameDecorator:       function,
		chroma.NameException:       function,
		chroma.NameFunction:        function,
		chroma.NameOther:           text,
		chroma.Literal:             text,
		chroma.LiteralNumber:       number,
		chroma.LiteralDate:         number,
		chroma.LiteralString:       stringLiteral,
		chroma.LiteralStringEscape: stringLiteral,
		chroma.GenericDeleted:      deleted,
		chroma.GenericEmph:         chromaStyleDescriptor(text, "italic"),
		chroma.GenericInserted:     inserted,
		chroma.GenericStrong:       chromaStyleDescriptor(text, "bold"),
		chroma.GenericSubheading:   keyword,
	})
}

func chromaColorDescriptor(value color.Color) string {
	if value == nil {
		return ""
	}
	r, g, b, _ := value.RGBA()
	return strings.ToLower(chromaColour(r>>8, g>>8, b>>8))
}

func chromaColour(r, g, b uint32) string {
	const hex = "0123456789abcdef"
	return string([]byte{
		'#',
		hex[(r>>4)&0xf], hex[r&0xf],
		hex[(g>>4)&0xf], hex[g&0xf],
		hex[(b>>4)&0xf], hex[b&0xf],
	})
}

func chromaStyleDescriptor(color string, attributes ...string) string {
	parts := make([]string, 0, len(attributes)+1)
	if color != "" {
		parts = append(parts, color)
	}
	parts = append(parts, attributes...)
	return strings.Join(parts, " ")
}
