package promptrefs

import (
	"os"
	"path/filepath"
	"strings"
)

// ProjectionOptions provides the workspace and skill names used to resolve
// scanned submission reference tokens.
type ProjectionOptions struct {
	WorkspaceDir string
	SkillNames   map[string]string
}

// Projection is the model-visible text produced by resolving submission
// shorthand references.
type Projection struct {
	Text    string
	Changed bool
}

type projectionToken struct {
	kind        Kind
	start       int
	end         int
	value       string
	replacement string
}

func ProjectSubmissionReferences(text string, opts ProjectionOptions) Projection {
	text = strings.TrimSpace(text)
	if text == "" {
		return Projection{}
	}
	tokens := scanProjectionTokens(text, opts.WorkspaceDir, opts.SkillNames)
	if len(tokens) == 0 {
		return Projection{Text: text}
	}

	var skills []string
	var files []string
	for _, token := range tokens {
		switch token.kind {
		case KindSkill:
			skills = appendUniqueString(skills, token.value)
		case KindFile:
			files = appendUniqueString(files, token.value)
		}
	}
	if len(skills) == 0 && len(files) == 0 {
		return Projection{Text: text}
	}

	userRequest := replaceProjectionTokens(text, tokens)
	var out strings.Builder
	out.WriteString("The user referenced these resources. Treat them as explicit instructions for this turn:")
	for _, skill := range skills {
		out.WriteByte('\n')
		out.WriteString("- Load and follow the `")
		out.WriteString(skill)
		out.WriteString("` skill before taking task actions.")
	}
	for _, file := range files {
		out.WriteByte('\n')
		out.WriteString("- Read `")
		out.WriteString(file)
		out.WriteString("` before answering or editing.")
	}
	if userRequest != "" {
		out.WriteString("\n\nUser request:\n")
		out.WriteString(userRequest)
	}
	return Projection{Text: strings.TrimSpace(out.String()), Changed: true}
}

func scanProjectionTokens(text string, workspaceDir string, skillNames map[string]string) []projectionToken {
	raw := ScanSubmissionReferences(text)
	tokens := make([]projectionToken, 0, len(raw))
	for _, token := range raw {
		switch token.Kind {
		case KindSkill:
			name, ok := canonicalSkillName(token.Value, skillNames)
			if !ok {
				continue
			}
			tokens = append(tokens, projectionToken{
				kind:  KindSkill,
				start: token.Start,
				end:   token.End,
				value: name,
			})
		case KindFile:
			display, ok := workspaceFileReference(token.Value, workspaceDir)
			if !ok {
				continue
			}
			tokens = append(tokens, projectionToken{
				kind:        KindFile,
				start:       token.Start,
				end:         token.End,
				value:       display,
				replacement: "`" + display + "`",
			})
		}
	}
	return tokens
}

func canonicalSkillName(name string, skillNames map[string]string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len(skillNames) == 0 {
		return "", false
	}
	canonical := strings.TrimSpace(skillNames[strings.ToLower(name)])
	if canonical == "" {
		canonical = strings.TrimSpace(skillNames[name])
	}
	if canonical == "" {
		return "", false
	}
	return canonical, true
}

func workspaceFileReference(name string, workspaceDir string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	display := filepath.ToSlash(filepath.Clean(filepath.FromSlash(name)))
	if display == "." || display == ".." || strings.HasPrefix(display, "../") {
		return "", false
	}
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return display, fileReferenceLooksPathLike(display)
	}
	base, err := filepath.Abs(workspaceDir)
	if err != nil {
		return "", false
	}
	full := filepath.Join(base, filepath.FromSlash(display))
	full, err = filepath.Abs(full)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(base, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if _, err := os.Stat(full); err != nil {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func fileReferenceLooksPathLike(name string) bool {
	return strings.ContainsAny(name, "./\\")
}

func replaceProjectionTokens(text string, tokens []projectionToken) string {
	if len(tokens) == 0 {
		return strings.TrimSpace(text)
	}
	input := []rune(text)
	var out strings.Builder
	last := 0
	for _, token := range tokens {
		if token.start < last || token.start > len(input) || token.end > len(input) {
			continue
		}
		out.WriteString(string(input[last:token.start]))
		out.WriteString(token.replacement)
		last = token.end
	}
	out.WriteString(string(input[last:]))
	return compactProjectedSubmissionText(out.String())
}

func compactProjectedSubmissionText(text string) string {
	text = strings.TrimSpace(text)
	for strings.Contains(text, "  ") {
		text = strings.ReplaceAll(text, "  ", " ")
	}
	text = strings.ReplaceAll(text, " \n", "\n")
	text = strings.ReplaceAll(text, "\n ", "\n")
	return strings.TrimSpace(text)
}

func appendUniqueString(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}
