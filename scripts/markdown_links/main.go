// Command markdown_links validates repository-local links in maintained docs.
package main

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var markdownLink = regexp.MustCompile(`!?\[[^]]*\]\((<[^>]+>|[^[:space:])]+)(?:[[:space:]]+"[^"]*")?\)`)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fatal(err)
	}
	paths := os.Args[1:]
	if len(paths) == 0 {
		paths, err = documentationPaths(root)
		if err != nil {
			fatal(err)
		}
	}
	problems := checkPaths(root, paths)
	for _, problem := range problems {
		fmt.Fprintln(os.Stderr, problem)
	}
	if len(problems) != 0 {
		os.Exit(1)
	}
	fmt.Printf("documentation links passed (%d files checked)\n", len(paths))
}

func documentationPaths(root string) ([]string, error) {
	paths := []string{"README.md", "agent-sdk/README.md"}
	err := filepath.WalkDir(filepath.Join(root, "docs"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, relative)
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func checkPaths(root string, paths []string) []string {
	var problems []string
	for _, relative := range paths {
		path := relative
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		file, err := os.Open(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", relative, err))
			continue
		}
		scanner := bufio.NewScanner(file)
		inFence := false
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				inFence = !inFence
				continue
			}
			if inFence {
				continue
			}
			for _, match := range markdownLink.FindAllStringSubmatch(line, -1) {
				target := strings.Trim(match[1], "<>")
				if !isLocalTarget(target) {
					continue
				}
				target = strings.SplitN(target, "#", 2)[0]
				target = strings.SplitN(target, "?", 2)[0]
				if target == "" {
					continue
				}
				decoded, err := url.PathUnescape(target)
				if err != nil {
					problems = append(problems, fmt.Sprintf("%s:%d: invalid link %q: %v", relative, lineNumber, target, err))
					continue
				}
				var destination string
				if strings.HasPrefix(decoded, "/") {
					destination = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(decoded, "/")))
				} else {
					destination = filepath.Join(filepath.Dir(path), filepath.FromSlash(decoded))
				}
				if _, err := os.Stat(destination); err != nil {
					problems = append(problems, fmt.Sprintf("%s:%d: link %q: %v", relative, lineNumber, target, err))
				}
			}
		}
		if err := scanner.Err(); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", relative, err))
		}
		if err := file.Close(); err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", relative, err))
		}
	}
	return problems
}

func isLocalTarget(target string) bool {
	if target == "" || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "//") {
		return false
	}
	lower := strings.ToLower(target)
	for _, prefix := range []string{"http://", "https://", "mailto:", "tel:", "data:"} {
		if strings.HasPrefix(lower, prefix) {
			return false
		}
	}
	return true
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
