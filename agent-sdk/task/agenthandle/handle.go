package agenthandle

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

var namePool = []string{
	"emun", "ari", "aria", "ava", "ben", "cai", "cleo", "dana",
	"eden", "eli", "emma", "finn", "gia", "hugo", "ivy", "jane",
	"juno", "kai", "kira", "lena", "leo", "lina", "luca", "maya",
	"mila", "mina", "mira", "nina", "nora", "olga", "omar", "otto",
	"palo", "quinn", "rhea", "rio", "sara", "tala", "tari", "thea",
	"uma", "vera", "wren", "xena", "yara", "zane", "zoe", "noa",
	"nia", "zuri", "mika", "niko", "rafe", "ravi", "reya", "sima",
	"tova", "yuki", "akio", "amir", "anika", "arlo", "bela", "brio",
	"cora", "dario", "elio", "farah", "hani", "iona", "keira", "lumi",
	"maia", "navi", "orla", "pippa", "rohan", "siena", "tessa", "timo",
	"vito", "wen", "yuna", "zola", "alba", "azra", "bryn", "cian",
	"dora", "eira", "faye", "gwen", "hale", "inez", "joel", "kian",
	"lora", "mona", "pavel", "remy", "sona", "tina", "urie", "vida",
	"willa", "xavi", "yori", "ziva",
}

// Allocate returns a short human handle that is unique within used. It prefers
// the shared human-name pool and falls back to a normalized agent-derived base
// only after the pool is exhausted.
func Allocate(used map[string]struct{}, agent string) string {
	if used == nil {
		used = map[string]struct{}{}
	}
	if len(namePool) > 0 {
		offset := randomOffset(len(namePool))
		for i := 0; i < len(namePool); i++ {
			candidate := Normalize(namePool[(offset+i)%len(namePool)])
			if candidate == "" {
				continue
			}
			if _, exists := used[candidate]; !exists {
				return candidate
			}
		}
	}
	for _, base := range fallbackBases(agent) {
		for i := 0; i < 1000; i++ {
			candidate := base
			if i > 0 {
				candidate = fmt.Sprintf("%s%d", base, i+1)
			}
			if _, exists := used[candidate]; !exists {
				return candidate
			}
		}
	}
	return "agent"
}

// ContainsPoolName reports whether name matches a normalized entry from the
// shared human-name pool.
func ContainsPoolName(name string) bool {
	name = Normalize(name)
	if name == "" {
		return false
	}
	for _, candidate := range namePool {
		if Normalize(candidate) == name {
			return true
		}
	}
	return false
}

// Normalize lowercases value and strips a leading @ mention prefix.
func Normalize(value string) string {
	return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(value), "@"))
}

// NormalizeBase derives a stable handle base from value by lowercasing,
// stripping @, and replacing separators with dashes.
func NormalizeBase(value string) string {
	value = Normalize(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		var keep rune
		switch {
		case r >= 'a' && r <= 'z':
			keep = r
		case r >= '0' && r <= '9':
			keep = r
		case r == '-' || r == '_':
			keep = r
		case r == '/' || r == '.' || r == ' ' || r == '\t':
			if !lastDash && b.Len() > 0 {
				keep = '-'
				lastDash = true
			}
		}
		if keep == 0 {
			continue
		}
		if keep != '-' && keep != '_' {
			lastDash = false
		}
		b.WriteRune(keep)
	}
	return strings.Trim(b.String(), "-_")
}

func randomOffset(n int) int {
	if n <= 1 {
		return 0
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(value.Int64())
}

func fallbackBases(agent string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 2)
	for _, base := range []string{NormalizeBase(agent), "agent"} {
		if base == "" {
			continue
		}
		if _, ok := seen[base]; ok {
			continue
		}
		seen[base] = struct{}{}
		out = append(out, base)
	}
	return out
}
