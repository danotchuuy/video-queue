package twitch

import "testing"

func TestParseClipSlug(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"https://clips.twitch.tv/AwkwardHelplessSalamanderSwiftRage-abc_123", "AwkwardHelplessSalamanderSwiftRage-abc_123"},
		{"https://clips.twitch.tv/Slug1?tt_content=x", "Slug1"},
		{"https://clips.twitch.tv/embed?clip=Slug2&parent=example.com", "Slug2"},
		{"https://www.twitch.tv/somestreamer/clip/Slug3?filter=clips", "Slug3"},
		{"https://twitch.tv/somestreamer/clip/Slug4", "Slug4"},
		{"https://m.twitch.tv/somestreamer/clip/Slug5", "Slug5"},
		{"https://m.twitch.tv/clip/Slug6", "Slug6"},
		{"https://WWW.Twitch.TV/streamer/clip/Slug7", "Slug7"},
		// rejected
		{"https://www.twitch.tv/somestreamer", ""},
		{"https://www.twitch.tv/videos/123456", ""},
		{"https://clips.twitch.tv/", ""},
		{"https://clips.twitch.tv/a/b", ""},
		{"https://clips.twitch.tv/bad<slug>", ""},
		{"https://clips.twitch.tv.evil.example/Slug", ""},
		{"https://evil.example/streamer/clip/Slug", ""},
		{"https://clips.twitch.tv:8443/Slug", ""},
		{"https://user@clips.twitch.tv/Slug", ""},
		{"javascript:alert(1)", ""},
		{"ftp://clips.twitch.tv/Slug", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got, ok := ParseClipSlug(tt.in)
		if got != tt.want || ok != (tt.want != "") {
			t.Errorf("ParseClipSlug(%q) = %q, %v; want %q", tt.in, got, ok, tt.want)
		}
	}
}

func TestFirstClip(t *testing.T) {
	tests := []struct {
		name, in, wantURL, wantSlug string
	}{
		{"plain", "lol https://clips.twitch.tv/Funny1 so good", "https://clips.twitch.tv/Funny1", "Funny1"},
		{"suppressed", "<https://clips.twitch.tv/Funny2>", "https://clips.twitch.tv/Funny2", "Funny2"},
		{"markdown", "[clip](https://www.twitch.tv/s/clip/Funny3)", "https://www.twitch.tv/s/clip/Funny3", "Funny3"},
		{"no scheme", "clips.twitch.tv/Funny4!", "https://clips.twitch.tv/Funny4", "Funny4"},
		{"parenthesized", "(https://clips.twitch.tv/Funny5)", "https://clips.twitch.tv/Funny5", "Funny5"},
		{"first clip wins", "https://youtu.be/abcdefghijk https://clips.twitch.tv/A1 https://clips.twitch.tv/B2", "https://clips.twitch.tv/A1", "A1"},
		{"no clip", "https://www.twitch.tv/streamer and twitchy words", "", ""},
		{"empty", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, s := FirstClip(tt.in)
			if u != tt.wantURL || s != tt.wantSlug {
				t.Errorf("FirstClip(%q) = %q, %q; want %q, %q", tt.in, u, s, tt.wantURL, tt.wantSlug)
			}
		})
	}
}
