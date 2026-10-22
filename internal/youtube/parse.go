package youtube

import (
	"net/url"
	"regexp"
	"strings"
)

var (
	videoIDRe    = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	playlistIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{2,64}$`)
)

var playlistPrefixes = []string{"PL", "UU", "OL", "FL", "RD", "LL"}

// youtubeHosts are the accepted (lowercased) URL hosts; a port never matches.
var youtubeHosts = map[string]bool{
	"youtube.com":       true,
	"www.youtube.com":   true,
	"m.youtube.com":     true,
	"music.youtube.com": true,
	"youtu.be":          true,
}

// parseURL returns u only for plain http(s) URLs on a known YouTube host.
func parseURL(s string) (*url.URL, bool) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, false
	}
	if !youtubeHosts[strings.ToLower(u.Host)] {
		return nil, false
	}
	return u, true
}

// ParseVideoID extracts an 11-character video ID from a YouTube URL or a bare ID.
func ParseVideoID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.ContainsAny(s, "/:") {
		if !videoIDRe.MatchString(s) {
			return "", false
		}
		return s, true
	}
	u, ok := parseURL(s)
	if !ok {
		return "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	var id string
	switch {
	case strings.EqualFold(u.Host, "youtu.be"):
		if len(segs) == 1 {
			id = segs[0]
		}
	case len(segs) == 1 && segs[0] == "watch":
		id = u.Query().Get("v")
	case len(segs) == 2 && (segs[0] == "shorts" || segs[0] == "embed" || segs[0] == "live" || segs[0] == "v"):
		id = segs[1]
	}
	if !videoIDRe.MatchString(id) {
		return "", false
	}
	return id, true
}

// ParsePlaylistID extracts a playlist ID from a YouTube URL (list= parameter) or a bare ID.
func ParsePlaylistID(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !strings.ContainsAny(s, "/:") {
		if !isBarePlaylistID(s) {
			return "", false
		}
		return s, true
	}
	u, ok := parseURL(s)
	if !ok {
		return "", false
	}
	if id := u.Query().Get("list"); id != "" {
		if !playlistIDRe.MatchString(id) {
			return "", false
		}
		return id, true
	}
	// Show pages (/show/VL{playlist ID}) and YouTube Music browse pages
	// (/browse/VL{playlist ID}) wrap a playlist in an internal "VL" browse ID.
	// Show IDs without the VL prefix (e.g. "SC...") aren't playlists.
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) == 2 && (segs[0] == "show" || segs[0] == "browse") {
		if id, found := strings.CutPrefix(segs[1], "VL"); found && isBarePlaylistID(id) {
			return id, true
		}
	}
	return "", false
}

// isBarePlaylistID reports whether s looks like a playlist ID on its own.
func isBarePlaylistID(s string) bool {
	if !playlistIDRe.MatchString(s) {
		return false
	}
	for _, p := range playlistPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
