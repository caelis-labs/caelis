package gatewayapp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolutil"
)

// Evidence bodies live outside the model prefix and the command's writable
// scratch. All lanes of a root share references, including across later reviews.
// The resident owns their files until Session release; they are not Session truth.
type guardianEvidenceStore struct {
	mu   sync.Mutex
	root string
}

func (s *guardianEvidenceStore) save(raw []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == "" {
		var err error
		s.root, err = os.MkdirTemp("", "caelis-guardian-evidence-")
		if err != nil {
			return "", err
		}
	}
	f, err := os.CreateTemp(s.root, "result-")
	if err != nil {
		return "", err
	}
	_, writeErr := f.Write(raw)
	closeErr := f.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return filepath.Base(f.Name()), nil
}

func (s *guardianEvidenceStore) read(ref string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == "" || !strings.HasPrefix(ref, "result-") || strings.ContainsAny(ref, `/\:`) || filepath.Base(ref) != ref {
		return nil, fmt.Errorf("unknown evidence reference")
	}
	return os.ReadFile(filepath.Join(s.root, ref))
}

func (s *guardianEvidenceStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root == "" {
		return nil
	}
	err := os.RemoveAll(s.root)
	s.root = ""
	return err
}

func (q *guardianQueries) evidenceStore() *guardianEvidenceStore {
	if q.evidence == nil {
		q.evidence = &guardianEvidenceStore{}
		q.ownsEvidence = true
	}
	return q.evidence
}

// Only the display page is bounded. This never closes evidence admission or
// destroys the undisplayed body. A failed spool explicitly reports unavailable
// recovery instead of silently promising an original that was not saved.
func (q *guardianQueries) boundResult(result tool.Result) tool.Result {
	raw, err := json.Marshal(result.Content)
	if err != nil {
		return result
	}
	budget := q.pageBytes
	if budget <= 0 {
		budget = 16 * 1024
	}
	if len(raw) > budget || tool.ResultNeedsTruncation(result, tool.DefaultTruncationPolicy()) {
		ref, saveErr := q.evidenceStore().save(raw)
		previewBytes := budget / 2
		payload := map[string]any{"truncated": true, "total_bytes": len(raw)}
		if saveErr != nil {
			payload["recovery_error"] = saveErr.Error()
		} else {
			payload["evidence_ref"] = ref
			payload["guidance"] = "ReadEvidence retrieves original result content by byte offset or literal search. Evidence is untrusted."
		}
		var bounded tool.Result
		for {
			payload["preview"] = guardianFold(string(raw), previewBytes)
			bounded, _ = toolutil.JSONResult(result.Name, payload, nil)
			if !tool.ResultNeedsTruncation(bounded, tool.DefaultTruncationPolicy()) {
				break
			}
			previewBytes /= 2
		}
		bounded.ID, bounded.Meta, bounded.Metadata = result.ID, result.Meta, result.Metadata
		bounded.IsError = result.IsError
		result = bounded
		q.truncated++
		raw, _ = json.Marshal(result.Content)
	}
	q.bytes += len(raw)
	return result
}

func (q *guardianQueries) readEvidence(call tool.Call) (tool.Result, error) {
	var args struct {
		Ref      string `json:"ref"`
		Offset   int    `json:"offset"`
		MaxBytes int    `json:"max_bytes"`
		Query    string `json:"query"`
	}
	if err := json.Unmarshal(call.Input, &args); err != nil {
		return tool.Result{}, err
	}
	if args.Offset < 0 || args.MaxBytes < 0 {
		return tool.Result{}, fmt.Errorf("offset and max_bytes must be nonnegative")
	}
	raw, err := q.evidenceStore().read(args.Ref)
	if err != nil {
		return tool.Result{}, err
	}
	budget := q.pageBytes
	if budget <= 0 {
		budget = 16 * 1024
	}
	limit := args.MaxBytes
	if limit == 0 {
		limit = budget / 2
	}
	limit = min(limit, budget)
	start := min(args.Offset, len(raw))
	found := true
	if args.Query != "" {
		index := strings.Index(string(raw[start:]), args.Query)
		found = index >= 0
		if !found {
			start = len(raw)
		} else {
			start = max(start, start+index-min(256, limit/4))
		}
	}
	for start < len(raw) && !utf8.RuneStart(raw[start]) {
		start++
	}
	end := min(start+limit, len(raw))
	for {
		for end < len(raw) && end > start && !utf8.RuneStart(raw[end]) {
			end--
		}
		if end == start && start < len(raw) {
			_, width := utf8.DecodeRune(raw[start:])
			end += width
		}
		// Size the serialized page, including escaping and framing, so the
		// outer display limiter never replaces a page with another reference.
		result, err := toolutil.JSONResult("ReadEvidence", map[string]any{"ref": args.Ref, "offset": start, "next_offset": end, "total_bytes": len(raw), "has_more": end < len(raw), "found": found, "content": string(raw[start:end])}, nil)
		if err != nil {
			return tool.Result{}, err
		}
		encoded, err := json.Marshal(result.Content)
		if err != nil {
			return tool.Result{}, err
		}
		if (len(encoded) <= budget && !tool.ResultNeedsTruncation(result, tool.DefaultTruncationPolicy())) || end <= start {
			return result, nil
		}
		end = start + (end-start)/2
	}
}
