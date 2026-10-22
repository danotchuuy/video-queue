package config

import (
	"strings"
	"testing"
)

// setRequired sets the always-required settings and clears the ones under test.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("DISCORD_CLIENT_ID", "id")
	t.Setenv("DISCORD_CLIENT_SECRET", "secret")
	t.Setenv("DISCORD_BOT_TOKEN", "token")
	t.Setenv("APP_ADMIN_USER_IDS", "")
	t.Setenv("SELF_REGISTRATION", "")
}

func TestAdminsOptionalWithSelfRegistration(t *testing.T) {
	setRequired(t)
	c, err := Load(t.TempDir() + "/missing.env")
	if err != nil {
		t.Fatalf("Load without admins (self-registration on by default): %v", err)
	}
	if !c.SelfRegistration || len(c.AppAdminIDs) != 0 {
		t.Fatalf("SelfRegistration = %v, AppAdminIDs = %v", c.SelfRegistration, c.AppAdminIDs)
	}
}

func TestAdminsRequiredWithoutSelfRegistration(t *testing.T) {
	setRequired(t)
	t.Setenv("SELF_REGISTRATION", "false")
	if _, err := Load(t.TempDir() + "/missing.env"); err == nil || !strings.Contains(err.Error(), "APP_ADMIN_USER_IDS") {
		t.Fatalf("Load = %v, want an APP_ADMIN_USER_IDS error", err)
	}

	t.Setenv("APP_ADMIN_USER_IDS", "123456789012345678, 223456789012345678")
	c, err := Load(t.TempDir() + "/missing.env")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.AppAdminIDs) != 2 || c.AppAdminIDs[1] != "223456789012345678" {
		t.Fatalf("AppAdminIDs = %q", c.AppAdminIDs)
	}
}

func TestMissingRequiredSettings(t *testing.T) {
	setRequired(t)
	t.Setenv("DISCORD_BOT_TOKEN", "")
	if _, err := Load(t.TempDir() + "/missing.env"); err == nil || !strings.Contains(err.Error(), "DISCORD_BOT_TOKEN") {
		t.Fatalf("Load = %v, want a DISCORD_BOT_TOKEN error", err)
	}
}
