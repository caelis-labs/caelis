package agentprofile

import (
	"bufio"
	"fmt"
	"strings"
)

func ParseMarkdown(path string, data []byte) (Profile, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return Profile{}, fmt.Errorf("ports/agentprofile: profile file %q is empty", strings.TrimSpace(path))
	}
	header, body := parseFrontMatter(text)
	profile := Profile{
		ID:           header["id"],
		Name:         header["name"],
		Description:  header["description"],
		Capabilities: parseCSVList(header["capabilities"]),
		Instructions: strings.TrimSpace(body),
		Path:         strings.TrimSpace(path),
		Metadata:     parseMetadata(header),
	}
	profile = NormalizeProfile(profile)
	if err := ValidateProfile(profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

func FormatMarkdown(profile Profile) string {
	profile = NormalizeProfile(profile)
	var b strings.Builder
	b.WriteString("---\n")
	if profile.ID != "" {
		b.WriteString("id: ")
		b.WriteString(profile.ID)
		b.WriteString("\n")
	}
	if profile.Name != "" {
		b.WriteString("name: ")
		b.WriteString(profile.Name)
		b.WriteString("\n")
	}
	if profile.Description != "" {
		b.WriteString("description: ")
		b.WriteString(profile.Description)
		b.WriteString("\n")
	}
	if len(profile.Capabilities) > 0 {
		b.WriteString("capabilities: ")
		b.WriteString(strings.Join(profile.Capabilities, ", "))
		b.WriteString("\n")
	}
	if value := metadataString(profile.Metadata, "source"); value != "" {
		b.WriteString("source: ")
		b.WriteString(value)
		b.WriteString("\n")
	}
	if metadataBool(profile.Metadata, "built_in") {
		b.WriteString("built_in: true\n")
	}
	if metadataBool(profile.Metadata, "system_managed") {
		b.WriteString("system_managed: true\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(strings.TrimSpace(profile.Instructions))
	b.WriteString("\n")
	return b.String()
}

func parseMetadata(header map[string]string) map[string]any {
	out := map[string]any{}
	if value := strings.TrimSpace(header["source"]); value != "" {
		out["source"] = value
	}
	for _, key := range []string{"built_in", "system_managed"} {
		if value, ok := parseFrontMatterBool(header[key]); ok {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseFrontMatterBool(value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "1", "on":
		return true, true
	case "false", "no", "0", "off":
		return false, true
	default:
		return false, false
	}
}

func metadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	switch value := metadata[key].(type) {
	case string:
		return strings.TrimSpace(value)
	default:
		return ""
	}
}

func metadataBool(metadata map[string]any, key string) bool {
	if len(metadata) == 0 {
		return false
	}
	switch value := metadata[key].(type) {
	case bool:
		return value
	case string:
		parsed, ok := parseFrontMatterBool(value)
		return ok && parsed
	default:
		return false
	}
}

func parseFrontMatter(text string) (map[string]string, string) {
	header := map[string]string{}
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "---") {
		return header, text
	}
	rest := strings.TrimPrefix(text, "---")
	switch {
	case strings.HasPrefix(rest, "\r\n"):
		rest = strings.TrimPrefix(rest, "\r\n")
	case strings.HasPrefix(rest, "\n"):
		rest = strings.TrimPrefix(rest, "\n")
	case rest == "":
		return header, text
	default:
		return header, text
	}
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return header, text
	}
	headerText := rest[:end]
	body := rest[end+len("\n---"):]
	body = strings.TrimPrefix(body, "\r\n")
	body = strings.TrimPrefix(body, "\n")
	scanner := bufio.NewScanner(strings.NewReader(headerText))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" {
			header[key] = value
		}
	}
	return header, strings.TrimSpace(body)
}

func parseCSVList(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(strings.TrimSpace(part), `"'`)
		if part != "" {
			out = append(out, part)
		}
	}
	return normalizeStringList(out)
}
