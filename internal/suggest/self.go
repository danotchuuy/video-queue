package suggest

import (
	"strings"
	"unicode"
)

// minNameLen is the shortest normalized name that may count as a match, so
// that very short names ("ab", "x") don't flag unrelated channels.
const minNameLen = 3

// normalizeName lowercases s and keeps only letters and digits, so
// "Druid Grove", "druid_grove" and "@DruidGrove" all compare equal.
func normalizeName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// IsSelfSuggestion reports whether any of the suggester's Discord names
// (display name, username) equals any of the video channel's names (title,
// @handle) after normalization. Matching is exact on purpose: similar but
// different names are not flagged.
func IsSelfSuggestion(discordNames, channelNames []string) bool {
	seen := map[string]bool{}
	for _, n := range discordNames {
		if n = normalizeName(n); len([]rune(n)) >= minNameLen {
			seen[n] = true
		}
	}
	for _, n := range channelNames {
		if seen[normalizeName(n)] {
			return true
		}
	}
	return false
}
