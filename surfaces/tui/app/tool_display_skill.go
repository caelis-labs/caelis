package tuiapp

import (
	"encoding/xml"
	"strings"
)

func skillContentDisplayNameFromRaw(raw map[string]any) string {
	return skillContentNameFromFragment(toolPath(raw))
}

// skillContentNameFromTitle returns the skill name from a direct or Read-prefixed
// skill-content marker title.
func skillContentNameFromTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	if detail := prefixedTitleDetail(title, "Read"); detail != "" {
		if name := skillContentNameFromFragment(detail); name != "" {
			return name
		}
	}
	if strings.HasPrefix(strings.ToLower(title), "<skill_content") {
		return skillContentNameFromFragment(title)
	}
	return ""
}

func skillContentNameFromFragment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	idx := strings.Index(strings.ToLower(value), "<skill_content")
	if idx < 0 {
		return ""
	}
	fragment := strings.TrimSpace(value[idx:])
	end := strings.Index(fragment, ">")
	if end < 0 {
		return ""
	}
	decoder := xml.NewDecoder(strings.NewReader(fragment[:end+1]))
	for {
		token, err := decoder.Token()
		if err != nil {
			return ""
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if !strings.EqualFold(start.Name.Local, "skill_content") {
			return ""
		}
		for _, attr := range start.Attr {
			if strings.EqualFold(attr.Name.Local, "name") {
				return strings.TrimSpace(attr.Value)
			}
		}
		return ""
	}
}
