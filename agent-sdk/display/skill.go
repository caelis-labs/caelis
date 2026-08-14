package display

import (
	"encoding/xml"
	"strings"
)

// SkillContentNameFromHint returns the skill name embedded in a skill-content
// marker surfaced by ACP tool titles or path-like raw input fields.
func SkillContentNameFromHint(title string, pathHint string) string {
	if name := SkillContentNameFromTitle(title); name != "" {
		return name
	}
	return skillContentNameFromFragment(pathHint)
}

// SkillContentNameFromTitle returns the skill name from a direct or Read-prefixed
// skill-content marker title.
func SkillContentNameFromTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	if detail := readTitleDetail(title); detail != "" {
		if name := skillContentNameFromFragment(detail); name != "" {
			return name
		}
	}
	if strings.HasPrefix(strings.ToLower(title), "<skill_content") {
		return skillContentNameFromFragment(title)
	}
	return ""
}

func readTitleDetail(title string) string {
	const prefix = "Read "
	title = strings.TrimSpace(title)
	if len(title) <= len(prefix) || !strings.EqualFold(title[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(title[len(prefix):])
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
