package suggest

import "testing"

func TestIsSelfSuggestion(t *testing.T) {
	tests := []struct {
		name    string
		discord []string
		channel []string
		want    bool
	}{
		{"display name equals channel title", []string{"Druid Grove", "druid"}, []string{"Druid Grove", "@other"}, true},
		{"username equals handle", []string{"Someone", "druidgrove"}, []string{"Other Channel", "@DruidGrove"}, true},
		{"punctuation and case ignored", []string{"Druid_Grove!"}, []string{"druid grove"}, true},
		{"unicode letters kept", []string{"Ätherwind"}, []string{"ätherwind"}, true},
		{"different names", []string{"Druid Grove", "druidgrove"}, []string{"TeamFourStar", "@TeamFourStar"}, false},
		{"substring is not a match", []string{"Druid"}, []string{"Druid Grove"}, false},
		{"too short to match", []string{"ab"}, []string{"AB"}, false},
		{"empty channel names", []string{"Druid"}, []string{"", ""}, false},
		{"empty discord names", []string{"", "!!"}, []string{"", "!!"}, false},
		{"no names", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSelfSuggestion(tt.discord, tt.channel); got != tt.want {
				t.Errorf("IsSelfSuggestion(%q, %q) = %v, want %v", tt.discord, tt.channel, got, tt.want)
			}
		})
	}
}
