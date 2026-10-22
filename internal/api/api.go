// Package api implements the JSON REST handlers under /api.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danotchuuy/video-queue/internal/auth"
	"github.com/danotchuuy/video-queue/internal/discord"
	"github.com/danotchuuy/video-queue/internal/store"
	"github.com/danotchuuy/video-queue/internal/youtube"
)

const (
	maxBodyBytes = 1 << 20
	maxTitleLen  = 300
	maxNameLen   = 100

	maxChannelLen      = 200
	maxDurationSeconds = 10_000_000
)

// Indirections so tests can avoid the youtube package's parsers.
var (
	parseVideoID    = youtube.ParseVideoID
	parsePlaylistID = youtube.ParsePlaylistID
)

// Store is the subset of *store.Store used by the handlers.
type Store interface {
	ListServers(ctx context.Context) ([]store.Server, error)
	GetServer(ctx context.Context, id int64) (store.Server, error)
	CreateServer(ctx context.Context, in store.Server) (store.Server, error)
	UpdateServer(ctx context.Context, id int64, p store.ServerPatch) (store.Server, error)
	DeleteServer(ctx context.Context, id int64) error

	ListSeries(ctx context.Context, serverID int64) ([]store.Series, error)
	GetSeries(ctx context.Context, serverID, id int64) (store.Series, error)
	CreateSeries(ctx context.Context, in store.Series) (store.Series, error)
	UpdateSeries(ctx context.Context, serverID, id int64, p store.SeriesPatch) (store.Series, error)
	DeleteSeries(ctx context.Context, serverID, id int64) error

	ListVideos(ctx context.Context, f store.VideoFilter) ([]store.Video, error)
	GetVideo(ctx context.Context, serverID, id int64) (store.Video, error)
	CreateVideo(ctx context.Context, in store.Video) (store.Video, error)
	UpdateVideo(ctx context.Context, serverID, id int64, p store.VideoPatch) (store.Video, error)
	DeleteVideo(ctx context.Context, serverID, id int64) error
	ReorderVideos(ctx context.Context, serverID int64, ids []int64) error
	CreateVideos(ctx context.Context, vs []store.Video) ([]store.Video, error)
	CreateSeriesWithVideos(ctx context.Context, s store.Series, vs []store.Video) (store.Series, []store.Video, error)

	ListSuggestions(ctx context.Context, f store.SuggestionFilter) ([]store.Suggestion, error)
	SuggestersByVideo(ctx context.Context, serverID int64, videoIDs []string) (map[string][]store.Suggester, error)
	GetSuggestion(ctx context.Context, serverID, id int64) (store.Suggestion, error)
	SetSuggestionStatus(ctx context.Context, serverID, id int64, status store.SuggestionStatus, reviewerID string, now time.Time) (store.Suggestion, error)
	ApproveSuggestion(ctx context.Context, serverID, id int64, v store.Video, reviewerID string, now time.Time) (store.Suggestion, store.Video, error)
	RemoveSuggestions(ctx context.Context, serverID int64, ids []int64, now time.Time) (int64, error)
	UnremoveSuggestion(ctx context.Context, serverID, id int64) error

	ListClips(ctx context.Context, f store.ClipFilter) ([]store.Clip, error)
	GetClip(ctx context.Context, serverID, id int64) (store.Clip, error)
	UpdateClip(ctx context.Context, serverID, id int64, p store.ClipPatch, reviewerID string, now time.Time) (store.Clip, error)

	ListUserGuilds(ctx context.Context, userID string) ([]store.UserGuild, error)
	ListUsers(ctx context.Context) ([]store.User, error)
	GetUser(ctx context.Context, discordID string) (store.User, error)
}

// Perms is what a user may do on a registered server. Access means any
// section; Full is every section (including suggestions); Videos and Clips are
// the video and clip pages (each implied by Full); Manage covers the settings
// and deletion.
type Perms struct{ Access, Full, Manage, Videos, Clips bool }

// has reports whether p covers the section (manage is checked separately).
func (p Perms) has(need section) bool {
	switch need {
	case secVideos:
		return p.Videos
	case secClips:
		return p.Clips
	case secFull:
		return p.Full
	case secManage:
		return p.Manage
	}
	return p.Access
}

// section is the access a route needs on a server.
type section int

const (
	secAny    section = iota // any access
	secVideos                // video pages: queue, pipeline, series
	secClips                 // clip pages
	secFull                  // full access only (suggestions)
	secManage                // settings and deletion
)

// Access decides who may use the app and each server.
type Access interface {
	IsAdmin(userID string) bool
	Check(ctx context.Context, userID string, srv store.Server) (Perms, error)
	// CanManageGuild is a live check that the user manages a Discord server.
	CanManageGuild(ctx context.Context, userID, guildID string) (bool, error)
}

// Bot is the subset of the Discord bot client used by the handlers.
type Bot interface {
	BotGuilds(ctx context.Context) ([]discord.Guild, error)
	Guild(ctx context.Context, guildID string) (discord.Guild, error)
	GuildRoles(ctx context.Context, guildID string) ([]discord.Role, error)
	GuildTextChannels(ctx context.Context, guildID string) ([]discord.Channel, error)
}

// Syncer pulls new suggestions for one server on demand. A nil Syncer means
// syncing is disabled.
type Syncer func(ctx context.Context, serverID int64) error

// YouTube is the subset of the YouTube client used by the handlers.
type YouTube interface {
	Video(ctx context.Context, id string) (youtube.Video, error)
	Playlist(ctx context.Context, id string) (youtube.Playlist, []youtube.PlaylistItem, error)
}

// Deps are the API's dependencies.
type Deps struct {
	Store    Store
	Access   Access
	Bot      Bot
	Sync     Syncer // nil means syncing is disabled
	ClientID string // Discord application ID, for the bot invite URL
	Log      *slog.Logger

	// YouTube is nil when lookups are not configured. Callers must leave it as
	// a true nil interface (not a typed-nil pointer) when there is no API key,
	// so only assign it when a client exists.
	YouTube YouTube

	TwitchEnabled bool // Twitch lookups are configured (shown in /api/me)

	SelfRegistration bool // non-admins may register servers they manage
}

// currentUser is a hook so tests can avoid the auth package.
var currentUser = auth.UserFromContext

// API holds the handler dependencies.
type API struct {
	st       Store
	access   Access
	bot      Bot
	sync     Syncer
	yt       YouTube
	clientID string
	log      *slog.Logger
	now      func() time.Time

	twitchEnabled    bool
	selfRegistration bool
}

// New returns an API backed by d.
func New(d Deps) *API {
	log := d.Log
	if log == nil {
		log = slog.Default()
	}
	return &API{st: d.Store, access: d.Access, bot: d.Bot, sync: d.Sync, yt: d.YouTube,
		clientID: d.ClientID, log: log, now: time.Now, twitchEnabled: d.TwitchEnabled, selfRegistration: d.SelfRegistration}
}

// srvHandler is a handler for a route under /api/servers/{sid}; srv is the
// loaded server the user may access.
type srvHandler func(w http.ResponseWriter, r *http.Request, srv store.Server)

// Register adds all routes to mux, each wrapped with require.
func (a *API) Register(mux *http.ServeMux, require func(http.Handler) http.Handler) {
	route := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, require(h))
	}
	any := func(pattern string, h srvHandler) { route(pattern, a.withServer(secAny, h)) }
	videos := func(pattern string, h srvHandler) { route(pattern, a.withServer(secVideos, h)) }
	clips := func(pattern string, h srvHandler) { route(pattern, a.withServer(secClips, h)) }
	full := func(pattern string, h srvHandler) { route(pattern, a.withServer(secFull, h)) }
	manage := func(pattern string, h srvHandler) { route(pattern, a.withServer(secManage, h)) }

	route("GET /api/me", a.me)
	route("GET /api/youtube/video", a.youtubeVideo)
	route("GET /api/youtube/playlist", a.youtubePlaylist)
	route("GET /api/servers", a.listServers)
	route("GET /api/guilds", a.listGuilds)
	route("POST /api/servers", a.createServer)

	any("GET /api/servers/{sid}", a.getServer)
	manage("PATCH /api/servers/{sid}", a.patchServer)
	manage("DELETE /api/servers/{sid}", a.deleteServer)
	manage("GET /api/servers/{sid}/discord/roles", a.discordRoles)
	manage("GET /api/servers/{sid}/discord/channels", a.discordChannels)

	videos("GET /api/servers/{sid}/series", a.listSeries)
	videos("POST /api/servers/{sid}/series", a.createSeries)
	videos("POST /api/servers/{sid}/series/import", a.importSeries)
	videos("POST /api/servers/{sid}/series/{id}/import", a.importIntoSeries)
	videos("PATCH /api/servers/{sid}/series/{id}", a.patchSeries)
	videos("DELETE /api/servers/{sid}/series/{id}", a.deleteSeries)

	videos("GET /api/servers/{sid}/videos", a.listVideos)
	videos("POST /api/servers/{sid}/videos", a.createVideo)
	videos("POST /api/servers/{sid}/videos/reorder", a.reorderVideos)
	videos("GET /api/servers/{sid}/videos/{id}", a.getVideo)
	videos("PATCH /api/servers/{sid}/videos/{id}", a.patchVideo)
	videos("DELETE /api/servers/{sid}/videos/{id}", a.deleteVideo)

	videos("GET /api/servers/{sid}/members", a.listMembers)

	full("GET /api/servers/{sid}/suggestions", a.listSuggestions)
	full("POST /api/servers/{sid}/suggestions/sync", a.syncSuggestions)
	full("POST /api/servers/{sid}/suggestions/{id}/approve", a.approveSuggestion)
	full("POST /api/servers/{sid}/suggestions/{id}/discard", a.discardSuggestion)
	full("POST /api/servers/{sid}/suggestions/{id}/restore", a.restoreSuggestion)
	full("POST /api/servers/{sid}/suggestions/remove", a.removeSuggestions)
	full("POST /api/servers/{sid}/suggestions/{id}/unremove", a.unremoveSuggestion)

	clips("GET /api/servers/{sid}/clips", a.listClips)
	clips("POST /api/servers/{sid}/clips/sync", a.syncClips)
	clips("GET /api/servers/{sid}/clips/{id}", a.getClip)
	clips("PATCH /api/servers/{sid}/clips/{id}", a.patchClip)
}

// withServer loads the {sid} server and checks the user's permissions: access
// to the section the route needs. A missing or inaccessible server is a 404;
// access without the section is a 403.
func (a *API) withServer(need section, h srvHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := a.user(w, r)
		if !ok {
			return
		}
		sid, err := strconv.ParseInt(r.PathValue("sid"), 10, 64)
		if err != nil || sid <= 0 {
			writeError(w, http.StatusNotFound, "server not found")
			return
		}
		srv, err := a.st.GetServer(r.Context(), sid)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "server not found")
			return
		}
		if err != nil {
			a.storeError(w, r, err)
			return
		}
		perms, err := a.access.Check(r.Context(), u.DiscordID, srv)
		if err != nil {
			a.log.Error("access check failed", "server", srv.ID, "user", u.DiscordID, "err", err)
			writeError(w, http.StatusBadGateway, "could not check access")
			return
		}
		if !perms.Access {
			writeError(w, http.StatusNotFound, "server not found")
			return
		}
		if need == secManage {
			if !perms.Manage {
				writeError(w, http.StatusForbidden, "you can't manage this server")
				return
			}
		} else if !perms.has(need) {
			writeError(w, http.StatusForbidden, "you don't have access to this section")
			return
		}
		h(w, r, srv)
	}
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// decodeJSON reads a single JSON object into dst, writing a 400 and returning
// false on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

// parseID parses the {id} path value, writing a 400 on failure.
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

// storeError maps store errors to responses: 404 for ErrNotFound, 409 for
// ErrConflict, else a logged 500.
func (a *API) storeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "already exists")
		return
	}
	a.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// user returns the signed-in user or writes a 401.
func (a *API) user(w http.ResponseWriter, r *http.Request) (store.User, bool) {
	u, ok := currentUser(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "not signed in")
	}
	return u, ok
}

// validTitle trims and checks a title, returning the message on failure.
func validTitle(title string) (string, string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", "title is required"
	}
	if utf8.RuneCountInString(title) > maxTitleLen {
		return "", "title is too long (max 300 characters)"
	}
	return title, ""
}

// seriesExists reports whether series id exists. On a store failure it writes
// the response and returns ok=false.
func (a *API) seriesExists(w http.ResponseWriter, r *http.Request, serverID, id int64) (exists, ok bool) {
	_, err := a.st.GetSeries(r.Context(), serverID, id)
	if errors.Is(err, store.ErrNotFound) {
		return false, true
	}
	if err != nil {
		a.storeError(w, r, err)
		return false, false
	}
	return true, true
}

// --- me & servers ---

func (a *API) me(w http.ResponseWriter, r *http.Request) {
	if u, ok := a.user(w, r); ok {
		isAdmin := a.access.IsAdmin(u.DiscordID)
		writeJSON(w, http.StatusOK, map[string]any{
			"user": u, "is_admin": isAdmin, "can_register": isAdmin || a.selfRegistration,
			"youtube_enabled": a.yt != nil, "twitch_enabled": a.twitchEnabled,
		})
	}
}

// normServer makes list fields non-nil so they encode as [] not null.
func normServer(s store.Server) store.Server {
	if s.ModRoleIDs == nil {
		s.ModRoleIDs = []string{}
	}
	if s.AllowedUserIDs == nil {
		s.AllowedUserIDs = []string{}
	}
	if s.ClipEditorRoleIDs == nil {
		s.ClipEditorRoleIDs = []string{}
	}
	if s.VideoEditorRoleIDs == nil {
		s.VideoEditorRoleIDs = []string{}
	}
	return s
}

// ServerView is a server as seen by one user.
type ServerView struct {
	store.Server
	CanManage bool     `json:"can_manage"`
	Sections  Sections `json:"sections"`
}

// Sections says which parts of a server the user may use.
type Sections struct {
	Videos      bool `json:"videos"`
	Clips       bool `json:"clips"`
	Suggestions bool `json:"suggestions"`
}

func serverView(s store.Server, p Perms) ServerView {
	return ServerView{Server: normServer(s), CanManage: p.Manage,
		Sections: Sections{Videos: p.Videos, Clips: p.Clips, Suggestions: p.Full}}
}

func (a *API) listServers(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	all, err := a.st.ListServers(r.Context())
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	out := []ServerView{}
	for _, s := range all {
		perms, err := a.access.Check(r.Context(), u.DiscordID, s)
		if err != nil {
			a.log.Error("access check failed", "server", s.ID, "user", u.DiscordID, "err", err)
			continue
		}
		if perms.Access {
			out = append(out, serverView(s, perms))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getServer(w http.ResponseWriter, r *http.Request, srv store.Server) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	perms, err := a.access.Check(r.Context(), u.DiscordID, srv)
	if err != nil {
		a.log.Error("access check failed", "server", srv.ID, "user", u.DiscordID, "err", err)
		writeError(w, http.StatusBadGateway, "could not check access")
		return
	}
	writeJSON(w, http.StatusOK, serverView(srv, perms))
}

// botError logs a Discord failure and writes a 502.
func (a *API) botError(w http.ResponseWriter, r *http.Request, err error) {
	a.log.Error("discord request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusBadGateway, "discord request failed")
}

// canRegister reports whether the user may add servers: admins always, others
// when self-registration is on.
func (a *API) canRegister(userID string) bool {
	return a.access.IsAdmin(userID) || a.selfRegistration
}

func (a *API) listGuilds(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	if !a.canRegister(u.DiscordID) {
		writeError(w, http.StatusForbidden, "self-registration is disabled")
		return
	}
	botGuilds, err := a.bot.BotGuilds(r.Context())
	if err != nil {
		a.botError(w, r, err)
		return
	}
	servers, err := a.st.ListServers(r.Context())
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	mine, err := a.st.ListUserGuilds(r.Context(), u.DiscordID)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	registered := make(map[string]int64, len(servers))
	for _, s := range servers {
		registered[s.GuildID] = s.ID
	}
	inBot := make(map[string]bool, len(botGuilds))
	for _, g := range botGuilds {
		inBot[g.ID] = true
	}
	type guildOut struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Icon       string `json:"icon"`
		ServerID   *int64 `json:"server_id"`
		BotPresent bool   `json:"bot_present"`
		InviteURL  string `json:"invite_url"`
	}
	invite := "https://discord.com/oauth2/authorize?" + url.Values{
		"client_id":   {a.clientID},
		"scope":       {"bot"},
		"permissions": {"66560"},
	}.Encode()
	out := []guildOut{}
	seen := map[string]bool{}
	add := func(id, name, icon string) {
		if seen[id] {
			return
		}
		seen[id] = true
		o := guildOut{ID: id, Name: name, Icon: icon, BotPresent: inBot[id]}
		if sid, ok := registered[id]; ok {
			o.ServerID = &sid
		}
		if !o.BotPresent {
			o.InviteURL = invite + "&guild_id=" + url.QueryEscape(id) + "&disable_guild_select=true"
		}
		out = append(out, o)
	}
	if a.access.IsAdmin(u.DiscordID) {
		for _, g := range botGuilds {
			add(g.ID, g.Name, g.Icon)
		}
	}
	for _, g := range mine {
		if g.CanManage {
			add(g.GuildID, g.Name, g.Icon)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, http.StatusOK, map[string]any{"invite_url": invite, "guilds": out})
}

// validSnowflake reports whether s looks like a Discord ID (17-20 digits).
func validSnowflake(s string) bool {
	if len(s) < 17 || len(s) > 20 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// cleanIDs trims, validates and deduplicates snowflake IDs, keeping order.
func cleanIDs(in []string) ([]string, bool) {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, id := range in {
		id = strings.TrimSpace(id)
		if !validSnowflake(id) {
			return nil, false
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, true
}

// validName trims and checks a server name, returning the message on failure.
func validName(name string) (string, string) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "name is required"
	}
	if utf8.RuneCountInString(name) > maxNameLen {
		return "", "name is too long (max 100 characters)"
	}
	return name, ""
}

func (a *API) createServer(w http.ResponseWriter, r *http.Request) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	var in struct {
		GuildID string `json:"guild_id"`
		Name    string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	in.GuildID = strings.TrimSpace(in.GuildID)
	if !validSnowflake(in.GuildID) {
		writeError(w, http.StatusBadRequest, "invalid guild_id")
		return
	}
	g, err := a.bot.Guild(r.Context(), in.GuildID)
	if errors.Is(err, discord.ErrNotFound) {
		writeError(w, http.StatusBadRequest, "bot is not in that server")
		return
	}
	if err != nil {
		a.botError(w, r, err)
		return
	}
	if !a.access.IsAdmin(u.DiscordID) {
		allowed := false
		if a.selfRegistration {
			allowed, err = a.access.CanManageGuild(r.Context(), u.DiscordID, in.GuildID)
			if err != nil {
				a.log.Error("manage check failed", "guild", in.GuildID, "user", u.DiscordID, "err", err)
				writeError(w, http.StatusBadGateway, "could not check your permissions on that server")
				return
			}
		}
		if !allowed {
			writeError(w, http.StatusForbidden, "you need Manage Server on that Discord server to add it")
			return
		}
	}
	name := in.Name
	if strings.TrimSpace(name) == "" {
		name = g.Name
	}
	name, msg := validName(name)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	srv, err := a.st.CreateServer(r.Context(), store.Server{
		GuildID: in.GuildID, Name: name, GuildName: g.Name, Icon: g.Icon,
		ModRoleIDs: []string{}, AllowedUserIDs: []string{}, CreatedBy: u.DiscordID,
	})
	if errors.Is(err, store.ErrConflict) {
		writeError(w, http.StatusConflict, "server is already registered")
		return
	}
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, serverView(srv, Perms{Access: true, Full: true, Manage: true, Videos: true, Clips: true}))
}

func (a *API) patchServer(w http.ResponseWriter, r *http.Request, srv store.Server) {
	var p store.ServerPatch
	if !decodeJSON(w, r, &p) {
		return
	}
	if p.Name != nil {
		name, msg := validName(*p.Name)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		p.Name = &name
	}
	if p.ModRoleIDs != nil {
		ids, ok := cleanIDs(*p.ModRoleIDs)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid mod_role_ids")
			return
		}
		p.ModRoleIDs = &ids
	}
	if p.ClipEditorRoleIDs != nil {
		ids, ok := cleanIDs(*p.ClipEditorRoleIDs)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid clip_editor_role_ids")
			return
		}
		p.ClipEditorRoleIDs = &ids
	}
	if p.VideoEditorRoleIDs != nil {
		ids, ok := cleanIDs(*p.VideoEditorRoleIDs)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid video_editor_role_ids")
			return
		}
		p.VideoEditorRoleIDs = &ids
	}
	if p.AllowedUserIDs != nil {
		ids, ok := cleanIDs(*p.AllowedUserIDs)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid allowed_user_ids")
			return
		}
		p.AllowedUserIDs = &ids
	}
	if p.SuggestionsChannelID != nil {
		ch, ok := a.checkChannel(w, r, srv, "suggestions", *p.SuggestionsChannelID)
		if !ok {
			return
		}
		p.SuggestionsChannelID = &ch
	}
	if p.ClipsChannelID != nil {
		ch, ok := a.checkChannel(w, r, srv, "clips", *p.ClipsChannelID)
		if !ok {
			return
		}
		p.ClipsChannelID = &ch
	}
	// The two channels must differ after the patch is applied.
	sugg, clips := srv.SuggestionsChannelID, srv.ClipsChannelID
	if p.SuggestionsChannelID != nil {
		sugg = *p.SuggestionsChannelID
	}
	if p.ClipsChannelID != nil {
		clips = *p.ClipsChannelID
	}
	if (p.SuggestionsChannelID != nil || p.ClipsChannelID != nil) && sugg != "" && sugg == clips {
		writeError(w, http.StatusBadRequest, "use different channels for clips and suggestions")
		return
	}
	updated, err := a.st.UpdateServer(r.Context(), srv.ID, p)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, serverView(updated, Perms{Access: true, Full: true, Manage: true, Videos: true, Clips: true}))
}

// checkChannel trims and validates a channel setting: "" disables it, anything
// else must be a text channel of the server's guild. kind names the setting in
// error messages. On failure it writes the response and returns ok=false.
func (a *API) checkChannel(w http.ResponseWriter, r *http.Request, srv store.Server, kind, ch string) (string, bool) {
	ch = strings.TrimSpace(ch)
	if ch == "" {
		return "", true
	}
	if !validSnowflake(ch) {
		writeError(w, http.StatusBadRequest, "invalid "+kind+"_channel_id")
		return "", false
	}
	channels, err := a.bot.GuildTextChannels(r.Context(), srv.GuildID)
	if err != nil {
		a.botError(w, r, err)
		return "", false
	}
	for _, c := range channels {
		if c.ID == ch {
			return ch, true
		}
	}
	writeError(w, http.StatusBadRequest, "unknown "+kind+" channel")
	return "", false
}

func (a *API) deleteServer(w http.ResponseWriter, r *http.Request, srv store.Server) {
	if err := a.st.DeleteServer(r.Context(), srv.ID); err != nil {
		a.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) discordRoles(w http.ResponseWriter, r *http.Request, srv store.Server) {
	roles, err := a.bot.GuildRoles(r.Context(), srv.GuildID)
	if err != nil {
		a.botError(w, r, err)
		return
	}
	if roles == nil {
		roles = []discord.Role{}
	}
	writeJSON(w, http.StatusOK, roles)
}

func (a *API) discordChannels(w http.ResponseWriter, r *http.Request, srv store.Server) {
	channels, err := a.bot.GuildTextChannels(r.Context(), srv.GuildID)
	if err != nil {
		a.botError(w, r, err)
		return
	}
	if channels == nil {
		channels = []discord.Channel{}
	}
	writeJSON(w, http.StatusOK, channels)
}

// --- series ---

type seriesInput struct {
	Title        string      `json:"title"`
	Creator      string      `json:"creator"`
	URL          string      `json:"url"`
	DefaultVenue store.Venue `json:"default_venue"`
	Notes        string      `json:"notes"`
}

func (a *API) listSeries(w http.ResponseWriter, r *http.Request, srv store.Server) {
	list, err := a.st.ListSeries(r.Context(), srv.ID)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	if list == nil {
		list = []store.Series{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) createSeries(w http.ResponseWriter, r *http.Request, srv store.Server) {
	var in seriesInput
	if !decodeJSON(w, r, &in) {
		return
	}
	title, msg := validTitle(in.Title)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if in.DefaultVenue == "" {
		in.DefaultVenue = store.VenueWatchParty
	}
	if !in.DefaultVenue.Valid() {
		writeError(w, http.StatusBadRequest, "invalid default_venue")
		return
	}
	if in.URL, msg = validLink("url", in.URL); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	s, err := a.st.CreateSeries(r.Context(), store.Series{
		ServerID: srv.ID, Title: title, Creator: in.Creator, URL: in.URL,
		DefaultVenue: in.DefaultVenue, Notes: in.Notes,
		YouTubePlaylistID: playlistIDFromURL(in.URL),
	})
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, s)
}

func (a *API) patchSeries(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var p store.SeriesPatch
	if !decodeJSON(w, r, &p) {
		return
	}
	if p.Title != nil {
		title, msg := validTitle(*p.Title)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		p.Title = &title
	}
	if p.DefaultVenue != nil && !p.DefaultVenue.Valid() {
		writeError(w, http.StatusBadRequest, "invalid default_venue")
		return
	}
	if msg := validLinks(map[string]*string{"url": p.URL}); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if p.URL != nil {
		pl := playlistIDFromURL(*p.URL)
		p.YouTubePlaylistID = &pl
	}
	s, err := a.st.UpdateSeries(r.Context(), srv.ID, id, p)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

func (a *API) deleteSeries(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := a.st.DeleteSeries(r.Context(), srv.ID, id); err != nil {
		a.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- videos ---

type videoInput struct {
	Title        string      `json:"title"`
	Venue        store.Venue `json:"venue"`
	SeriesID     *int64      `json:"series_id"`
	Episode      string      `json:"episode"`
	URL          string      `json:"url"`
	ScheduledFor *time.Time  `json:"scheduled_for"`
	Notes        string      `json:"notes"`

	ChannelTitle    string `json:"channel_title"`
	DurationSeconds int64  `json:"duration_seconds"`
}

const maxLinkLen = 2000

// validLink trims and checks a user-supplied link, returning the message on
// failure. Links are rendered as clickable hrefs for other mods, so only
// http(s) URLs with a host are accepted (no javascript:, data:, etc.).
func validLink(field, s string) (string, string) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", ""
	}
	u, err := url.Parse(s)
	if len(s) > maxLinkLen || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", field + " must be an http(s) URL"
	}
	return s, ""
}

// validLinks applies validLink to each non-nil field of a patch, in place.
func validLinks(fields map[string]*string) string {
	for name, p := range fields {
		if p == nil {
			continue
		}
		v, msg := validLink(name, *p)
		if msg != "" {
			return msg
		}
		*p = v
	}
	return ""
}

// validChannel trims and checks a channel title, returning the message on failure.
func validChannel(s string) (string, string) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxChannelLen {
		return "", "channel_title is too long (max 200 characters)"
	}
	return s, ""
}

func validDuration(n int64) bool { return n >= 0 && n <= maxDurationSeconds }

// videoIDFromURL returns the YouTube video ID in u, or "".
func videoIDFromURL(u string) string {
	if strings.TrimSpace(u) == "" {
		return ""
	}
	id, ok := parseVideoID(u)
	if !ok {
		return ""
	}
	return id
}

// playlistIDFromURL returns the YouTube playlist ID in u, or "".
func playlistIDFromURL(u string) string {
	if strings.TrimSpace(u) == "" {
		return ""
	}
	id, ok := parsePlaylistID(u)
	if !ok {
		return ""
	}
	return id
}

// buildVideo validates in and returns a queued Video. On failure it has
// already written the response and returns ok=false.
func (a *API) buildVideo(w http.ResponseWriter, r *http.Request, srv store.Server, in videoInput, userID string) (store.Video, bool) {
	title, msg := validTitle(in.Title)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return store.Video{}, false
	}
	channel, msg := validChannel(in.ChannelTitle)
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return store.Video{}, false
	}
	if !validDuration(in.DurationSeconds) {
		writeError(w, http.StatusBadRequest, "invalid duration_seconds")
		return store.Video{}, false
	}
	if in.URL, msg = validLink("url", in.URL); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return store.Video{}, false
	}
	venue := in.Venue
	if in.SeriesID != nil {
		s, err := a.st.GetSeries(r.Context(), srv.ID, *in.SeriesID)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "unknown series")
			return store.Video{}, false
		}
		if err != nil {
			a.storeError(w, r, err)
			return store.Video{}, false
		}
		if venue == "" {
			venue = s.DefaultVenue
		}
	}
	if venue == "" {
		writeError(w, http.StatusBadRequest, "venue is required")
		return store.Video{}, false
	}
	if !venue.Valid() {
		writeError(w, http.StatusBadRequest, "invalid venue")
		return store.Video{}, false
	}
	return store.Video{
		ServerID: srv.ID, SeriesID: in.SeriesID, Episode: in.Episode, Title: title, URL: in.URL,
		Venue: venue, Status: store.StatusQueued, ScheduledFor: in.ScheduledFor,
		Notes: in.Notes, CreatedBy: userID,
		YouTubeVideoID: videoIDFromURL(in.URL), ChannelTitle: channel, DurationSeconds: in.DurationSeconds,
	}, true
}

func (a *API) listVideos(w http.ResponseWriter, r *http.Request, srv store.Server) {
	q := r.URL.Query()
	f := store.VideoFilter{ServerID: srv.ID}
	if s := q.Get("status"); s != "" {
		for _, part := range strings.Split(s, ",") {
			st := store.VideoStatus(strings.TrimSpace(part))
			if !st.Valid() {
				writeError(w, http.StatusBadRequest, "invalid status")
				return
			}
			f.Status = append(f.Status, st)
		}
	}
	if v := q.Get("venue"); v != "" {
		f.Venue = store.Venue(v)
		if !f.Venue.Valid() {
			writeError(w, http.StatusBadRequest, "invalid venue")
			return
		}
	}
	if s := q.Get("series_id"); s != "" {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid series_id")
			return
		}
		f.SeriesID = id
	}
	list, err := a.st.ListVideos(r.Context(), f)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	if list == nil {
		list = []store.Video{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) createVideo(w http.ResponseWriter, r *http.Request, srv store.Server) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	var in videoInput
	if !decodeJSON(w, r, &in) {
		return
	}
	v, ok := a.buildVideo(w, r, srv, in, u.DiscordID)
	if !ok {
		return
	}
	created, err := a.st.CreateVideo(r.Context(), v)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (a *API) getVideo(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	v, err := a.st.GetVideo(r.Context(), srv.ID, id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *API) patchVideo(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	var p store.VideoPatch
	if !decodeJSON(w, r, &p) {
		return
	}
	if p.Title != nil {
		title, msg := validTitle(*p.Title)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		p.Title = &title
	}
	if p.Venue != nil && !p.Venue.Valid() {
		writeError(w, http.StatusBadRequest, "invalid venue")
		return
	}
	if p.ChannelTitle != nil {
		ch, msg := validChannel(*p.ChannelTitle)
		if msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
		p.ChannelTitle = &ch
	}
	if p.DurationSeconds != nil && !validDuration(*p.DurationSeconds) {
		writeError(w, http.StatusBadRequest, "invalid duration_seconds")
		return
	}
	if msg := validLinks(map[string]*string{
		"url": p.URL, "vod_url": p.VODURL, "edit_url": p.EditURL,
		"patreon_url": p.PatreonURL, "youtube_url": p.YouTubeURL,
	}); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if p.URL != nil {
		yid := videoIDFromURL(*p.URL)
		p.YouTubeVideoID = &yid
	}
	if p.Status != nil && !p.Status.Valid() {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	if p.SeriesID != nil {
		if p.ClearSeries {
			writeError(w, http.StatusBadRequest, "series_id and clear_series are mutually exclusive")
			return
		}
		exists, ok := a.seriesExists(w, r, srv.ID, *p.SeriesID)
		if !ok {
			return
		}
		if !exists {
			writeError(w, http.StatusBadRequest, "unknown series")
			return
		}
	}
	if !a.resolveEditor(w, r, srv, &p) {
		return
	}

	cur, err := a.st.GetVideo(r.Context(), srv.ID, id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	// Stamp the matching timestamp when entering a published/recorded status.
	if p.Status != nil {
		now := a.now().UTC()
		switch *p.Status {
		case store.StatusReacted:
			if cur.ReactedAt == nil && p.ReactedAt == nil {
				p.ReactedAt = &now
			}
		case store.StatusPatreon:
			if cur.PatreonAt == nil && p.PatreonAt == nil {
				p.PatreonAt = &now
			}
		case store.StatusYouTube:
			if cur.YouTubeAt == nil && p.YouTubeAt == nil {
				p.YouTubeAt = &now
			}
		}
	}

	v, err := a.st.UpdateVideo(r.Context(), srv.ID, id, p)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// resolveEditor validates and normalizes the editor fields of a patch. A
// non-empty editor_id must be a known user with access to the server, and
// editor_name is then taken from that user (any client value is ignored).
// Otherwise editor_name is free text, and sending it also clears editor_id.
func (a *API) resolveEditor(w http.ResponseWriter, r *http.Request, srv store.Server, p *store.VideoPatch) bool {
	if p.EditorID != nil && *p.EditorID != "" {
		eid := *p.EditorID
		if !validSnowflake(eid) {
			writeError(w, http.StatusBadRequest, "invalid editor_id")
			return false
		}
		u, err := a.st.GetUser(r.Context(), eid)
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "unknown editor")
			return false
		}
		if err != nil {
			a.storeError(w, r, err)
			return false
		}
		perms, err := a.access.Check(r.Context(), u.DiscordID, srv)
		if err != nil {
			a.log.Error("editor access check failed", "server", srv.ID, "user", u.DiscordID, "err", err)
			writeError(w, http.StatusBadGateway, "could not check access")
			return false
		}
		if !perms.Videos {
			writeError(w, http.StatusBadRequest, "that user can't access this server")
			return false
		}
		name := displayName(u)
		p.EditorName = &name
		return true
	}
	if p.EditorName != nil {
		name := strings.TrimSpace(*p.EditorName)
		if utf8.RuneCountInString(name) > maxNameLen {
			writeError(w, http.StatusBadRequest, "editor_name is too long")
			return false
		}
		p.EditorName = &name
		empty := ""
		p.EditorID = &empty
	}
	return true
}

func displayName(u store.User) string {
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

const maxMembers = 500

type member struct {
	DiscordID string `json:"discord_id"`
	Name      string `json:"name"`
	Avatar    string `json:"avatar"`
}

// listMembers returns the app users who can use the video pages (at most the
// first maxMembers users are considered).
func (a *API) listMembers(w http.ResponseWriter, r *http.Request, srv store.Server) {
	users, err := a.st.ListUsers(r.Context())
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	if len(users) > maxMembers {
		users = users[:maxMembers]
	}
	out := []member{}
	for _, u := range users {
		perms, err := a.access.Check(r.Context(), u.DiscordID, srv)
		if err != nil {
			a.log.Warn("member access check failed", "server", srv.ID, "user", u.DiscordID, "err", err)
			continue
		}
		if perms.Videos {
			out = append(out, member{DiscordID: u.DiscordID, Name: displayName(u), Avatar: u.Avatar})
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) deleteVideo(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := a.st.DeleteVideo(r.Context(), srv.ID, id); err != nil {
		a.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) reorderVideos(w http.ResponseWriter, r *http.Request, srv store.Server) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids must not be empty")
		return
	}
	seen := make(map[int64]bool, len(in.IDs))
	for _, id := range in.IDs {
		if seen[id] {
			writeError(w, http.StatusBadRequest, "ids must not contain duplicates")
			return
		}
		seen[id] = true
	}
	if err := a.st.ReorderVideos(r.Context(), srv.ID, in.IDs); err != nil {
		a.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- suggestions ---

func (a *API) listSuggestions(w http.ResponseWriter, r *http.Request, srv store.Server) {
	q := r.URL.Query()
	f := store.SuggestionFilter{ServerID: srv.ID}
	if s := q.Get("status"); s != "" {
		f.Status = store.SuggestionStatus(s)
		if !f.Status.Valid() {
			writeError(w, http.StatusBadRequest, "invalid status")
			return
		}
	}
	switch s := store.SuggestionSort(q.Get("sort")); s {
	case "":
		f.Sort = store.SortNewest
	case store.SortNewest, store.SortOldest, store.SortVotes:
		f.Sort = s
	default:
		writeError(w, http.StatusBadRequest, "invalid sort")
		return
	}
	if s := q.Get("author_id"); s != "" {
		if !validSnowflake(s) {
			writeError(w, http.StatusBadRequest, "invalid author_id")
			return
		}
		f.AuthorID = s
	}
	list, err := a.st.ListSuggestions(r.Context(), f)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	var videoIDs []string
	for _, x := range list {
		if x.YouTubeVideoID != "" {
			videoIDs = append(videoIDs, x.YouTubeVideoID)
		}
	}
	var by map[string][]store.Suggester
	if len(videoIDs) > 0 {
		if by, err = a.st.SuggestersByVideo(r.Context(), srv.ID, videoIDs); err != nil {
			a.storeError(w, r, err)
			return
		}
	}
	views := make([]SuggestionView, 0, len(list))
	for _, x := range list {
		sg := by[x.YouTubeVideoID]
		if x.YouTubeVideoID == "" || sg == nil {
			sg = []store.Suggester{}
		}
		views = append(views, SuggestionView{Suggestion: x, Suggesters: sg})
	}
	writeJSON(w, http.StatusOK, views)
}

// SuggestionView is a suggestion of the list endpoint plus everyone who suggested the same YouTube video.
type SuggestionView struct {
	store.Suggestion
	Suggesters []store.Suggester `json:"suggesters"`
}

func (a *API) syncSuggestions(w http.ResponseWriter, r *http.Request, srv store.Server) {
	if a.sync == nil {
		writeError(w, http.StatusServiceUnavailable, "suggestion sync is not configured")
		return
	}
	if srv.SuggestionsChannelID == "" {
		writeError(w, http.StatusBadRequest, "server has no suggestions channel")
		return
	}
	if err := a.sync(r.Context(), srv.ID); err != nil {
		a.log.Error("suggestion sync failed", "server", srv.ID, "err", err)
		if errors.Is(err, discord.ErrMessageContentMissing) {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "suggestion sync failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) approveSuggestion(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	var in videoInput
	if !decodeJSON(w, r, &in) {
		return
	}
	sg, err := a.st.GetSuggestion(r.Context(), srv.ID, id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	if sg.RemovedAt != nil {
		writeError(w, http.StatusNotFound, "suggestion not found")
		return
	}
	if sg.Status != store.SuggestionPending {
		writeError(w, http.StatusConflict, "suggestion is not pending")
		return
	}
	v, ok := a.buildVideo(w, r, srv, in, u.DiscordID)
	if !ok {
		return
	}
	sg, video, err := a.st.ApproveSuggestion(r.Context(), srv.ID, id, v, u.DiscordID, a.now().UTC())
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestion": sg, "video": video})
}

func (a *API) discardSuggestion(w http.ResponseWriter, r *http.Request, srv store.Server) {
	a.reviewSuggestion(w, r, srv, store.SuggestionDiscarded)
}

func (a *API) restoreSuggestion(w http.ResponseWriter, r *http.Request, srv store.Server) {
	a.reviewSuggestion(w, r, srv, store.SuggestionPending)
}

// reviewSuggestion moves a non-approved suggestion to status.
func (a *API) reviewSuggestion(w http.ResponseWriter, r *http.Request, srv store.Server, status store.SuggestionStatus) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	cur, err := a.st.GetSuggestion(r.Context(), srv.ID, id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	if cur.RemovedAt != nil {
		writeError(w, http.StatusNotFound, "suggestion not found")
		return
	}
	if cur.Status == store.SuggestionApproved {
		writeError(w, http.StatusConflict, "suggestion is already approved")
		return
	}
	sg, err := a.st.SetSuggestionStatus(r.Context(), srv.ID, id, status, u.DiscordID, a.now().UTC())
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sg)
}

// maxRemoveIDs is the most suggestion ids one remove request may carry.
const maxRemoveIDs = 500

// removeSuggestions hides the given suggestions from the Suggestions page.
func (a *API) removeSuggestions(w http.ResponseWriter, r *http.Request, srv store.Server) {
	var in struct {
		IDs []int64 `json:"ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.IDs) == 0 {
		writeError(w, http.StatusBadRequest, "ids is required")
		return
	}
	if len(in.IDs) > maxRemoveIDs {
		writeError(w, http.StatusBadRequest, "too many ids")
		return
	}
	seen := make(map[int64]bool, len(in.IDs))
	ids := make([]int64, 0, len(in.IDs))
	for _, id := range in.IDs {
		if id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid id")
			return
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	n, err := a.st.RemoveSuggestions(r.Context(), srv.ID, ids, a.now().UTC())
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"removed": n})
}

// unremoveSuggestion brings a removed suggestion back.
func (a *API) unremoveSuggestion(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := a.st.UnremoveSuggestion(r.Context(), srv.ID, id); err != nil {
		a.storeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- YouTube ---

const errYouTubeOff = "YouTube lookups are not configured"

// youtubeError maps a YouTube failure to a response.
func (a *API) youtubeError(w http.ResponseWriter, r *http.Request, err error, what string) {
	if errors.Is(err, youtube.ErrNotFound) {
		writeError(w, http.StatusNotFound, what+" not found")
		return
	}
	a.log.Error("youtube request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusBadGateway, "YouTube request failed")
}

func (a *API) youtubeVideo(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.user(w, r); !ok {
		return
	}
	id, ok := parseVideoID(r.URL.Query().Get("url"))
	if !ok {
		writeError(w, http.StatusBadRequest, "not a YouTube video URL")
		return
	}
	if a.yt == nil {
		writeError(w, http.StatusServiceUnavailable, errYouTubeOff)
		return
	}
	v, err := a.yt.Video(r.Context(), id)
	if err != nil {
		a.youtubeError(w, r, err, "video")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (a *API) youtubePlaylist(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.user(w, r); !ok {
		return
	}
	id, ok := parsePlaylistID(r.URL.Query().Get("url"))
	if !ok {
		writeError(w, http.StatusBadRequest, "not a YouTube playlist or show URL")
		return
	}
	if a.yt == nil {
		writeError(w, http.StatusServiceUnavailable, errYouTubeOff)
		return
	}
	pl, items, err := a.yt.Playlist(r.Context(), id)
	if err != nil {
		a.youtubeError(w, r, err, "playlist")
		return
	}
	if items == nil {
		items = []youtube.PlaylistItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"playlist": pl, "items": items})
}

// pickItems returns the playlist items to import, in playlist order, skipping
// IDs in skip and repeats. If ids is non-nil it filters the items and each id
// must be a playable item of the playlist; ok is false otherwise.
func pickItems(items []youtube.PlaylistItem, ids []string, skip map[string]bool) (out []youtube.PlaylistItem, ok bool) {
	var want map[string]bool
	if ids != nil {
		have := make(map[string]bool, len(items))
		for _, it := range items {
			have[it.ID] = true
		}
		want = make(map[string]bool, len(ids))
		for _, id := range ids {
			id = strings.TrimSpace(id)
			if !have[id] {
				return nil, false
			}
			want[id] = true
		}
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] || skip[it.ID] || (want != nil && !want[it.ID]) {
			continue
		}
		seen[it.ID] = true
		out = append(out, it)
	}
	return out, true
}

// itemVideo builds the queued video for a playlist item.
func itemVideo(serverID int64, seriesID *int64, venue store.Venue, it youtube.PlaylistItem, userID string) store.Video {
	u := it.URL
	if u == "" {
		u = "https://www.youtube.com/watch?v=" + it.ID
	}
	ch := it.ChannelTitle
	if utf8.RuneCountInString(ch) > maxChannelLen {
		ch = string([]rune(ch)[:maxChannelLen])
	}
	dur := it.DurationSeconds
	if !validDuration(dur) {
		dur = 0
	}
	return store.Video{
		ServerID: serverID, SeriesID: seriesID, Episode: strconv.Itoa(it.Position), Title: it.Title,
		URL: u, Venue: venue, Status: store.StatusQueued, CreatedBy: userID,
		YouTubeVideoID: it.ID, ChannelTitle: ch, DurationSeconds: dur,
	}
}

func (a *API) importSeries(w http.ResponseWriter, r *http.Request, srv store.Server) {
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	var in struct {
		PlaylistURL  string      `json:"playlist_url"`
		Title        string      `json:"title"`
		DefaultVenue store.Venue `json:"default_venue"`
		VideoIDs     []string    `json:"video_ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	plID, ok := parsePlaylistID(in.PlaylistURL)
	if !ok {
		writeError(w, http.StatusBadRequest, "not a YouTube playlist or show URL")
		return
	}
	if in.DefaultVenue == "" {
		in.DefaultVenue = store.VenueWatchParty
	}
	if !in.DefaultVenue.Valid() {
		writeError(w, http.StatusBadRequest, "invalid default_venue")
		return
	}
	title := strings.TrimSpace(in.Title)
	if title != "" {
		var msg string
		if title, msg = validTitle(title); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
	}
	if a.yt == nil {
		writeError(w, http.StatusServiceUnavailable, errYouTubeOff)
		return
	}
	pl, items, err := a.yt.Playlist(r.Context(), plID)
	if err != nil {
		a.youtubeError(w, r, err, "playlist")
		return
	}
	picked, ok := pickItems(items, in.VideoIDs, nil)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown video id")
		return
	}
	if len(picked) == 0 {
		writeError(w, http.StatusBadRequest, "nothing to import")
		return
	}
	if title == "" {
		var msg string
		if title, msg = validTitle(pl.Title); msg != "" {
			writeError(w, http.StatusBadRequest, msg)
			return
		}
	}
	if pl.ID != "" {
		plID = pl.ID
	}
	plURL := pl.URL
	if plURL == "" {
		plURL = "https://www.youtube.com/playlist?list=" + plID
	}
	videos := make([]store.Video, 0, len(picked))
	for _, it := range picked {
		videos = append(videos, itemVideo(srv.ID, nil, in.DefaultVenue, it, u.DiscordID))
	}
	s, created, err := a.st.CreateSeriesWithVideos(r.Context(), store.Series{
		ServerID: srv.ID, Title: title, Creator: pl.ChannelTitle, URL: plURL,
		DefaultVenue: in.DefaultVenue, YouTubePlaylistID: plID, ThumbnailVideoID: picked[0].ID,
	}, videos)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	if created == nil {
		created = []store.Video{}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"series": s, "videos": created})
}

func (a *API) importIntoSeries(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	var in struct {
		PlaylistURL string   `json:"playlist_url"`
		VideoIDs    []string `json:"video_ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	series, err := a.st.GetSeries(r.Context(), srv.ID, id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	plID := series.YouTubePlaylistID
	if strings.TrimSpace(in.PlaylistURL) != "" {
		if plID, ok = parsePlaylistID(in.PlaylistURL); !ok {
			writeError(w, http.StatusBadRequest, "not a YouTube playlist or show URL")
			return
		}
	}
	if plID == "" {
		writeError(w, http.StatusBadRequest, "playlist_url is required")
		return
	}
	if a.yt == nil {
		writeError(w, http.StatusServiceUnavailable, errYouTubeOff)
		return
	}
	pl, items, err := a.yt.Playlist(r.Context(), plID)
	if err != nil {
		a.youtubeError(w, r, err, "playlist")
		return
	}
	existing, err := a.st.ListVideos(r.Context(), store.VideoFilter{ServerID: srv.ID, SeriesID: series.ID})
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	have := make(map[string]bool, len(existing))
	for _, v := range existing {
		if v.YouTubeVideoID != "" {
			have[v.YouTubeVideoID] = true
		}
	}
	picked, ok := pickItems(items, in.VideoIDs, have)
	if !ok {
		writeError(w, http.StatusBadRequest, "unknown video id")
		return
	}
	added := []store.Video{}
	if len(picked) > 0 {
		videos := make([]store.Video, 0, len(picked))
		for _, it := range picked {
			videos = append(videos, itemVideo(srv.ID, &series.ID, series.DefaultVenue, it, u.DiscordID))
		}
		if added, err = a.st.CreateVideos(r.Context(), videos); err != nil {
			a.storeError(w, r, err)
			return
		}
		if added == nil {
			added = []store.Video{}
		}
	}
	if series.YouTubePlaylistID == "" {
		if pl.ID != "" {
			plID = pl.ID
		}
		if series, err = a.st.UpdateSeries(r.Context(), srv.ID, series.ID, store.SeriesPatch{YouTubePlaylistID: &plID}); err != nil {
			a.storeError(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": series, "videos": added})
}
