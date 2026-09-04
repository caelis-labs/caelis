package tuiapp

import "strings"

func slashArgCandidateIdentity(candidate SlashArgCandidate) string {
	if display := strings.TrimSpace(candidate.Display); display != "" {
		return display
	}
	return strings.TrimSpace(candidate.Value)
}

func slashArgPickerHint(command string, candidates []SlashArgCandidate, index int) string {
	if index < 0 || index >= len(candidates) {
		return ""
	}
	identity := slashArgCandidateIdentity(candidates[index])
	raw := strings.Join(strings.Fields(strings.TrimSpace(candidates[index].Detail)), " ")
	if isModelAliasPickerCommand(command) {
		return modelAliasPickerHint(identity, raw, candidates)
	}
	return sanitizeCompletionHint(identity, raw)
}

func isModelAliasPickerCommand(command string) bool {
	return strings.EqualFold(strings.TrimSpace(command), "model")
}

func modelAliasPickerHint(identity string, raw string, candidates []SlashArgCandidate) string {
	if !slashArgIdentityDuplicated(identity, candidates) {
		return ""
	}
	return shortModelAliasDisambiguator(raw)
}

func slashArgIdentityDuplicated(identity string, candidates []SlashArgCandidate) bool {
	needle := strings.ToLower(strings.TrimSpace(identity))
	if needle == "" {
		return false
	}
	count := 0
	for _, candidate := range candidates {
		if strings.ToLower(slashArgCandidateIdentity(candidate)) != needle {
			continue
		}
		count++
		if count > 1 {
			return true
		}
	}
	return false
}

func shortModelAliasDisambiguator(raw string) string {
	raw = strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if raw == "" {
		return ""
	}
	fallback := ""
	for _, part := range strings.Split(raw, "·") {
		part = strings.Join(strings.Fields(strings.TrimSpace(part)), " ")
		if part == "" {
			continue
		}
		part = strings.TrimSpace(strings.TrimPrefix(part, "endpoint:"))
		if part == "" || isOperationalCompletionHint(part) {
			continue
		}
		if part == "default" || strings.HasSuffix(part, "@default") {
			if fallback == "" {
				fallback = "default"
			}
			continue
		}
		return part
	}
	return fallback
}

func sanitizeCompletionHint(identity string, raw string) string {
	identity = strings.TrimSpace(identity)
	raw = strings.Join(strings.Fields(strings.TrimSpace(raw)), " ")
	if raw == "" || strings.EqualFold(raw, identity) || isGenericCompletionHint(raw) {
		return ""
	}
	parts := strings.Split(raw, "·")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Join(strings.Fields(strings.TrimSpace(part)), " ")
		if part == "" || isOperationalCompletionHint(part) {
			continue
		}
		kept = append(kept, part)
	}
	hint := strings.Join(kept, " · ")
	if hint == "" || strings.EqualFold(hint, identity) || isGenericCompletionHint(hint) {
		return ""
	}
	return hint
}

func isOperationalCompletionHint(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "endpoint:") ||
		lower == "managed auth" ||
		strings.Contains(value, "://")
}

func isGenericCompletionHint(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "configured model alias", "configured model":
		return true
	default:
		return false
	}
}
