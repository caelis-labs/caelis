package tuiapp

import (
	"bytes"

	gansi "charm.land/glamour/v2/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// narrativeMarkdown keeps Glamour's ANSI layout, but normalizes parsed link
// targets before it generates and wraps OSC 8. Normalizing the rendered output
// is too late: a UTF-8 target can already have corrupted Glamour's line layout.
// Displayed hrefs use URI encoding; link labels and other source text are intact.
type narrativeMarkdown struct {
	markdown goldmark.Markdown
	width    int
}

func newNarrativeMarkdown(options gansi.Options) *narrativeMarkdown {
	// Goldmark distinguishes explicit Markdown line breaks from soft newlines.
	// Keep explicit breaks through Glamour's paragraph layout.
	options.PreserveNewLines = true
	markdown := goldmark.New(
		goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
	)
	// Install after extensions so their HTML renderers cannot replace Glamour's
	// table, strikethrough, and definition-list renderers.
	markdown.SetRenderer(renderer.NewRenderer(
		renderer.WithNodeRenderers(util.Prioritized(gansi.NewRenderer(options), 1000)),
	))
	return &narrativeMarkdown{markdown: markdown, width: options.WordWrap}
}

func (r *narrativeMarkdown) Render(raw string) (string, error) {
	source := []byte(raw)
	document := r.markdown.Parser().Parse(text.NewReader(source))
	var autolinks []*ast.AutoLink
	_ = ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *ast.Text:
			if node.SoftLineBreak() && proseParagraph(node) {
				value := node.Segment.Value(source)
				start := len(source)
				source = append(source, value...)
				source = append(source, ' ')
				node.Segment = text.NewSegment(start, len(source))
				node.SetSoftLineBreak(false)
			}
		case *ast.Link:
			node.Destination = []byte(tuikit.EscapeHyperlinkURI(string(node.Destination)))
		case *ast.Image:
			node.Destination = []byte(tuikit.EscapeHyperlinkURI(string(node.Destination)))
		case *ast.AutoLink:
			autolinks = append(autolinks, node)
		}
		return ast.WalkContinue, nil
	})
	// Goldmark autolink values are source segments, not mutable destinations.
	// Append the complete encoded URL (including any implied protocol) and replace
	// only that node. Existing segments, including code and text, keep their offsets.
	for _, node := range autolinks {
		uri := string(node.URL(source))
		encoded := tuikit.EscapeHyperlinkURI(uri)
		if encoded == uri {
			continue
		}
		start := len(source)
		source = append(source, encoded...)
		link := ast.NewAutoLink(node.AutoLinkType, ast.NewTextSegment(text.NewSegment(start, len(source))))
		node.Parent().ReplaceChild(node.Parent(), node, link)
	}
	var out bytes.Buffer
	// Glamour's document word wrap can move indentation onto a line by
	// itself before a long code token. Render overflowing top-level code
	// directly, then hard-wrap the highlighted output in display cells.
	render := r.markdown.Renderer().Render
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		if !r.overflowsCodeBlock(node, source) {
			continue
		}
		before := ast.NewDocument()
		for document.FirstChild() != node {
			before.AppendChild(before, document.FirstChild())
		}
		if err := render(&out, source, before); err != nil {
			return "", err
		}
		var code bytes.Buffer
		if err := render(&code, source, node); err != nil {
			return "", err
		}
		out.WriteString(hardWrapDisplayLine(code.String(), r.width))
		// Keep the sibling boundary used for paragraph spacing.
		boundary := ast.NewTextBlock()
		document.ReplaceChild(document, node, boundary)
		node = boundary
	}
	err := render(&out, source, document)
	return out.String(), err
}

// Glamour flows paragraphs as prose, except inside list items.
func proseParagraph(node ast.Node) bool {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if parent.IsRaw() {
			return false
		}
		if parent.Kind() == ast.KindParagraph {
			return parent.Parent().Kind() != ast.KindListItem
		}
	}
	return false
}

func (r *narrativeMarkdown) overflowsCodeBlock(node ast.Node, source []byte) bool {
	if node.Kind() != ast.KindCodeBlock && node.Kind() != ast.KindFencedCodeBlock {
		return false
	}
	for i := 0; i < node.Lines().Len(); i++ {
		line := node.Lines().At(i)
		if graphemeWidth(string(line.Value(source))) > r.width {
			return true
		}
	}
	return false
}
