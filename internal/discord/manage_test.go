package discord

import "testing"

func TestCanManage(t *testing.T) {
	g := Guild{
		ID:      "1",
		OwnerID: "owner",
		Roles: []Role{
			{ID: "1", Permissions: "0"},     // @everyone
			{ID: "10", Permissions: "32"},   // Manage Server
			{ID: "11", Permissions: "8"},    // Administrator
			{ID: "12", Permissions: "2048"}, // Send Messages only
		},
	}
	tests := []struct {
		name   string
		user   string
		roles  []string
		guild  Guild
		expect bool
	}{
		{"owner", "owner", nil, g, true},
		{"manage server role", "u", []string{"10"}, g, true},
		{"administrator role", "u", []string{"11"}, g, true},
		{"unrelated role", "u", []string{"12"}, g, false},
		{"no roles", "u", nil, g, false},
		{"role from another guild ignored", "u", []string{"99"}, g, false},
		{"everyone has manage", "u", nil, Guild{ID: "1", Roles: []Role{{ID: "1", Permissions: "32"}}}, true},
		{"bad bitfield", "u", []string{"10"}, Guild{ID: "1", Roles: []Role{{ID: "10", Permissions: "x"}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanManage(tt.guild, Member{Roles: tt.roles}, tt.user); got != tt.expect {
				t.Errorf("CanManage = %v, want %v", got, tt.expect)
			}
		})
	}
}
