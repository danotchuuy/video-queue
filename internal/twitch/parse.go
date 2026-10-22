// Package twitch parses Twitch clip URLs and looks clips up in the Twitch Helix API.
package twitch

import (
	"net/url"
	"regexp"
	"strings"
)

var slugRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

// ParseClipSlug extracts the clip slug (Helix clip ID) from a Twitch clip URL:
//
//	https://clips.twitch.tv/{slug}
//	https://clips.twitch.tv/embed?clip={slug}
//	https://(www.|m.)twitch.tv/{channel}/clip/{slug}
//	https://m.twitch.tv/clip/{slug}
//
// Only http(s) URLs on those hosts are accepted, and the slug must match a strict pattern.
func ParseClipSlug(s string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Port() != "" {
		return "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	var slug string
	switch strings.ToLower(u.Hostname()) {
	case "clips.twitch.tv":
		switch {
		case len(segs) == 1 && segs[0] == "embed":
			slug = u.Query().Get("clip")
		case len(segs) == 1:
			slug = segs[0]
		}
	case "twitch.tv", "www.twitch.tv", "m.twitch.tv":
		switch {
		case len(segs) == 3 && segs[1] == "clip":
			slug = segs[2]
		case len(segs) == 2 && segs[0] == "clip":
			slug = segs[1]
		}
	}
	if !slugRe.MatchString(slug) {
		return "", false
	}
	return slug, true
}

// FirstClip returns the first Twitch clip URL in Discord message text and its
// slug, or "", "". It handles <suppressed>, ||spoiler||, [text](url) links,
// surrounding parentheses, trailing punctuation and links without a scheme.
func FirstClip(content string) (clipURL, slug string) {
	for _, tok := range strings.Fields(content) {
		tok = cleanToken(tok)
		if !strings.Contains(tok, "://") {
			lower := strings.ToLower(tok)
			if !strings.HasPrefix(lower, "clips.twitch.tv/") && !strings.HasPrefix(lower, "twitch.tv/") &&
				!strings.HasPrefix(lower, "www.twitch.tv/") && !strings.HasPrefix(lower, "m.twitch.tv/") {
				continue
			}
			tok = "https://" + tok
		}
		if s, ok := ParseClipSlug(tok); ok {
			return tok, s
		}
	}
	return "", ""
}

func cleanToken(tok string) string {
	const trailing = ").,!?;:>"
	tok = strings.Trim(tok, "|")
	if i := strings.Index(tok, "]("); i >= 0 {
		tok = tok[i+2:]
	}
	for {
		next := strings.TrimRight(strings.Trim(tok, "|"), trailing)
		next = strings.TrimLeft(next, "<(")
		if next == tok {
			return tok
		}
		tok = next
	}
}
