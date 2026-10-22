// Package config loads server settings from the environment, optionally
// seeded from a .env file.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"
)

type Config struct {
	Addr      string
	StaticDir string
	DBPath    string

	DiscordClientID     string
	DiscordClientSecret string
	DiscordRedirectURL  string
	DiscordBotToken     string
	AppAdminIDs         []string // optional; may register and configure any server and see every server
	SelfRegistration    bool     // server managers may register their own servers
	CookieSecure        bool
	SessionTTL          time.Duration
	AccessCacheTTL      time.Duration

	SuggestionsPollPeriod time.Duration

	YouTubeAPIKey string // optional; enables video/playlist lookups

	TwitchClientID     string // optional, with TwitchClientSecret; enables clip lookups
	TwitchClientSecret string

	// Legacy single-server settings. If set, they are imported once as a
	// registered server on startup (see Store.ClaimUnassigned).
	LegacyGuildID              string
	LegacyModRoleIDs           []string
	LegacyAllowedUsers         []string
	LegacySuggestionsChannelID string
}

// Load reads .env (if present; real environment variables win) and then the environment.
func Load(envFile string) (Config, error) {
	if err := loadDotEnv(envFile); err != nil {
		return Config{}, err
	}
	c := Config{
		Addr:                getenv("ADDR", ":8080"),
		StaticDir:           getenv("STATIC_DIR", "web/dist"),
		DBPath:              getenv("DATABASE_PATH", "video-queue.db"),
		DiscordClientID:     os.Getenv("DISCORD_CLIENT_ID"),
		DiscordClientSecret: os.Getenv("DISCORD_CLIENT_SECRET"),
		DiscordRedirectURL:  getenv("DISCORD_REDIRECT_URL", "http://localhost:5173/api/auth/callback"),
		DiscordBotToken:     os.Getenv("DISCORD_BOT_TOKEN"),
		AppAdminIDs:         splitList(os.Getenv("APP_ADMIN_USER_IDS")),
		CookieSecure:        os.Getenv("COOKIE_SECURE") == "true",
		SelfRegistration:    getenv("SELF_REGISTRATION", "true") == "true",
		YouTubeAPIKey:       os.Getenv("YOUTUBE_API_KEY"),
		TwitchClientID:      os.Getenv("TWITCH_CLIENT_ID"),
		TwitchClientSecret:  os.Getenv("TWITCH_CLIENT_SECRET"),

		LegacyGuildID:              os.Getenv("DISCORD_GUILD_ID"),
		LegacyModRoleIDs:           splitList(os.Getenv("DISCORD_MOD_ROLE_IDS")),
		LegacyAllowedUsers:         splitList(os.Getenv("DISCORD_ALLOWED_USER_IDS")),
		LegacySuggestionsChannelID: os.Getenv("DISCORD_SUGGESTIONS_CHANNEL_ID"),
	}
	var err error
	if c.SessionTTL, err = time.ParseDuration(getenv("SESSION_TTL", "168h")); err != nil {
		return Config{}, fmt.Errorf("SESSION_TTL: %w", err)
	}
	if c.AccessCacheTTL, err = time.ParseDuration(getenv("ACCESS_CACHE_TTL", "10m")); err != nil {
		return Config{}, fmt.Errorf("ACCESS_CACHE_TTL: %w", err)
	}
	if c.SuggestionsPollPeriod, err = time.ParseDuration(getenv("SUGGESTIONS_POLL_INTERVAL", "2m")); err != nil {
		return Config{}, fmt.Errorf("SUGGESTIONS_POLL_INTERVAL: %w", err)
	}

	var missing []string
	for name, v := range map[string]string{
		"DISCORD_CLIENT_ID":     c.DiscordClientID,
		"DISCORD_CLIENT_SECRET": c.DiscordClientSecret,
		"DISCORD_BOT_TOKEN":     c.DiscordBotToken,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required settings: %s", strings.Join(missing, ", "))
	}
	// Without self-registration, only admins can register servers, so at least one is needed.
	if len(c.AppAdminIDs) == 0 && !c.SelfRegistration {
		return Config{}, errors.New("SELF_REGISTRATION=false needs APP_ADMIN_USER_IDS (your Discord user ID), or nobody can register servers")
	}
	return c, nil
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, val)
		}
	}
	return sc.Err()
}
