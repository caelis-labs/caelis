package appserveradapter

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

const imageMIMESampleBytes = 512

func contentPartsFromSubmission(input string, items []controlprompt.Attachment, workspace string) ([]model.ContentPart, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]model.ContentPart, 0, len(items)*2+1)
	totalImageBytes := 0
	err := walkSubmissionAttachments(input, items, func(text string) error {
		out = append(out, model.ContentPart{Type: model.ContentPartText, Text: text})
		return nil
	}, func(_ int, item controlprompt.Attachment) error {
		part, imageBytes, err := imageContentPartFromAttachment(item, workspace)
		if err != nil {
			return err
		}
		if err := addPromptImageBytes(&totalImageBytes, imageBytes); err != nil {
			return err
		}
		out = append(out, part)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func displayInputWithAttachments(input string, items []controlprompt.Attachment) string {
	input = strings.TrimSpace(input)
	if len(items) == 0 {
		return input
	}
	var out displayInputBuilder
	_ = walkSubmissionAttachments(input, items, func(text string) error {
		out.append(text)
		return nil
	}, func(index int, _ controlprompt.Attachment) error {
		out.append(fmt.Sprintf("[image #%d]", index))
		return nil
	})
	return out.String()
}

func walkSubmissionAttachments(input string, items []controlprompt.Attachment, text func(string) error, attachment func(int, controlprompt.Attachment) error) error {
	input = strings.TrimSpace(input)
	inputRunes := []rune(input)
	items = cloneAndSortAttachments(items, len(inputRunes))
	textPos := 0
	for i, item := range items {
		offset := item.Offset
		if offset < textPos {
			offset = textPos
		}
		if offset > len(inputRunes) {
			offset = len(inputRunes)
		}
		if offset > textPos {
			if text != nil {
				if err := text(string(inputRunes[textPos:offset])); err != nil {
					return err
				}
			}
			textPos = offset
		}
		if attachment != nil {
			if err := attachment(i+1, item); err != nil {
				return err
			}
		}
	}
	if textPos < len(inputRunes) {
		if text != nil {
			if err := text(string(inputRunes[textPos:])); err != nil {
				return err
			}
		}
	}
	return nil
}

type displayInputBuilder struct {
	out     strings.Builder
	last    rune
	hasLast bool
}

func (b *displayInputBuilder) append(segment string) {
	if b == nil {
		return
	}
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return
	}
	if b.hasLast {
		first, _ := firstDisplayInputRune(segment)
		if !unicode.IsSpace(b.last) && !unicode.IsSpace(first) {
			b.out.WriteByte(' ')
		}
	}
	b.out.WriteString(segment)
	if last, ok := lastDisplayInputRune(segment); ok {
		b.last = last
		b.hasLast = true
	}
}

func (b *displayInputBuilder) String() string {
	if b == nil {
		return ""
	}
	return strings.TrimSpace(b.out.String())
}

func firstDisplayInputRune(s string) (rune, bool) {
	for _, r := range s {
		return r, true
	}
	return 0, false
}

func lastDisplayInputRune(s string) (rune, bool) {
	var out rune
	ok := false
	for _, r := range s {
		out = r
		ok = true
	}
	return out, ok
}

func imageContentPartFromAttachment(item controlprompt.Attachment, workspace string) (model.ContentPart, int, error) {
	if part, imageBytes, ok, err := imageContentPartFromInlineAttachment(item); ok || err != nil {
		return part, imageBytes, err
	}
	raw := strings.TrimSpace(item.Name)
	if raw == "" {
		return model.ContentPart{}, 0, fmt.Errorf("image attachment path is empty")
	}
	path, err := resolveAttachmentPath(raw, workspace)
	if err != nil {
		return model.ContentPart{}, 0, err
	}
	file, err := os.Open(path)
	if err != nil {
		return model.ContentPart{}, 0, fmt.Errorf("read image attachment %q: %w", raw, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return model.ContentPart{}, 0, fmt.Errorf("stat image attachment %q: %w", raw, err)
	}
	if info.Size() > appserver.MaxPromptImageBytes {
		return model.ContentPart{}, 0, fmt.Errorf("image attachment %q is too large (%d bytes, limit %d)", raw, info.Size(), appserver.MaxPromptImageBytes)
	}
	mimeType, data, imageBytes, err := readAndEncodeImageAttachment(file, raw, info.Size())
	if err != nil {
		return model.ContentPart{}, 0, err
	}
	return model.ContentPart{
		Type:     model.ContentPartImage,
		MimeType: mimeType,
		Data:     data,
		FileName: filepath.Base(path),
	}, imageBytes, nil
}

// readAndEncodeImageAttachment detects and encodes one forward-only byte
// sequence. The detected prefix is replayed into the encoder; re-seeking would
// allow an in-place rewrite to replace that trusted prefix before encoding.
func readAndEncodeImageAttachment(r io.Reader, raw string, statSize int64) (string, string, int, error) {
	sample, err := readImageMIMESample(r)
	if err != nil {
		return "", "", 0, fmt.Errorf("read image attachment %q: %w", raw, err)
	}
	if len(sample) == 0 {
		return "", "", 0, fmt.Errorf("image attachment %q is empty", raw)
	}
	mimeType, ok := detectSupportedImageMimeType(sample)
	if !ok {
		return "", "", 0, fmt.Errorf("attachment %q is not a supported image (detected %s)", raw, imageMimeType(sample))
	}
	data, imageBytes, err := encodeImageAttachmentBase64(
		io.MultiReader(bytes.NewReader(sample), r),
		raw,
		statSize,
	)
	if err != nil {
		return "", "", 0, err
	}
	return mimeType, data, imageBytes, nil
}

func readImageMIMESample(r io.Reader) ([]byte, error) {
	var buf [imageMIMESampleBytes]byte
	n, err := io.ReadFull(r, buf[:])
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		err = nil
	}
	return buf[:n], err
}

// encodeImageAttachmentBase64 streams file bytes into standard base64 without
// retaining the raw image. LimitReader stops one byte past the per-image cap so
// growth after Stat cannot admit an oversized payload; the copy count is the
// admitted size when the file shrinks or grows under the cap.
func encodeImageAttachmentBase64(r io.Reader, raw string, statSize int64) (string, int, error) {
	var encoded strings.Builder
	if statSize > 0 {
		growTo := statSize
		if growTo > appserver.MaxPromptImageBytes {
			growTo = appserver.MaxPromptImageBytes
		}
		encoded.Grow(base64.StdEncoding.EncodedLen(int(growTo)))
	}
	encoder := base64.NewEncoder(base64.StdEncoding, &encoded)
	var buf [32 * 1024]byte
	written, err := io.CopyBuffer(encoder, io.LimitReader(r, int64(appserver.MaxPromptImageBytes)+1), buf[:])
	closeErr := encoder.Close()
	if err != nil {
		return "", 0, fmt.Errorf("read image attachment %q: %w", raw, err)
	}
	if closeErr != nil {
		return "", 0, fmt.Errorf("read image attachment %q: %w", raw, closeErr)
	}
	if written == 0 {
		return "", 0, fmt.Errorf("image attachment %q is empty", raw)
	}
	if written > appserver.MaxPromptImageBytes {
		return "", 0, fmt.Errorf("image attachment %q is too large (%d bytes, limit %d)", raw, written, appserver.MaxPromptImageBytes)
	}
	return encoded.String(), int(written), nil
}

func imageContentPartFromInlineAttachment(item controlprompt.Attachment) (model.ContentPart, int, bool, error) {
	data := strings.TrimSpace(item.Data)
	if data == "" {
		return model.ContentPart{}, 0, false, nil
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return model.ContentPart{}, 0, true, fmt.Errorf("decode inline image attachment %q: %w", strings.TrimSpace(item.Name), err)
	}
	if len(raw) == 0 {
		return model.ContentPart{}, 0, true, fmt.Errorf("inline image attachment %q is empty", strings.TrimSpace(item.Name))
	}
	if len(raw) > appserver.MaxPromptImageBytes {
		return model.ContentPart{}, 0, true, fmt.Errorf("inline image attachment %q is too large (%d bytes, limit %d)", strings.TrimSpace(item.Name), len(raw), appserver.MaxPromptImageBytes)
	}
	mimeType, ok := detectSupportedImageMimeType(raw)
	if !ok {
		return model.ContentPart{}, 0, true, fmt.Errorf("inline attachment %q is not a supported image (detected %s)", strings.TrimSpace(item.Name), imageMimeType(raw))
	}
	return model.ContentPart{
		Type:     model.ContentPartImage,
		MimeType: mimeType,
		Data:     data,
		FileName: strings.TrimSpace(item.Name),
	}, len(raw), true, nil
}

func cloneAndSortAttachments(items []controlprompt.Attachment, textLen int) []controlprompt.Attachment {
	if len(items) == 0 {
		return nil
	}
	out := make([]controlprompt.Attachment, 0, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		data := strings.TrimSpace(item.Data)
		if name == "" && data == "" {
			continue
		}
		offset := item.Offset
		if offset < 0 {
			offset = 0
		}
		if offset > textLen {
			offset = textLen
		}
		out = append(out, controlprompt.Attachment{
			Name:     name,
			Offset:   offset,
			MimeType: strings.TrimSpace(item.MimeType),
			Data:     data,
		})
	}
	if len(out) <= 1 {
		return out
	}
	slices.SortStableFunc(out, func(left controlprompt.Attachment, right controlprompt.Attachment) int {
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

func contentPartsFromAttachments(items []controlprompt.Attachment, workspace string) ([]model.ContentPart, error) {
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]model.ContentPart, 0, len(items))
	totalImageBytes := 0
	for _, item := range cloneAndSortAttachments(items, 0) {
		part, imageBytes, err := imageContentPartFromAttachment(item, workspace)
		if err != nil {
			return nil, err
		}
		if err := addPromptImageBytes(&totalImageBytes, imageBytes); err != nil {
			return nil, err
		}
		out = append(out, part)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func addPromptImageBytes(total *int, imageBytes int) error {
	if total == nil || *total < 0 || *total > appserver.MaxPromptImageTotalBytes || imageBytes < 0 || imageBytes > appserver.MaxPromptImageTotalBytes-*total {
		return fmt.Errorf("image attachments exceed the aggregate limit of %d bytes", appserver.MaxPromptImageTotalBytes)
	}
	*total += imageBytes
	return nil
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
