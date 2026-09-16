package tuiapp

import "strings"

// botPickerRowLine keeps each entry on one physical line so the shared Session
// picker's row offsets also describe mouse targets. Reserve readable name space
// before allocating the remainder to the model and current marker.
func botPickerRowLine(inner int, prefix, label, model string, current bool) string {
	available := inner - displayColumns(prefix)
	nameReserve := minInt(12, maxInt(1, available/2))
	suffixBudget := maxInt(0, available-2-nameReserve)
	suffix := truncateTailDisplay(model, suffixBudget)
	if current {
		suffix = truncateTailDisplay("current", suffixBudget)
		modelBudget := suffixBudget - displayColumns(suffix) - 2
		if modelBudget >= 8 {
			suffix = truncateTailDisplay(model, modelBudget) + "  " + suffix
		}
	}
	name := truncateTailDisplay(label, maxInt(1, available-2-displayColumns(suffix)))
	return prefix + name + strings.Repeat(" ", maxInt(0, available-displayColumns(name)-displayColumns(suffix))) + suffix
}
