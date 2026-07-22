package plugin

import (
	"context"
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

func validateGitCloneURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("plugin service: git url is required")
	}
	if strings.HasPrefix(raw, "/") || filepath.IsAbs(raw) {
		return "", fmt.Errorf("plugin service: rejected local absolute git path %q", raw)
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "file://") || strings.HasPrefix(lower, "file:") {
		return "", fmt.Errorf("plugin service: rejected file:// git url")
	}
	if strings.HasPrefix(raw, "git@") {
		if !strings.Contains(raw, ":") {
			return "", fmt.Errorf("plugin service: invalid git@ url %q", raw)
		}
		return raw, nil
	}
	if strings.HasPrefix(lower, "https://") {
		return raw, nil
	}
	if strings.Contains(raw, "://") {
		return "", fmt.Errorf("plugin service: unsupported git url scheme in %q", raw)
	}
	return "", fmt.Errorf("plugin service: unsupported git url %q", raw)
}

func validateGitHubRepo(repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", fmt.Errorf("plugin service: github repo is required")
	}
	if strings.Contains(repo, "://") || strings.Contains(repo, "..") || strings.HasPrefix(repo, "/") {
		return "", fmt.Errorf("plugin service: invalid github repo %q", repo)
	}
	if !githubRepoPattern.MatchString(repo) {
		return "", fmt.Errorf("plugin service: invalid github repo %q: want owner/repo", repo)
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

func resolveMarketplaceGitURL(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "":
		return "", fmt.Errorf("plugin service: marketplace source is required")
	case strings.EqualFold(ref, "claude-plugins-official"):
		return validateGitCloneURL("https://github.com/anthropics/claude-plugins-official.git")
	case strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "http://"):
		return validateGitCloneURL(ref)
	case strings.HasPrefix(ref, "git@"):
		return validateGitCloneURL(ref)
	case strings.Count(ref, "/") == 1 && !strings.Contains(ref, " "):
		repo, err := validateGitHubRepo(ref)
		if err != nil {
			return "", err
		}
		return validateGitCloneURL("https://github.com/" + repo + ".git")
	default:
		return "", fmt.Errorf("plugin service: unsupported marketplace source %q", ref)
	}
}

func resolvePluginSourceGitURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Count(raw, "/") == 1 && !strings.Contains(raw, "://") && !strings.HasPrefix(raw, "git@") {
		return resolveGitHubCloneURL(raw)
	}
	return validateGitCloneURL(raw)
}

func cloneOrRefreshGitRepo(ctx context.Context, repoURL string, ref string, root string, expectedSHA string) error {
	validatedURL, err := validateGitCloneURL(repoURL)
	if err != nil {
		return err
	}
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return fmt.Errorf("plugin service: invalid git cache root")
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return err
	}
	args := []string{"clone", "--depth", "1", validatedURL, root}
	cmd := exec.CommandContext(ctx, "git", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	checkoutRef := strings.TrimSpace(ref)
	if checkoutRef == "" && strings.TrimSpace(expectedSHA) != "" {
		checkoutRef = strings.TrimSpace(expectedSHA)
	}
	if checkoutRef != "" {
		if err := checkoutGitRef(ctx, root, checkoutRef); err != nil {
			return err
		}
	}
	return verifyGitHEAD(ctx, root, expectedSHA)
}

func checkoutGitRef(ctx context.Context, root string, ref string) error {
	fetch := exec.CommandContext(ctx, "git", "-C", root, "fetch", "--depth", "1", "origin", ref)
	fetchOutput, fetchErr := fetch.CombinedOutput()
	if fetchErr == nil {
		checkout := exec.CommandContext(ctx, "git", "-C", root, "checkout", "--detach", "FETCH_HEAD")
		if checkoutOutput, checkoutErr := checkout.CombinedOutput(); checkoutErr != nil {
			return fmt.Errorf("git checkout FETCH_HEAD: %w\n%s", checkoutErr, strings.TrimSpace(string(checkoutOutput)))
		}
		return nil
	}
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
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("plugin service: read cloned repo head: %w", err)
	}
	got := strings.ToLower(strings.TrimSpace(string(output)))
	if got != expectedSHA {
		return fmt.Errorf("plugin service: sha mismatch after clone: got %s want %s", got, expectedSHA)
	}
	return nil
}
