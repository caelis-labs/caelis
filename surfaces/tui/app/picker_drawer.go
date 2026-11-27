package tuiapp

import (
	"image/color"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const pickerDrawerVisibleItems = 10

type pickerKeycapItem struct {
	key   string
	label string
}

type pickerEffortHit struct {
	x        int
	width    int
	midpoint int
}

type pickerModelRowSpec struct {
	name        string
	source      string
	efforts     []string
	effort      string
	effortIndex int
}

type pickerModelTableLayout struct {
	contentWidth    int
	innerWidth      int
	identityWidth   int
	colGap          int
	gaugeWidth      int
	gaugeGap        int
	chevronWidth    int
	effortSlotWidth int
	effortColWidth  int
}

func (m *Model) pickerDrawerHeaderLines(
	contentWidth int,
	ancestors []string,
	title string,
	scroll completionScrollAffordance,
	showRule bool,
) []string {
	inner := max(1, contentWidth-2)
	scrollText := ""
	switch {
	case scroll.CanUp && scroll.CanDown:
		scrollText = "↑↓"
	case scroll.CanUp:
		scrollText = "↑"
	case scroll.CanDown:
		scrollText = "↓"
	}
	scrollWidth := displayColumns(scrollText)
	crumbWidth := inner
	if scrollWidth > 0 && inner > scrollWidth+2 {
		crumbWidth = inner - scrollWidth - 1
	} else {
		scrollText = ""
		scrollWidth = 0
	}
	crumb := tuikit.RenderBreadcrumb(m.theme, crumbWidth, ancestors, title)
	line := " " + padRightDisplay(crumb, crumbWidth)
	if scrollWidth > 0 {
		line += " " + m.theme.Tokens().ListMeta.Render(scrollText)
	}
	line += " "
	lines := []string{line}
	if showRule {
		lines = append(lines, " "+m.theme.DiffPanelBorderStyle().Render(strings.Repeat("─", inner))+" ")
	}
	return lines
}

func (m *Model) renderPickerKeycapFooter(full, compact, minimal []pickerKeycapItem, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	candidates := [][]pickerKeycapItem{full, compact, minimal}
	for idx, items := range candidates {
		if len(items) == 0 {
			continue
		}
		rendered := m.formatPickerKeycaps(items)
		if displayColumns(rendered) <= maxWidth || idx == len(candidates)-1 {
			return truncateTailDisplay(rendered, maxWidth)
		}
	}
	return ""
}

func (m *Model) formatPickerKeycaps(items []pickerKeycapItem) string {
	keyStyle := m.theme.TextStyle().Bold(true)
	hintStyle := m.theme.Tokens().ListMeta
	parts := make([]string, 0, len(items))
	for _, item := range items {
		if item.key == "" && item.label == "" {
			continue
		}
		if item.label == "" {
			parts = append(parts, keyStyle.Render(item.key))
		} else {
			parts = append(parts, keyStyle.Render(item.key)+" "+hintStyle.Render(item.label))
		}
	}
	return strings.Join(parts, "   ")
}

func newPickerModelTableLayout(contentWidth int, specs []pickerModelRowSpec) pickerModelTableLayout {
	inner := max(1, contentWidth-2)
	maxGauge := 0
	maxEffort := 0
	for _, spec := range specs {
		if len(spec.efforts) > 1 {
			maxGauge = max(maxGauge, len(spec.efforts))
		}
		if w := displayColumns(spec.effort); w > maxEffort {
			maxEffort = w
		}
	}

	gaugeWidth, gaugeGap := 0, 0
	if inner >= 60 && maxGauge > 1 {
		gaugeWidth = maxGauge
		gaugeGap = 2
	}
	chevronWidth := 0
	if maxEffort > 0 {
		if inner >= 32 {
			chevronWidth = 2
		} else if inner >= 22 {
			chevronWidth = 1
		}
	}

	minIdentity := min(10, max(4, inner/3))
	maxBudget := max(0, inner-2-2-minIdentity) // prefix(2) + colGap(2)
	effortSlotWidth := chevronWidth*2 + maxEffort
	if maxEffort == 0 {
		effortSlotWidth = 0
	}
	if gaugeWidth+gaugeGap+effortSlotWidth > maxBudget {
		gaugeWidth, gaugeGap = 0, 0
	}
	if effortSlotWidth > maxBudget && chevronWidth > 1 {
		chevronWidth = 1
		effortSlotWidth = chevronWidth*2 + maxEffort
	}
	if effortSlotWidth > maxBudget && chevronWidth > 0 {
		chevronWidth = 0
		effortSlotWidth = maxEffort
	}
	effortColWidth := min(maxBudget, gaugeWidth+gaugeGap+effortSlotWidth)

	colGap := 0
	if effortColWidth > 0 {
		colGap = 2
	}
	maxAllowedIdentity := max(4, inner-2-colGap-effortColWidth)
	maxNaturalIdentity := 0
	for _, spec := range specs {
		w := displayColumns(spec.name)
		if spec.source != "" {
			w += 2 + displayColumns(spec.source)
		}
		if w > maxNaturalIdentity {
			maxNaturalIdentity = w
		}
	}
	identityWidth := maxAllowedIdentity
	if maxNaturalIdentity > 0 {
		identityWidth = min(maxNaturalIdentity, maxAllowedIdentity)
	}

	return pickerModelTableLayout{
		contentWidth:    contentWidth,
		innerWidth:      inner,
		identityWidth:   identityWidth,
		colGap:          colGap,
		gaugeWidth:      gaugeWidth,
		gaugeGap:        gaugeGap,
		chevronWidth:    chevronWidth,
		effortSlotWidth: min(effortSlotWidth, effortColWidth),
		effortColWidth:  effortColWidth,
	}
}

func (m *Model) renderPickerModelRow(
	layout pickerModelTableLayout,
	spec pickerModelRowSpec,
	selected bool,
) (string, pickerEffortHit) {
	prefix := "  "
	if selected {
		prefix = "› "
	}

	nameText, sourceText := fitPickerModelIdentity(spec.name, spec.source, layout.identityWidth)
	identityUsed := displayColumns(nameText)
	if sourceText != "" {
		identityUsed += 2 + displayColumns(sourceText)
	}
	identityTrail := strings.Repeat(" ", max(0, layout.identityWidth-identityUsed))

	var (
		baseStyle   lipgloss.Style
		prefixStyle lipgloss.Style
		nameStyle   lipgloss.Style
		sourceStyle lipgloss.Style
		effortStyle lipgloss.Style
		mutedStyle  lipgloss.Style
		bgColor     color.Color
	)
	if selected {
		baseStyle = m.theme.SelectionStyle()
		bgColor = baseStyle.GetBackground()
		nameStyle = baseStyle.Bold(true)
		if baseStyle.GetReverse() || m.theme.NoColor {
			prefixStyle = baseStyle
			sourceStyle = baseStyle
			effortStyle = baseStyle
			mutedStyle = baseStyle
		} else {
			prefixStyle = baseStyle.Foreground(m.theme.ReadableTextColor(m.theme.Tokens().Accent.GetForeground(), bgColor))
			sourceStyle = baseStyle.Foreground(m.theme.ReadableTextColor(m.theme.Tokens().ListMeta.GetForeground(), bgColor))
			effortStyle = baseStyle
			mutedStyle = sourceStyle
		}
	} else {
		baseStyle = m.theme.TextStyle()
		bgColor = m.theme.ModalBg
		prefixStyle = baseStyle
		nameStyle = baseStyle
		sourceStyle = m.theme.Tokens().ListMeta
		effortStyle = m.theme.SecondaryTextStyle()
		mutedStyle = m.theme.Tokens().ListMeta
	}

	var b strings.Builder
	b.WriteString(baseStyle.Render(" "))
	b.WriteString(prefixStyle.Render(prefix))
	b.WriteString(nameStyle.Render(nameText))
	if sourceText != "" {
		b.WriteString(baseStyle.Render("  "))
		b.WriteString(sourceStyle.Render(sourceText))
	}
	b.WriteString(baseStyle.Render(identityTrail))

	var hit pickerEffortHit
	if layout.effortColWidth > 0 && layout.colGap > 0 {
		b.WriteString(baseStyle.Render(strings.Repeat(" ", layout.colGap)))
		effortX := 1 + 2 + layout.identityWidth + layout.colGap
		effortBlock, midpointOffset := m.renderPickerEffortColumn(layout, spec, selected, bgColor, baseStyle, effortStyle, mutedStyle)
		b.WriteString(effortBlock)
		if len(spec.efforts) > 1 {
			hit = pickerEffortHit{
				x:        effortX,
				width:    layout.effortColWidth,
				midpoint: effortX + midpointOffset,
			}
		}
	}

	used := 2 + layout.identityWidth + layout.colGap + layout.effortColWidth
	if pad := max(0, layout.innerWidth-used); pad > 0 {
		b.WriteString(baseStyle.Render(strings.Repeat(" ", pad)))
	}
	b.WriteString(baseStyle.Render(" "))
	return b.String(), hit
}

func fitPickerModelIdentity(name, source string, width int) (string, string) {
	if width <= 0 {
		return "", ""
	}
	name = strings.TrimSpace(name)
	source = strings.TrimSpace(source)
	if source == "" {
		return ansi.Truncate(name, width, "…"), ""
	}
	sourceWidth := displayColumns(source)
	if width >= sourceWidth+6 {
		nameBudget := width - sourceWidth - 2
		return ansi.Truncate(name, nameBudget, "…"), source
	}
	return ansi.Truncate(name+"  "+source, width, "…"), ""
}

func (m *Model) renderPickerEffortColumn(
	layout pickerModelTableLayout,
	spec pickerModelRowSpec,
	selected bool,
	bg color.Color,
	baseStyle, effortStyle, mutedStyle lipgloss.Style,
) (string, int) {
	var b strings.Builder
	multi := len(spec.efforts) > 1
	index := spec.effortIndex
	if index < 0 && spec.effort != "" {
		index = slices.Index(spec.efforts, spec.effort)
	}
	if layout.gaugeWidth > 0 {
		if multi && index >= 0 {
			filled := min(layout.gaugeWidth, max(1, index+1))
			total := min(layout.gaugeWidth, len(spec.efforts))
			for step := range filled {
				style := effortStyle
				if c := m.theme.EffortBlockColor(step, total, bg); c != nil {
					style = baseStyle.Foreground(c)
				}
				b.WriteString(style.Render("■"))
			}
			for step := filled; step < total; step++ {
				b.WriteString(mutedStyle.Render("□"))
			}
			if pad := layout.gaugeWidth - total; pad > 0 {
				b.WriteString(baseStyle.Render(strings.Repeat(" ", pad)))
			}
		} else {
			b.WriteString(baseStyle.Render(strings.Repeat(" ", layout.gaugeWidth)))
		}
		if layout.gaugeGap > 0 {
			b.WriteString(baseStyle.Render(strings.Repeat(" ", layout.gaugeGap)))
		}
	}

	leftChevron, rightChevron := "", ""
	switch layout.chevronWidth {
	case 2:
		leftChevron, rightChevron = "  ", "  "
		if selected && multi {
			leftChevron, rightChevron = "‹ ", " ›"
		}
	case 1:
		leftChevron, rightChevron = " ", " "
		if selected && multi {
			leftChevron, rightChevron = "‹", "›"
		}
	}

	labelBudget := max(0, layout.effortSlotWidth-displayColumns(leftChevron)-displayColumns(rightChevron))
	label := ansi.Truncate(spec.effort, labelBudget, "…")
	labelPad := max(0, labelBudget-displayColumns(label))

	leftStyle := mutedStyle
	rightStyle := mutedStyle
	textStyle := effortStyle
	if !multi {
		textStyle = mutedStyle
	}
	if selected && multi {
		if index <= 0 {
			leftStyle = mutedStyle
		} else {
			leftStyle = effortStyle
		}
		if index < 0 || index >= len(spec.efforts)-1 {
			rightStyle = mutedStyle
		} else {
			rightStyle = effortStyle
		}
	}

	if leftChevron != "" {
		b.WriteString(leftStyle.Render(leftChevron))
	}
	b.WriteString(textStyle.Render(label))
	if labelPad > 0 {
		b.WriteString(baseStyle.Render(strings.Repeat(" ", labelPad)))
	}
	if rightChevron != "" {
		b.WriteString(rightStyle.Render(rightChevron))
	}

	used := layout.gaugeWidth + layout.gaugeGap + displayColumns(leftChevron) + displayColumns(label) + labelPad + displayColumns(rightChevron)
	if trail := max(0, layout.effortColWidth-used); trail > 0 {
		b.WriteString(baseStyle.Render(strings.Repeat(" ", trail)))
	}
	midpointOffset := layout.gaugeWidth + layout.gaugeGap + displayColumns(leftChevron) + max(1, displayColumns(label)/2)
	return b.String(), midpointOffset
}

func (m *Model) renderPickerSimpleRow(label, detail string, selected bool, contentWidth, labelWidth int) string {
	inner := max(1, contentWidth-2)
	prefix := "  "
	if selected {
		prefix = "› "
	}
	var baseStyle, prefixStyle, labelStyle, detailStyle lipgloss.Style
	if selected {
		baseStyle = m.theme.SelectionStyle()
		labelStyle = baseStyle.Bold(true)
		if baseStyle.GetReverse() || m.theme.NoColor {
			prefixStyle = baseStyle
			detailStyle = baseStyle
		} else {
			bg := baseStyle.GetBackground()
			prefixStyle = baseStyle.Foreground(m.theme.ReadableTextColor(m.theme.Tokens().Accent.GetForeground(), bg))
			detailStyle = baseStyle.Foreground(m.theme.ReadableTextColor(m.theme.Tokens().ListMeta.GetForeground(), bg))
		}
	} else {
		baseStyle = m.theme.TextStyle()
		prefixStyle = baseStyle
		labelStyle = baseStyle
		detailStyle = m.theme.Tokens().ListMeta
	}

	avail := max(1, inner-2)
	detail = strings.TrimSpace(detail)
	if detail == "" {
		text := ansi.Truncate(strings.TrimSpace(label), avail, "…")
		pad := max(0, avail-displayColumns(text))
		return baseStyle.Render(" ") + prefixStyle.Render(prefix) + labelStyle.Render(text) + baseStyle.Render(strings.Repeat(" ", pad)+" ")
	}
	if labelWidth <= 0 || avail <= labelWidth+2 {
		nameText, sourceText := fitPickerModelIdentity(label, detail, avail)
		used := displayColumns(nameText)
		var out strings.Builder
		out.WriteString(baseStyle.Render(" "))
		out.WriteString(prefixStyle.Render(prefix))
		out.WriteString(labelStyle.Render(nameText))
		if sourceText != "" {
			used += 2 + displayColumns(sourceText)
			out.WriteString(baseStyle.Render("  "))
			out.WriteString(detailStyle.Render(sourceText))
		}
		pad := max(0, avail-used)
		out.WriteString(baseStyle.Render(strings.Repeat(" ", pad) + " "))
		return out.String()
	}
	labelBudget := min(labelWidth, max(1, avail-4))
	labelText := padRightDisplay(ansi.Truncate(strings.TrimSpace(label), labelBudget, "…"), labelBudget)
	detailBudget := max(0, avail-labelBudget-2)
	detailText := ansi.Truncate(detail, detailBudget, "…")
	trail := max(0, avail-labelBudget-2-displayColumns(detailText))
	return baseStyle.Render(" ") + prefixStyle.Render(prefix) + labelStyle.Render(labelText) + baseStyle.Render("  ") + detailStyle.Render(detailText) + baseStyle.Render(strings.Repeat(" ", trail)+" ")
}

func pickerProviderFromConfigID(modelConfigID string) string {
	source, _, _ := strings.Cut(strings.TrimSpace(modelConfigID), "/")
	return strings.TrimSuffix(strings.TrimSuffix(source, "@default"), "@local")
}
