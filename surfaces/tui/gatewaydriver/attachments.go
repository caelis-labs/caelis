package gatewaydriver

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
)

const maxAttachmentImageBytes = 20_000_000

func contentPartsFromSubmission(input string, items []Attachment, workspace string) ([]model.ContentPart, error) {
	if len(items) == 0 {
		return nil, nil
	}
	input = strings.TrimSpace(input)
	inputRunes := []rune(input)
	items = cloneAndSortAttachments(items, len(inputRunes))
	out := make([]model.ContentPart, 0, len(items)*2+1)
	textPos := 0
	for _, item := range items {
		offset := item.Offset
		if offset < textPos {
			offset = textPos
		}
		if offset > len(inputRunes) {
			offset = len(inputRunes)
		}
		if offset > textPos {
			out = append(out, model.ContentPart{Type: model.ContentPartText, Text: string(inputRunes[textPos:offset])})
			textPos = offset
		}
		part, err := imageContentPartFromAttachment(item, workspace)
		if err != nil {
			return nil, err
		}
		out = append(out, part)
	}
	if textPos < len(inputRunes) {
		out = append(out, model.ContentPart{Type: model.ContentPartText, Text: string(inputRunes[textPos:])})
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func imageContentPartFromAttachment(item Attachment, workspace string) (model.ContentPart, error) {
	raw := strings.TrimSpace(item.Name)
	if raw == "" {
		return model.ContentPart{}, fmt.Errorf("image attachment path is empty")
	}
	path, err := resolveAttachmentPath(raw, workspace)
	if err != nil {
		return model.ContentPart{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return model.ContentPart{}, fmt.Errorf("stat image attachment %q: %w", raw, err)
	}
	if info.Size() > maxAttachmentImageBytes {
		return model.ContentPart{}, fmt.Errorf("image attachment %q is too large (%d bytes, limit %d)", raw, info.Size(), maxAttachmentImageBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return model.ContentPart{}, fmt.Errorf("read image attachment %q: %w", raw, err)
	}
	if len(data) == 0 {
		return model.ContentPart{}, fmt.Errorf("image attachment %q is empty", raw)
	}
	mimeType, ok := detectSupportedImageMimeType(data)
	if !ok {
		return model.ContentPart{}, fmt.Errorf("attachment %q is not a supported image (detected %s)", raw, imageMimeType(data))
	}
	return model.ContentPart{
		Type:     model.ContentPartImage,
		MimeType: mimeType,
		Data:     base64.StdEncoding.EncodeToString(data),
		FileName: filepath.Base(path),
	}, nil
}

func cloneAndSortAttachments(items []Attachment, textLen int) []Attachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]Attachment, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		offset := item.Offset
		if offset < 0 {
			offset = 0
		}
		if offset > textLen {
			offset = textLen
		}
		out = append(out, Attachment{Name: name, Offset: offset})
	}
	if len(out) <= 1 {
		return out
	}
	slices.SortStableFunc(out, func(left Attachment, right Attachment) int {
		switch {
		case left.Offset < right.Offset:
			return -1
		case left.Offset > right.Offset:
			return 1
		default:
			return 0
		}
	})
	return out
}

func contentPartsFromAttachments(items []Attachment, workspace string) ([]model.ContentPart, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]model.ContentPart, 0, len(items))
	for _, item := range cloneAndSortAttachments(items, 0) {
		part, err := imageContentPartFromAttachment(item, workspace)
		if err != nil {
			return nil, err
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func resolveAttachmentPath(raw string, workspace string) (string, error) {
	raw = strings.TrimSpace(strings.Trim(raw, `"'`))
	if raw == "" {
		return "", fmt.Errorf("image attachment path is empty")
	}
	if parsed, err := url.Parse(raw); err == nil && strings.EqualFold(parsed.Scheme, "file") {
		if path, err := url.PathUnescape(parsed.Path); err == nil && path != "" {
			if len(path) >= 3 && path[0] == '/' && path[2] == ':' {
				path = path[1:]
			}
			if parsed.Host != "" {
				path = `\\` + parsed.Host + path
			}
			raw = path
		}
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	workspace = strings.TrimSpace(workspace)
	if workspace != "" {
		return filepath.Clean(filepath.Join(workspace, raw)), nil
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func detectSupportedImageMimeType(data []byte) (string, bool) {
	switch {
	case hasPrefixBytes(data, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}):
		return "image/png", true
	case len(data) >= 3 && data[0] == 0xff && data[1] == 0xd8 && data[2] == 0xff:
		return "image/jpeg", true
	case hasPrefixBytes(data, []byte("GIF87a")) || hasPrefixBytes(data, []byte("GIF89a")):
		return "image/gif", true
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp", true
	default:
		return "", false
	}
}

func hasPrefixBytes(data []byte, prefix []byte) bool {
	if len(data) < len(prefix) {
		return false
	}
	for i := range prefix {
		if data[i] != prefix[i] {
			return false
		}
	}
	return true
}

func imageMimeType(data []byte) string {
	return http.DetectContentType(data)
}
