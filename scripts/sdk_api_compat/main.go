package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

const defaultWaiverPath = "agent-sdk/api-compat-waivers.json"

type declaration struct {
	Package     string `json:"package"`
	Declaration string `json:"declaration"`
	SHA256      string `json:"sha256"`
}

type removalWaiver struct {
	Package string `json:"package"`
	SHA256  string `json:"sha256"`
	Symbol  string `json:"symbol"`
	Reason  string `json:"reason"`
}

type waiverConfig struct {
	BaselineTag     string          `json:"baseline_tag"`
	CurrentSnapshot string          `json:"current_snapshot"`
	Removals        []removalWaiver `json:"removals"`
}

func main() {
	report := flag.Bool("report", false, "print declarations removed since the baseline tag")
	printBaseline := flag.Bool("print-baseline", false, "print the resolved rolling baseline tag")
	waiverPath := flag.String("waivers", defaultWaiverPath, "compatibility waiver configuration")
	flag.Parse()

	config, err := readWaiverConfig(*waiverPath)
	if err != nil {
		fatalf("read waivers: %v", err)
	}
	baselineTag, err := resolveBaselineTag(config.BaselineTag)
	if err != nil {
		fatalf("resolve rolling baseline: %v", err)
	}
	if *printBaseline {
		fmt.Println(baselineTag)
		return
	}
	baselineRaw, err := gitShow(baselineTag, "agent-sdk/api.txt")
	if err != nil {
		fatalf("read baseline %s: %v", baselineTag, err)
	}
	currentRaw, err := os.ReadFile(config.CurrentSnapshot)
	if err != nil {
		fatalf("read current snapshot: %v", err)
	}
	baseline, err := parseSnapshot(baselineRaw)
	if err != nil {
		fatalf("parse baseline snapshot: %v", err)
	}
	current, err := parseSnapshot(currentRaw)
	if err != nil {
		fatalf("parse current snapshot: %v", err)
	}
	removed := removedDeclarations(baseline, current)
	if *report {
		encoded, err := json.MarshalIndent(removed, "", "  ")
		if err != nil {
			fatalf("encode report: %v", err)
		}
		fmt.Println(string(encoded))
		return
	}
	if err := validateCompatibility(removed, config.Removals); err != nil {
		fatalf("API compatibility against %s failed:\n%v", baselineTag, err)
	}
	fmt.Printf("sdk-api-compat: passed against %s (%d reviewed removals)\n", baselineTag, len(removed))
}

func readWaiverConfig(path string) (waiverConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return waiverConfig{}, err
	}
	var config waiverConfig
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return waiverConfig{}, err
	}
	config.BaselineTag = strings.TrimSpace(config.BaselineTag)
	config.CurrentSnapshot = strings.TrimSpace(config.CurrentSnapshot)
	if config.BaselineTag == "" {
		config.BaselineTag = "auto"
	}
	if config.CurrentSnapshot == "" {
		config.CurrentSnapshot = "agent-sdk/api.txt"
	}
	return config, nil
}

func resolveBaselineTag(configured string) (string, error) {
	if override := strings.TrimSpace(os.Getenv("SDK_API_BASELINE_TAG")); override != "" {
		return override, nil
	}
	configured = strings.TrimSpace(configured)
	if configured != "" && configured != "auto" {
		return configured, nil
	}
	exactRaw, _ := exec.Command("git", "describe", "--tags", "--exact-match", "--match", "v[0-9]*").Output()
	exact := strings.TrimSpace(string(exactRaw))
	tagsRaw, err := exec.Command("git", "tag", "--merged", "HEAD", "--sort=-v:refname", "--list", "v[0-9]*").Output()
	if err != nil {
		return "", err
	}
	return selectBaselineTag(strings.Fields(string(tagsRaw)), exact)
}

func selectBaselineTag(tags []string, exact string) (string, error) {
	exact = strings.TrimSpace(exact)
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || tag == exact {
			continue
		}
		return tag, nil
	}
	return "", fmt.Errorf("no previous semantic release tag is reachable from HEAD")
}

func gitShow(tag, path string) ([]byte, error) {
	ref := "refs/tags/" + strings.TrimSpace(tag) + "^{commit}"
	if err := exec.Command("git", "rev-parse", "--verify", ref).Run(); err != nil {
		return nil, fmt.Errorf("verify tag: %w", err)
	}
	out, err := exec.Command("git", "show", strings.TrimSpace(tag)+":"+path).Output()
	if err != nil {
		return nil, err
	}
	return out, nil
}

func parseSnapshot(raw []byte) ([]declaration, error) {
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var (
		packageName string
		lines       []string
		out         []declaration
	)
	flush := func() error {
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) == 0 {
			return nil
		}
		if packageName == "" {
			return fmt.Errorf("declaration appears before package: %q", lines[0])
		}
		text := strings.Join(lines, "\n")
		sum := sha256.Sum256([]byte(text))
		out = append(out, declaration{Package: packageName, Declaration: text, SHA256: hex.EncodeToString(sum[:])})
		lines = nil
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), " \t\r")
		if strings.HasPrefix(line, "package ") {
			if err := flush(); err != nil {
				return nil, err
			}
			packageName = strings.TrimSpace(strings.TrimPrefix(line, "package "))
			if packageName == "" {
				return nil, fmt.Errorf("empty package declaration")
			}
			continue
		}
		candidate := ""
		if strings.HasPrefix(line, "  ") {
			candidate = line[2:]
		}
		startsDeclaration := strings.HasPrefix(candidate, "const ") || strings.HasPrefix(candidate, "var ") || strings.HasPrefix(candidate, "func ") || strings.HasPrefix(candidate, "type ")
		if startsDeclaration {
			if err := flush(); err != nil {
				return nil, err
			}
			lines = append(lines, line[2:])
			continue
		}
		if len(lines) > 0 {
			line = strings.TrimPrefix(line, "  ")
			lines = append(lines, line)
			continue
		}
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			return nil, fmt.Errorf("unexpected snapshot line %q", line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

func removedDeclarations(baseline, current []declaration) []declaration {
	present := make(map[string]struct{}, len(current))
	for _, item := range current {
		present[declarationKey(item.Package, item.SHA256)] = struct{}{}
	}
	removed := make([]declaration, 0)
	for _, item := range baseline {
		if _, ok := present[declarationKey(item.Package, item.SHA256)]; !ok {
			removed = append(removed, item)
		}
	}
	slices.SortFunc(removed, func(left, right declaration) int {
		if comparison := strings.Compare(left.Package, right.Package); comparison != 0 {
			return comparison
		}
		return strings.Compare(left.Declaration, right.Declaration)
	})
	return removed
}

func validateCompatibility(removed []declaration, waivers []removalWaiver) error {
	removedByKey := make(map[string]declaration, len(removed))
	for _, item := range removed {
		removedByKey[declarationKey(item.Package, item.SHA256)] = item
	}
	waived := make(map[string]removalWaiver, len(waivers))
	var problems []string
	for index, waiver := range waivers {
		waiver.Package = strings.TrimSpace(waiver.Package)
		waiver.SHA256 = strings.TrimSpace(waiver.SHA256)
		waiver.Symbol = strings.TrimSpace(waiver.Symbol)
		waiver.Reason = strings.TrimSpace(waiver.Reason)
		if waiver.Package == "" || waiver.SHA256 == "" || waiver.Symbol == "" || waiver.Reason == "" {
			problems = append(problems, fmt.Sprintf("waiver %d must include package, sha256, symbol, and reason", index))
			continue
		}
		key := declarationKey(waiver.Package, waiver.SHA256)
		if _, duplicate := waived[key]; duplicate {
			problems = append(problems, fmt.Sprintf("duplicate waiver for %s %s", waiver.Package, waiver.SHA256))
			continue
		}
		waived[key] = waiver
		if _, used := removedByKey[key]; !used {
			problems = append(problems, fmt.Sprintf("stale waiver for %s %s (%s)", waiver.Package, waiver.SHA256, waiver.Symbol))
		}
	}
	for _, item := range removed {
		key := declarationKey(item.Package, item.SHA256)
		if _, ok := waived[key]; ok {
			continue
		}
		problems = append(problems, fmt.Sprintf("unwaived removal %s %s:\n%s", item.Package, item.SHA256, item.Declaration))
	}
	if len(problems) > 0 {
		return fmt.Errorf("%s", strings.Join(problems, "\n"))
	}
	return nil
}

func declarationKey(packageName, digest string) string {
	return strings.TrimSpace(packageName) + "\x00" + strings.TrimSpace(digest)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sdk-api-compat: "+format+"\n", args...)
	os.Exit(1)
}
