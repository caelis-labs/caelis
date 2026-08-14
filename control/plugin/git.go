package plugin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var githubRepoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var fullGitSHAPattern = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)
var scpLikeGitSSHPattern = regexp.MustCompile(`^git@[A-Za-z0-9._-]+(:\d+)?:[A-Za-z0-9._/-]+$`)

type pluginInstallCacheKey struct {
	RepoURL     string
	Ref         string
	Subpath     string
	Marketplace string
	PluginName  string
}

func safePluginCacheName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = parsed.Host + parsed.Path
	}
	var result strings.Builder
	lastDash := false
	for _, character := range value {
		alphanumeric := (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9')
		if alphanumeric {
			result.WriteRune(character)
			lastDash = false
			continue
		}
		if !lastDash {
			result.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(result.String(), "-")
	if out == "" {
		return "plugin"
	}
	return out
}

func stableShortHash(material string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(material)))
	return hex.EncodeToString(sum[:])[:12]
}

func safePluginCacheSlug(value string) string {
	slug := safePluginCacheName(value)
	if slug == "" {
		return "plugin"
	}
	if len(slug) > 48 {
		slug = slug[:48]
	}
	return strings.Trim(slug, "-")
}

func pluginInstallCacheDirName(key pluginInstallCacheKey) string {
	slug := safePluginCacheSlug(firstNonEmpty(key.PluginName, "plugin"))
	material := strings.Join([]string{
		strings.TrimSpace(key.RepoURL),
		strings.TrimSpace(key.Ref),
		strings.TrimSpace(key.Subpath),
		strings.TrimSpace(key.Marketplace),
		strings.TrimSpace(key.PluginName),
	}, "|")
	return slug + "-" + stableShortHash(material)
}

func marketplaceCacheDirName(ref string) string {
	ref = strings.TrimSpace(ref)
	slug := safePluginCacheSlug(ref)
	if slug == "" {
		slug = "marketplace"
	}
	return slug + "-" + stableShortHash(ref)
}

// validateGitCloneURL enforces the production git source allowlist: HTTPS and
// authenticated SSH (scp-like git@host:path or ssh:// with an explicit user).
// Relative Host-cwd paths, absolute/file URLs, http, and git protocol are rejected.
func validateGitCloneURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("plugin service: git url is required")
	}
	if strings.HasPrefix(raw, "-") {
		return "", fmt.Errorf("plugin service: rejected leading-dash git source")
	}
	if strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) {
		return "", fmt.Errorf("plugin service: rejected local absolute git path %q", raw)
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "file://") || strings.HasPrefix(lower, "file:") {
		return "", fmt.Errorf("plugin service: rejected file:// git url")
	}
	if strings.HasPrefix(raw, "git@") {
		if !scpLikeGitSSHPattern.MatchString(raw) {
			return "", fmt.Errorf("plugin service: invalid git SSH url")
		}
		return raw, nil
	}
	if !strings.Contains(raw, "://") {
		return "", fmt.Errorf("plugin service: unsupported git source")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("plugin service: invalid git url")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("plugin service: git url must not contain a query or fragment")
	}
	scheme := strings.ToLower(strings.TrimSpace(parsed.Scheme))
	switch scheme {
	case "https":
		if strings.TrimSpace(parsed.Host) == "" {
			return "", fmt.Errorf("plugin service: git url host is required")
		}
		if parsed.User != nil {
			// HTTPS userinfo is not part of the production allowlist.
			return "", fmt.Errorf("plugin service: unsupported git url userinfo")
		}
		return raw, nil
	case "ssh":
		if strings.TrimSpace(parsed.Host) == "" {
			return "", fmt.Errorf("plugin service: git url host is required")
		}
		if strings.TrimSpace(parsed.User.Username()) == "" {
			return "", fmt.Errorf("plugin service: ssh git url requires an authenticated user")
		}
		if _, hasPassword := parsed.User.Password(); hasPassword {
			return "", fmt.Errorf("plugin service: ssh git url must not contain a password")
		}
		return raw, nil
	case "http", "git":
		return "", fmt.Errorf("plugin service: unsupported git url scheme %q", scheme)
	default:
		return "", fmt.Errorf("plugin service: unsupported git url scheme %q", scheme)
	}
}

func validateGitHubRepo(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if !githubRepoPattern.MatchString(repo) {
		return "", fmt.Errorf("invalid github repository %q", repo)
	}
	return repo, nil
}

func resolveGitHubCloneURL(repo string) (string, error) {
	repo, err := validateGitHubRepo(repo)
	if err != nil {
		return "", err
	}
	return "https://github.com/" + repo + ".git", nil
}

func resolveMarketplaceGitURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "":
		return "", fmt.Errorf("plugin service: marketplace source is required")
	case strings.EqualFold(raw, "claude-plugins-official"):
		return validateGitCloneURL("https://github.com/anthropics/claude-plugins-official.git")
	case strings.Count(raw, "/") == 1 && !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "git@") && !strings.Contains(raw, " "):
		return resolveGitHubCloneURL(raw)
	default:
		return validateGitCloneURL(raw)
	}
}

func resolvePluginSourceGitURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Count(raw, "/") == 1 && !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "git@") {
		return resolveGitHubCloneURL(raw)
	}
	return validateGitCloneURL(raw)
}

// cloneGitRepoImmutable clones a production-validated remote into a
// content-addressed directory under parentDir. Existing published roots under
// parentDir are never deleted or overwritten so active Session Runtimes keep a
// frozen filesystem view. Concurrent writers for the same parentDir are
// serialized by the process-local managed cache gate.
func cloneGitRepoImmutable(ctx context.Context, repoURL string, ref string, parentDir string, expectedSHA string) (string, error) {
	validatedURL, err := validateGitCloneURL(repoURL)
	if err != nil {
		return "", err
	}
	return cloneGitRepoImmutableFromSource(ctx, validatedURL, ref, parentDir, expectedSHA)
}

// cloneLocalGitRepoImmutable is the package-private test/fixture seam for local
// git repositories. It never relaxes the production URL allowlist; callers must
// already trust the local path.
func cloneLocalGitRepoImmutable(ctx context.Context, localRepo string, ref string, parentDir string, expectedSHA string) (string, error) {
	localRepo = strings.TrimSpace(localRepo)
	if localRepo == "" {
		return "", fmt.Errorf("plugin service: local git repo is required")
	}
	abs, err := filepath.Abs(localRepo)
	if err != nil {
		return "", fmt.Errorf("plugin service: resolve local git repo: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("plugin service: local git repo: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("plugin service: local git repo is not a directory: %s", abs)
	}
	return cloneGitRepoImmutableFromSource(ctx, abs, ref, parentDir, expectedSHA)
}

func cloneGitRepoImmutableFromSource(ctx context.Context, source string, ref string, parentDir string, expectedSHA string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("plugin service: git source is required")
	}
	if strings.HasPrefix(source, "-") {
		return "", fmt.Errorf("plugin service: rejected leading-dash git source")
	}
	parentDir = filepath.Clean(strings.TrimSpace(parentDir))
	if parentDir == "" || parentDir == "." {
		return "", fmt.Errorf("plugin service: invalid git cache parent")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	release, err := acquireManagedPluginCacheOperation(ctx, parentDir)
	if err != nil {
		return "", err
	}
	defer release()

	if err := os.MkdirAll(parentDir, 0o700); err != nil {
		return "", err
	}
	stagingParent := filepath.Join(parentDir, ".staging")
	if err := os.MkdirAll(stagingParent, 0o700); err != nil {
		return "", err
	}
	stagingID, err := randomCacheToken()
	if err != nil {
		return "", err
	}
	staging := filepath.Join(stagingParent, stagingID)
	// Staging is private to this operation and may be cleaned on failure.
	if err := os.RemoveAll(staging); err != nil {
		return "", err
	}
	// Durable external effect begins: a new managed cache tree is created.
	markEffectStarted(ctx)
	// "--" prevents source/path values from being interpreted as git options.
	args := []string{"clone", "--depth", "1", "--", source, staging}
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		_ = os.RemoveAll(staging)
		return "", fmt.Errorf("git clone: %w\n%s", err, redactGitSourceOutput(string(output), source))
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(staging)
		}
	}()

	checkoutRef := strings.TrimSpace(ref)
	if checkoutRef == "" && strings.TrimSpace(expectedSHA) != "" {
		checkoutRef = strings.TrimSpace(expectedSHA)
	}
	if checkoutRef != "" {
		if err := checkoutGitRef(ctx, staging, checkoutRef); err != nil {
			return "", err
		}
	}
	if err := verifyGitHEAD(ctx, staging, expectedSHA); err != nil {
		return "", err
	}
	headSHA, err := readGitHEAD(ctx, staging)
	if err != nil {
		return "", err
	}
	finalRoot := filepath.Join(parentDir, headSHA)
	if fi, statErr := os.Stat(finalRoot); statErr == nil && fi.IsDir() {
		// Content already published; keep the existing root and drop staging.
		// This is the only CAS-loser / content-hit cleanup performed here.
		return finalRoot, nil
	}
	if err := os.Rename(staging, finalRoot); err != nil {
		// Another concurrent publisher may have won the rename race.
		if fi, statErr := os.Stat(finalRoot); statErr == nil && fi.IsDir() {
			return finalRoot, nil
		}
		return "", err
	}
	cleanupStaging = false
	return finalRoot, nil
}

func randomCacheToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("plugin service: allocate cache token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func checkoutGitRef(ctx context.Context, root string, ref string) error {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "-") {
		return fmt.Errorf("plugin service: rejected leading-dash git ref %q", ref)
	}
	fetch := exec.CommandContext(ctx, "git", "-C", root, "fetch", "--depth", "1", "origin", "--", ref)
	fetchOutput, fetchErr := fetch.CombinedOutput()
	if fetchErr == nil {
		checkout := exec.CommandContext(ctx, "git", "-C", root, "checkout", "--detach", "FETCH_HEAD")
		if checkoutOutput, checkoutErr := checkout.CombinedOutput(); checkoutErr != nil {
			return fmt.Errorf("git checkout FETCH_HEAD: %w\n%s", checkoutErr, strings.TrimSpace(string(checkoutOutput)))
		}
		return nil
	}
	// Ref is validated against leading dashes; do not use "--" here because
	// git checkout treats operands after "--" as pathspecs rather than refs.
	checkout := exec.CommandContext(ctx, "git", "-C", root, "checkout", ref)
	if checkoutOutput, checkoutErr := checkout.CombinedOutput(); checkoutErr != nil {
		return fmt.Errorf("git fetch %s: %w\n%s\ngit checkout %s: %w\n%s", ref, fetchErr, strings.TrimSpace(string(fetchOutput)), ref, checkoutErr, strings.TrimSpace(string(checkoutOutput)))
	}
	return nil
}

func verifyGitHEAD(ctx context.Context, root string, expectedSHA string) error {
	expectedSHA = strings.ToLower(strings.TrimSpace(expectedSHA))
	if expectedSHA == "" {
		return nil
	}
	if !fullGitSHAPattern.MatchString(expectedSHA) {
		return fmt.Errorf("plugin service: sha must be a full 40-character git commit SHA")
	}
	got, err := readGitHEAD(ctx, root)
	if err != nil {
		return err
	}
	if got != expectedSHA {
		return fmt.Errorf("plugin service: sha mismatch after clone: got %s want %s", got, expectedSHA)
	}
	return nil
}

func readGitHEAD(ctx context.Context, root string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("plugin service: read cloned repo head: %w", err)
	}
	got := strings.ToLower(strings.TrimSpace(string(output)))
	if !fullGitSHAPattern.MatchString(got) {
		return "", fmt.Errorf("plugin service: unexpected git HEAD %q", got)
	}
	return got, nil
}

func redactGitSourceOutput(output, source string) string {
	output = strings.ReplaceAll(output, strings.TrimSpace(source), "<redacted git source>")
	return strings.TrimSpace(output)
}
