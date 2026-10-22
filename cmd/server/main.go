// Command server runs the video-queue HTTP API and serves the built frontend.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/danotchuuy/video-queue/internal/access"
	"github.com/danotchuuy/video-queue/internal/api"
	"github.com/danotchuuy/video-queue/internal/auth"
	"github.com/danotchuuy/video-queue/internal/clips"
	"github.com/danotchuuy/video-queue/internal/config"
	"github.com/danotchuuy/video-queue/internal/discord"
	"github.com/danotchuuy/video-queue/internal/store"
	"github.com/danotchuuy/video-queue/internal/suggest"
	"github.com/danotchuuy/video-queue/internal/twitch"
	"github.com/danotchuuy/video-queue/internal/youtube"
)

func main() {
	envFile := flag.String("env", ".env", "optional .env file to load settings from")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(*envFile, logger); err != nil {
		logger.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(envFile string, logger *slog.Logger) error {
	cfg, err := config.Load(envFile)
	if err != nil {
		return err
	}
	if len(cfg.AppAdminIDs) == 0 {
		logger.Warn("no APP_ADMIN_USER_IDS set: servers can only be registered and managed by people with Manage Server on them")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer st.Close()

	bot := discord.NewClient(cfg.DiscordBotToken)
	if cfg.LegacyGuildID != "" {
		importLegacyServer(ctx, cfg, st, bot, logger)
	}

	authSvc := auth.New(auth.Config{
		ClientID:     cfg.DiscordClientID,
		ClientSecret: cfg.DiscordClientSecret,
		RedirectURL:  cfg.DiscordRedirectURL,
		CookieSecure: cfg.CookieSecure,
		SessionTTL:   cfg.SessionTTL,
	}, st)

	poller := discord.NewPoller(bot, st, cfg.SuggestionsPollPeriod, logger)

	// Leave YouTube nil (not a typed-nil *youtube.Client) when no key is set,
	// so the API reports lookups as unavailable.
	// After each sync, look up YouTube details for suggestions and Twitch
	// details for clips, for whichever APIs are configured.
	var afterSync []func(ctx context.Context, serverID int64) error
	var yt api.YouTube
	if cfg.YouTubeAPIKey != "" {
		ytClient := youtube.NewClient(cfg.YouTubeAPIKey)
		yt = ytClient
		afterSync = append(afterSync, suggest.NewEnricher(st, ytClient, logger).Enrich)
	} else {
		logger.Info("YouTube lookups disabled: set YOUTUBE_API_KEY to enable")
	}
	twitchEnabled := cfg.TwitchClientID != "" && cfg.TwitchClientSecret != ""
	if twitchEnabled {
		afterSync = append(afterSync, clips.NewEnricher(st, twitch.NewClient(cfg.TwitchClientID, cfg.TwitchClientSecret), logger).Enrich)
	} else {
		logger.Info("Twitch clip lookups disabled: set TWITCH_CLIENT_ID and TWITCH_CLIENT_SECRET to enable")
	}
	if len(afterSync) > 0 {
		poller.AfterSync = func(ctx context.Context, serverID int64) {
			for _, enrich := range afterSync {
				if err := enrich(ctx, serverID); err != nil {
					logger.Warn("lookup after sync failed; will retry next sync", "server_id", serverID, "err", err)
				}
			}
		}
	}

	go poller.Run(ctx)
	go cleanupSessions(ctx, st, logger)

	mux := newMux(cfg.StaticDir)
	authSvc.Register(mux)
	api.New(api.Deps{
		Store:            st,
		Access:           accessAdapter{access.New(st, bot, cfg.AppAdminIDs, cfg.AccessCacheTTL, logger)},
		Bot:              bot,
		Sync:             poller.SyncServer,
		ClientID:         cfg.DiscordClientID,
		SelfRegistration: cfg.SelfRegistration,
		YouTube:          yt,
		TwitchEnabled:    twitchEnabled,
		Log:              logger,
	}).Register(mux, authSvc.Require)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// accessAdapter exposes an access.Checker through the api package's Perms type.
type accessAdapter struct{ *access.Checker }

func (a accessAdapter) Check(ctx context.Context, userID string, srv store.Server) (api.Perms, error) {
	p, err := a.Checker.Check(ctx, userID, srv)
	return api.Perms{Access: p.Access, Full: p.Full, Manage: p.Manage, Videos: p.Videos, Clips: p.Clips}, err
}

// importLegacyServer registers the server from the old single-server settings
// (DISCORD_GUILD_ID etc.) and attaches any content created before multi-server
// support to it. It is idempotent, so the settings can stay in .env until removed.
func importLegacyServer(ctx context.Context, cfg config.Config, st *store.Store, bot *discord.Client, logger *slog.Logger) {
	in := store.Server{
		GuildID:              cfg.LegacyGuildID,
		Name:                 "Imported server",
		ModRoleIDs:           cfg.LegacyModRoleIDs,
		AllowedUserIDs:       cfg.LegacyAllowedUsers,
		SuggestionsChannelID: cfg.LegacySuggestionsChannelID,
	}
	if g, err := bot.Guild(ctx, cfg.LegacyGuildID); err == nil {
		in.Name, in.GuildName, in.Icon = g.Name, g.Name, g.Icon
	} else {
		logger.Warn("legacy import: couldn't look up guild; rename it in Admin", "guild_id", cfg.LegacyGuildID, "err", err)
	}
	srv, claimed, err := st.ClaimUnassigned(ctx, in)
	if err != nil {
		logger.Error("legacy import failed", "err", err)
		return
	}
	if claimed > 0 {
		logger.Info("legacy import: attached existing data to server", "server", srv.Name, "rows", claimed)
	}
	logger.Info("DISCORD_GUILD_ID and related settings are imported; you can remove them from .env")
}

// cleanupSessions periodically removes expired sessions.
func cleanupSessions(ctx context.Context, st *store.Store, logger *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if err := st.DeleteExpiredSessions(ctx, time.Now()); err != nil && ctx.Err() == nil {
			logger.Error("delete expired sessions", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func newMux(staticDir string) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", handleHealth)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	})
	mux.Handle("/", spaHandler(staticDir))
	return mux
}

func handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write json", "err", err)
	}
}

// spaHandler serves files from dir, falling back to index.html so client-side
// routes resolve to the frontend app.
func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		files.ServeHTTP(w, r)
	})
}
