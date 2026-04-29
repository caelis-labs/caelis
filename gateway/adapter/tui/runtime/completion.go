package runtime

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

const (
	defaultCompletionLimit  = 8
	maxCompletionLimit      = 50
	fileCompletionMaxDepth  = 5
	fileCompletionTimeout   = 150 * time.Millisecond
	skillCompletionTimeout  = 120 * time.Millisecond
	resumeCompletionTimeout = 250 * time.Millisecond
)

var errCompletionStopped = errors.New("tui/runtime: completion stopped")

var ignoredCompletionDirs = map[string]struct{}{
	".git":         {},
	".hg":          {},
	".svn":         {},
	".next":        {},
	".turbo":       {},
	".cache":       {},
	".direnv":      {},
	".idea":        {},
	"node_modules": {},
	"vendor":       {},
	"dist":         {},
	"build":        {},
	"target":       {},
	"coverage":     {},
}

type scoredCompletion struct {
	candidate CompletionCandidate
	score     int
}

func normalizeCompletionLimit(limit int) int {
	if limit <= 0 {
		limit = defaultCompletionLimit
	}
	if limit > maxCompletionLimit {
		limit = maxCompletionLimit
	}
	return limit
}

func completionContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= timeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func trimCompletionQuery(query string) string {
	query = strings.TrimSpace(query)
	query = strings.TrimPrefix(query, "./")
	query = filepath.ToSlash(query)
	if query == "." {
		return ""
	}
	return strings.TrimSpace(query)
}

func walkRootForQuery(root string, query string) (string, string, bool) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" {
		return "", "", false
	}
	query = strings.Trim(strings.ReplaceAll(trimCompletionQuery(query), "\\", "/"), "/")
	if query == "" {
		return root, "", true
	}
	dirPart, filePart := pathSplitForQuery(query)
	base := root
	if dirPart != "" {
		base = filepath.Join(root, filepath.FromSlash(dirPart))
	}
	relBase, err := filepath.Rel(root, base)
	if err != nil || strings.HasPrefix(relBase, "..") {
		return "", "", false
	}
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		if dirPart == "" {
			return root, query, true
		}
		return root, query, true
	}
	return base, filePart, true
}

func pathSplitForQuery(query string) (string, string) {
	idx := strings.LastIndex(query, "/")
	if idx < 0 {
		return "", query
	}
	return strings.Trim(query[:idx], "/"), strings.Trim(query[idx+1:], "/")
}

func shouldIgnoreCompletionDir(name string) bool {
	_, ok := ignoredCompletionDirs[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func relativeDisplayPath(root string, path string, dir bool) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = path
	}
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "." {
		rel = ""
	}
	if dir && rel != "" && !strings.HasSuffix(rel, "/") {
		rel += "/"
	}
	return rel
}

func displayPathHint(workspace string, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if workspace != "" {
		if rel, err := filepath.Rel(workspace, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		home = filepath.Clean(home)
		clean := filepath.Clean(path)
		if clean == home {
			return "~"
		}
		prefix := home + string(filepath.Separator)
		if strings.HasPrefix(clean, prefix) {
			return "~/" + filepath.ToSlash(strings.TrimPrefix(clean, prefix))
		}
	}
	return filepath.ToSlash(path)
}

func fuzzyMatchScore(query string, values ...string) (int, bool) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return 0, true
	}
	best := 1 << 30
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		switch {
		case strings.HasPrefix(value, query):
			if 0 < best {
				best = 0
			}
		case strings.Contains(value, query):
			score := 10 + strings.Index(value, query)
			if score < best {
				best = score
			}
		default:
			if gap, ok := fuzzySubsequenceGap(value, query); ok {
				score := 100 + gap
				if score < best {
					best = score
				}
			}
		}
	}
	if best == 1<<30 {
		return 0, false
	}
	return best, true
}

func fuzzySubsequenceGap(value string, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	pos := 0
	gap := 0
	for _, needle := range query {
		found := false
		for pos < len(value) {
			ch := rune(value[pos])
			if ch == needle {
				found = true
				pos++
				break
			}
			gap++
			pos++
		}
		if !found {
			return 0, false
		}
	}
	return gap, true
}

func sortAndTrimCandidates(items []scoredCompletion, limit int) []CompletionCandidate {
	sort.SliceStable(items, func(i, j int) bool {
		left := items[i]
		right := items[j]
		switch {
		case left.score != right.score:
			return left.score < right.score
		case len(left.candidate.Display) != len(right.candidate.Display):
			return len(left.candidate.Display) < len(right.candidate.Display)
		default:
			return strings.ToLower(left.candidate.Display) < strings.ToLower(right.candidate.Display)
		}
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]CompletionCandidate, 0, len(items))
	for _, item := range items {
		out = append(out, item.candidate)
	}
	return out
}

func enrichResumeCandidate(ctx context.Context, stack *gatewayapp.Stack, summary sdksession.SessionSummary) ResumeCandidate {
	candidate := ResumeCandidate{
		SessionID: summary.SessionID,
		Title:     strings.TrimSpace(summary.Title),
		Prompt:    strings.TrimSpace(summary.Title),
		Workspace: strings.TrimSpace(summary.CWD),
		Age:       humanAge(summary.UpdatedAt),
		UpdatedAt: summary.UpdatedAt,
	}
	if stack == nil || stack.Sessions == nil {
		return candidate
	}
	loaded, err := stack.Sessions.LoadSession(ctx, sdksession.LoadSessionRequest{
		SessionRef:       summary.SessionRef,
		Limit:            0,
		IncludeTransient: false,
	})
	if err != nil {
		return candidate
	}
	candidate.Title = firstNonEmpty(strings.TrimSpace(loaded.Session.Title), candidate.Title)
	candidate.Prompt = firstNonEmpty(strings.TrimSpace(loaded.Session.Title), candidate.Prompt)
	candidate.Workspace = firstNonEmpty(strings.TrimSpace(loaded.Session.CWD), candidate.Workspace)
	candidate.Model = strings.TrimSpace(appgateway.CurrentModelAlias(loaded.State))
	return candidate
}

func scoreResumeCandidate(query string, candidate ResumeCandidate) (int, bool) {
	return fuzzyMatchScore(query,
		candidate.SessionID,
		candidate.Title,
		candidate.Prompt,
		candidate.Model,
		candidate.Workspace,
	)
}

func scoreSkillMeta(query string, meta gatewayapp.SkillMeta, workspace string) (int, bool) {
	return fuzzyMatchScore(query,
		meta.Name,
		meta.Description,
		displayPathHint(workspace, meta.Path),
	)
}

func completeWorkspaceFiles(ctx context.Context, root string, query string, limit int) ([]CompletionCandidate, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return []CompletionCandidate{}, nil
	}
	limit = normalizeCompletionLimit(limit)
	ctx, cancel := completionContext(ctx, fileCompletionTimeout)
	defer cancel()

	base, needle, ok := walkRootForQuery(root, query)
	if !ok {
		return []CompletionCandidate{}, nil
	}
	entries := make([]scoredCompletion, 0, limit*2)
	seen := map[string]struct{}{}
	baseDepth := strings.Count(filepath.Clean(base), string(filepath.Separator))

	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if ctx.Err() != nil {
			return errCompletionStopped
		}
		if path == base {
			return nil
		}
		name := strings.TrimSpace(entry.Name())
		if entry.IsDir() && shouldIgnoreCompletionDir(name) {
			return filepath.SkipDir
		}
		depth := strings.Count(filepath.Clean(path), string(filepath.Separator)) - baseDepth
		if depth > fileCompletionMaxDepth {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		display := relativeDisplayPath(root, path, entry.IsDir())
		if display == "" {
			return nil
		}
		score, match := fuzzyMatchScore(needle, name, display)
		if !match {
			if entry.IsDir() {
				return nil
			}
			return nil
		}
		value := display
		key := strings.ToLower(value)
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		detail := "file"
		if entry.IsDir() {
			detail = "directory"
		}
		entries = append(entries, scoredCompletion{
			candidate: CompletionCandidate{
				Value:   value,
				Display: value,
				Detail:  detail,
				Path:    filepath.Clean(path),
			},
			score: score + depth,
		})
		if len(entries) >= limit*4 {
			return errCompletionStopped
		}
		return nil
	})
	if err != nil && !errors.Is(err, errCompletionStopped) {
		return nil, err
	}
	return sortAndTrimCandidates(entries, limit), nil
}
