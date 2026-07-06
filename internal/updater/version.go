package updater

import (
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

func displayVersion(value string) string {
	version := npmVersion(value)
	if version == "" || version == "dev" {
		return version
	}
	return "v" + version
}

func npmVersion(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "v")
	return value
}

func isDevVersion(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "", "dev", "(devel)", "0.0.0-dev":
		return true
	default:
		return false
	}
}

func compareVersions(left string, right string) int {
	if lv, lok := normalizeSemver(left); lok {
		if rv, rok := normalizeSemver(right); rok {
			return semver.Compare(lv, rv)
		}
	}
	return compareLooseVersions(left, right)
}

func normalizeSemver(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	if strings.HasPrefix(value, "V") {
		value = "v" + strings.TrimPrefix(value, "V")
	} else if !strings.HasPrefix(value, "v") {
		value = "v" + value
	}
	if !semver.IsValid(value) {
		return "", false
	}
	return value, true
}

func compareLooseVersions(left string, right string) int {
	lv := parseVersion(left)
	rv := parseVersion(right)
	for i := 0; i < 3; i++ {
		switch {
		case lv.nums[i] > rv.nums[i]:
			return 1
		case lv.nums[i] < rv.nums[i]:
			return -1
		}
	}
	switch {
	case lv.pre == "" && rv.pre != "":
		return 1
	case lv.pre != "" && rv.pre == "":
		return -1
	case lv.pre > rv.pre:
		return 1
	case lv.pre < rv.pre:
		return -1
	default:
		return 0
	}
}

type parsedVersion struct {
	nums [3]int
	pre  string
}

func parseVersion(value string) parsedVersion {
	value = npmVersion(value)
	if idx := strings.IndexByte(value, '+'); idx >= 0 {
		value = value[:idx]
	}
	pre := ""
	if idx := strings.IndexByte(value, '-'); idx >= 0 {
		pre = value[idx+1:]
		value = value[:idx]
	}
	parts := strings.Split(value, ".")
	out := parsedVersion{pre: pre}
	for i := 0; i < len(parts) && i < len(out.nums); i++ {
		n, _ := strconv.Atoi(parts[i])
		out.nums[i] = n
	}
	return out
}
