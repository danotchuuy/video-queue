package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danotchuuy/video-queue/internal/discord"
	"github.com/danotchuuy/video-queue/internal/store"
	"github.com/danotchuuy/video-queue/internal/youtube"
)

var testNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fakeStore is an in-memory Store. Content is scoped by ServerID like the real one.
type fakeStore struct {
	servers     map[int64]store.Server
	series      map[int64]store.Series
	videos      map[int64]store.Video
	suggestions map[int64]store.Suggestion
	clips       map[int64]store.Clip
	nextID      int64

	lastClipFilter       store.ClipFilter
	lastClipPatch        store.ClipPatch
	lastClipReviewer     string
	lastClipNow          time.Time
	lastVideoFilter      store.VideoFilter
	lastSuggestionFilter store.SuggestionFilter
	lastPatch            store.VideoPatch
	lastServerPatch      store.ServerPatch
	reordered            []int64
	removeCalls          [][]int64
	createdVideos        []store.Video
	err                  error // returned by ListSeries when set
	suggesters           map[string][]store.Suggester
	suggestersErr        error
	suggestersCalls      [][]string
	userGuilds           []store.UserGuild
	users                []store.User
}

func newFake() *fakeStore {
	return &fakeStore{
		servers:     map[int64]store.Server{},
		series:      map[int64]store.Series{},
		videos:      map[int64]store.Video{},
		suggestions: map[int64]store.Suggestion{},
		clips:       map[int64]store.Clip{},
		nextID:      100,
	}
}

func (f *fakeStore) ListUserGuilds(context.Context, string) ([]store.UserGuild, error) {
	return f.userGuilds, nil
}

func (f *fakeStore) ListUsers(context.Context) ([]store.User, error) { return f.users, nil }
func (f *fakeStore) GetUser(_ context.Context, id string) (store.User, error) {
	for _, u := range f.users {
		if u.DiscordID == id {
			return u, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) id() int64 { f.nextID++; return f.nextID }

func (f *fakeStore) ListServers(context.Context) ([]store.Server, error) {
	var out []store.Server
	for _, s := range f.servers {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (f *fakeStore) GetServer(_ context.Context, id int64) (store.Server, error) {
	s, ok := f.servers[id]
	if !ok {
		return s, store.ErrNotFound
	}
	return s, nil
}
func (f *fakeStore) CreateServer(_ context.Context, in store.Server) (store.Server, error) {
	for _, s := range f.servers {
		if s.GuildID == in.GuildID {
			return store.Server{}, store.ErrConflict
		}
	}
	in.ID = f.id()
	f.servers[in.ID] = in
	return in, nil
}
func (f *fakeStore) UpdateServer(_ context.Context, id int64, p store.ServerPatch) (store.Server, error) {
	s, ok := f.servers[id]
	if !ok {
		return s, store.ErrNotFound
	}
	f.lastServerPatch = p
	if p.Name != nil {
		s.Name = *p.Name
	}
	if p.ModRoleIDs != nil {
		s.ModRoleIDs = *p.ModRoleIDs
	}
	if p.ClipEditorRoleIDs != nil {
		s.ClipEditorRoleIDs = *p.ClipEditorRoleIDs
	}
	if p.VideoEditorRoleIDs != nil {
		s.VideoEditorRoleIDs = *p.VideoEditorRoleIDs
	}
	if p.AllowedUserIDs != nil {
		s.AllowedUserIDs = *p.AllowedUserIDs
	}
	if p.SuggestionsChannelID != nil {
		s.SuggestionsChannelID = *p.SuggestionsChannelID
	}
	if p.ClipsChannelID != nil {
		s.ClipsChannelID = *p.ClipsChannelID
	}
	f.servers[id] = s
	return s, nil
}
func (f *fakeStore) DeleteServer(_ context.Context, id int64) error {
	if _, ok := f.servers[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.servers, id)
	return nil
}

func (f *fakeStore) ListSeries(_ context.Context, sid int64) ([]store.Series, error) {
	if f.err != nil {
		return nil, f.err
	}
	var out []store.Series
	for _, s := range f.series {
		if s.ServerID == sid {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeStore) GetSeries(_ context.Context, sid, id int64) (store.Series, error) {
	s, ok := f.series[id]
	if !ok || s.ServerID != sid {
		return store.Series{}, store.ErrNotFound
	}
	return s, nil
}
func (f *fakeStore) CreateSeries(_ context.Context, in store.Series) (store.Series, error) {
	in.ID = f.id()
	f.series[in.ID] = in
	return in, nil
}
func (f *fakeStore) UpdateSeries(ctx context.Context, sid, id int64, p store.SeriesPatch) (store.Series, error) {
	s, err := f.GetSeries(ctx, sid, id)
	if err != nil {
		return s, err
	}
	if p.Title != nil {
		s.Title = *p.Title
	}
	if p.DefaultVenue != nil {
		s.DefaultVenue = *p.DefaultVenue
	}
	if p.URL != nil {
		s.URL = *p.URL
	}
	if p.YouTubePlaylistID != nil {
		s.YouTubePlaylistID = *p.YouTubePlaylistID
	}
	f.series[id] = s
	return s, nil
}
func (f *fakeStore) DeleteSeries(ctx context.Context, sid, id int64) error {
	if _, err := f.GetSeries(ctx, sid, id); err != nil {
		return err
	}
	delete(f.series, id)
	return nil
}
func (f *fakeStore) ListVideos(_ context.Context, fl store.VideoFilter) ([]store.Video, error) {
	f.lastVideoFilter = fl
	var out []store.Video
	for _, v := range f.videos {
		if v.ServerID == fl.ServerID && (fl.SeriesID == 0 || v.SeriesID != nil && *v.SeriesID == fl.SeriesID) {
			out = append(out, v)
		}
	}
	return out, nil
}
func (f *fakeStore) GetVideo(_ context.Context, sid, id int64) (store.Video, error) {
	v, ok := f.videos[id]
	if !ok || v.ServerID != sid {
		return store.Video{}, store.ErrNotFound
	}
	return v, nil
}
func (f *fakeStore) CreateVideo(_ context.Context, in store.Video) (store.Video, error) {
	in.ID = f.id()
	f.videos[in.ID] = in
	return in, nil
}
func (f *fakeStore) UpdateVideo(ctx context.Context, sid, id int64, p store.VideoPatch) (store.Video, error) {
	v, err := f.GetVideo(ctx, sid, id)
	if err != nil {
		return v, err
	}
	f.lastPatch = p
	if p.Title != nil {
		v.Title = *p.Title
	}
	if p.URL != nil {
		v.URL = *p.URL
	}
	if p.YouTubeVideoID != nil {
		v.YouTubeVideoID = *p.YouTubeVideoID
	}
	if p.ChannelTitle != nil {
		v.ChannelTitle = *p.ChannelTitle
	}
	if p.DurationSeconds != nil {
		v.DurationSeconds = *p.DurationSeconds
	}
	if p.Status != nil {
		v.Status = *p.Status
	}
	if p.EditorID != nil {
		v.EditorID = *p.EditorID
	}
	if p.EditorName != nil {
		v.EditorName = *p.EditorName
	}
	if p.ReactedAt != nil {
		v.ReactedAt = p.ReactedAt
	}
	if p.PatreonAt != nil {
		v.PatreonAt = p.PatreonAt
	}
	if p.YouTubeAt != nil {
		v.YouTubeAt = p.YouTubeAt
	}
	f.videos[id] = v
	return v, nil
}
func (f *fakeStore) CreateVideos(ctx context.Context, vs []store.Video) ([]store.Video, error) {
	f.createdVideos = append(f.createdVideos, vs...)
	out := make([]store.Video, 0, len(vs))
	for _, v := range vs {
		v, _ = f.CreateVideo(ctx, v)
		out = append(out, v)
	}
	return out, nil
}
func (f *fakeStore) CreateSeriesWithVideos(ctx context.Context, s store.Series, vs []store.Video) (store.Series, []store.Video, error) {
	s, _ = f.CreateSeries(ctx, s)
	for i := range vs {
		vs[i].SeriesID = &s.ID
	}
	out, err := f.CreateVideos(ctx, vs)
	return s, out, err
}
func (f *fakeStore) DeleteVideo(ctx context.Context, sid, id int64) error {
	if _, err := f.GetVideo(ctx, sid, id); err != nil {
		return err
	}
	delete(f.videos, id)
	return nil
}
func (f *fakeStore) ReorderVideos(_ context.Context, sid int64, ids []int64) error {
	f.reordered = ids
	return nil
}
func (f *fakeStore) ListSuggestions(_ context.Context, fl store.SuggestionFilter) ([]store.Suggestion, error) {
	f.lastSuggestionFilter = fl
	var out []store.Suggestion
	for _, s := range f.suggestions {
		if s.ServerID == fl.ServerID {
			out = append(out, s)
		}
	}
	return out, nil
}
func (f *fakeStore) SuggestersByVideo(_ context.Context, _ int64, ids []string) (map[string][]store.Suggester, error) {
	f.suggestersCalls = append(f.suggestersCalls, ids)
	if f.suggestersErr != nil {
		return nil, f.suggestersErr
	}
	out := map[string][]store.Suggester{}
	for _, id := range ids {
		if l, ok := f.suggesters[id]; ok {
			out[id] = l
		}
	}
	return out, nil
}
func (f *fakeStore) GetSuggestion(_ context.Context, sid, id int64) (store.Suggestion, error) {
	s, ok := f.suggestions[id]
	if !ok || s.ServerID != sid {
		return store.Suggestion{}, store.ErrNotFound
	}
	return s, nil
}
func (f *fakeStore) SetSuggestionStatus(ctx context.Context, sid, id int64, st store.SuggestionStatus, reviewer string, now time.Time) (store.Suggestion, error) {
	s, err := f.GetSuggestion(ctx, sid, id)
	if err != nil {
		return s, err
	}
	s.Status = st
	s.ReviewedBy = reviewer
	f.suggestions[id] = s
	return s, nil
}
func (f *fakeStore) ApproveSuggestion(ctx context.Context, sid, id int64, v store.Video, reviewer string, now time.Time) (store.Suggestion, store.Video, error) {
	s, err := f.GetSuggestion(ctx, sid, id)
	if err != nil {
		return s, v, err
	}
	v, _ = f.CreateVideo(ctx, v)
	s.Status = store.SuggestionApproved
	s.ReviewedBy = reviewer
	s.VideoID = &v.ID
	f.suggestions[id] = s
	return s, v, nil
}
func (f *fakeStore) RemoveSuggestions(_ context.Context, sid int64, ids []int64, now time.Time) (int64, error) {
	f.removeCalls = append(f.removeCalls, ids)
	var n int64
	for _, id := range ids {
		if s, ok := f.suggestions[id]; ok && s.ServerID == sid && s.RemovedAt == nil {
			s.RemovedAt = &now
			f.suggestions[id] = s
			n++
		}
	}
	return n, nil
}
func (f *fakeStore) UnremoveSuggestion(_ context.Context, sid, id int64) error {
	s, ok := f.suggestions[id]
	if !ok || s.ServerID != sid {
		return store.ErrNotFound
	}
	s.RemovedAt = nil
	f.suggestions[id] = s
	return nil
}

func (f *fakeStore) ListClips(_ context.Context, fl store.ClipFilter) ([]store.Clip, error) {
	f.lastClipFilter = fl
	var out []store.Clip
	for _, c := range f.clips {
		if c.ServerID == fl.ServerID {
			out = append(out, c)
		}
	}
	return out, nil
}
func (f *fakeStore) GetClip(_ context.Context, sid, id int64) (store.Clip, error) {
	c, ok := f.clips[id]
	if !ok || c.ServerID != sid {
		return store.Clip{}, store.ErrNotFound
	}
	return c, nil
}
func (f *fakeStore) UpdateClip(ctx context.Context, sid, id int64, p store.ClipPatch, reviewer string, now time.Time) (store.Clip, error) {
	c, err := f.GetClip(ctx, sid, id)
	if err != nil {
		return c, err
	}
	f.lastClipPatch, f.lastClipReviewer, f.lastClipNow = p, reviewer, now
	if p.Status != nil {
		c.Status = *p.Status
	}
	if p.EditedTitle != nil {
		c.EditedTitle = *p.EditedTitle
	}
	if p.EditedURL != nil {
		c.EditedURL = *p.EditedURL
	}
	if p.YouTubeURL != nil {
		c.YouTubeURL = *p.YouTubeURL
	}
	if p.Notes != nil {
		c.Notes = *p.Notes
	}
	c.ReviewedBy = reviewer
	f.clips[id] = c
	return c, nil
}

// fakeAccess: admins by user ID; others may access servers listed in allow
// and manage those listed in manage (manage implies access, as in the real Checker).
type fakeAccess struct {
	admins      map[string]bool
	allow       map[int64]bool
	manage      map[int64]bool
	errFor      map[int64]error
	manageGuild map[string]bool // guild ID -> user manages it
	manageErr   error
	userErr     map[string]error // per-user Check errors
	userDeny    map[string]bool  // per-user: no access
	userPerms   map[string]Perms // per-user exact perms (overrides allow/manage)
}

func (a *fakeAccess) IsAdmin(id string) bool { return a.admins[id] }
func (a *fakeAccess) Check(_ context.Context, id string, srv store.Server) (Perms, error) {
	if err := a.userErr[id]; err != nil {
		return Perms{}, err
	}
	if a.userDeny[id] {
		return Perms{}, nil
	}
	if err := a.errFor[srv.ID]; err != nil {
		return Perms{}, err
	}
	if a.admins[id] {
		return Perms{Access: true, Full: true, Manage: true, Videos: true, Clips: true}, nil
	}
	if p, ok := a.userPerms[id]; ok {
		return p, nil
	}
	full := a.allow[srv.ID] || a.manage[srv.ID]
	return Perms{Access: full, Full: full, Manage: a.manage[srv.ID], Videos: full, Clips: full}, nil
}
func (a *fakeAccess) CanManageGuild(_ context.Context, id, guildID string) (bool, error) {
	if a.manageErr != nil {
		return false, a.manageErr
	}
	return a.admins[id] || a.manageGuild[guildID], nil
}

type fakeBot struct {
	guilds   []discord.Guild
	roles    []discord.Role
	channels []discord.Channel
	err      error
	guildErr error
}

func (b *fakeBot) BotGuilds(context.Context) ([]discord.Guild, error) { return b.guilds, b.err }
func (b *fakeBot) Guild(_ context.Context, id string) (discord.Guild, error) {
	if b.guildErr != nil {
		return discord.Guild{}, b.guildErr
	}
	for _, g := range b.guilds {
		if g.ID == id {
			return g, nil
		}
	}
	return discord.Guild{}, discord.ErrNotFound
}
func (b *fakeBot) GuildRoles(context.Context, string) ([]discord.Role, error) { return b.roles, b.err }
func (b *fakeBot) GuildTextChannels(context.Context, string) ([]discord.Channel, error) {
	return b.channels, b.err
}

var me = store.User{DiscordID: "u1", Username: "mod"}

const (
	sid1   = 1
	sid2   = 2
	base   = "/api/servers/1"
	guild1 = "100000000000000001"
	guild2 = "100000000000000002"
	chan1  = "200000000000000001"
)

type env struct {
	*fakeStore
	acc *fakeAccess
	bot *fakeBot
	api *API
	h   http.Handler
}

// setup builds an API where the signed-in user ("u1", not an admin) can access
// server 1 but not server 2.
func setup(t *testing.T, sync Syncer) *env {
	t.Helper()
	orig := currentUser
	currentUser = func(context.Context) (store.User, bool) { return me, true }
	t.Cleanup(func() { currentUser = orig })

	origV, origP := parseVideoID, parsePlaylistID
	parseVideoID, parsePlaylistID = fakeParseVideoID, fakeParsePlaylistID
	t.Cleanup(func() { parseVideoID, parsePlaylistID = origV, origP })

	fs := newFake()
	fs.servers[sid1] = store.Server{ID: sid1, GuildID: guild1, Name: "One", SuggestionsChannelID: chan1}
	fs.servers[sid2] = store.Server{ID: sid2, GuildID: guild2, Name: "Two"}
	acc := &fakeAccess{admins: map[string]bool{}, allow: map[int64]bool{sid1: true}, manage: map[int64]bool{},
		errFor: map[int64]error{}, manageGuild: map[string]bool{}}
	bot := &fakeBot{
		guilds:   []discord.Guild{{ID: guild1, Name: "Guild One", Icon: "ic1"}, {ID: "100000000000000003", Name: "Guild Three", Icon: "ic3"}},
		channels: []discord.Channel{{ID: chan1, Name: "suggestions"}},
	}
	a := New(Deps{Store: fs, Access: acc, Bot: bot, Sync: sync, ClientID: "app123",
		SelfRegistration: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	a.now = func() time.Time { return testNow }
	mux := http.NewServeMux()
	a.Register(mux, func(h http.Handler) http.Handler { return h })
	return &env{fakeStore: fs, acc: acc, bot: bot, api: a, h: mux}
}

// asAdmin makes the signed-in user an app admin.
func (e *env) asAdmin() { e.acc.admins[me.DiscordID] = true }

func do(h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status = %d, want %d; body %s", rec.Code, want, rec.Body.String())
	}
}

func TestSeriesCRUD(t *testing.T) {
	e := setup(t, nil)
	h := e.h

	rec := do(h, "POST", base+"/series", `{"title":"  Abridged  ","default_venue":"stream"}`)
	wantStatus(t, rec, 201)
	s := decode[store.Series](t, rec)
	if s.Title != "Abridged" || s.DefaultVenue != store.VenueStream {
		t.Fatalf("series = %+v", s)
	}

	// Default venue when omitted.
	rec = do(h, "POST", base+"/series", `{"title":"Other"}`)
	wantStatus(t, rec, 201)
	if got := decode[store.Series](t, rec); got.DefaultVenue != store.VenueWatchParty {
		t.Errorf("default venue = %q", got.DefaultVenue)
	}

	rec = do(h, "GET", base+"/series", "")
	wantStatus(t, rec, 200)
	if got := decode[[]store.Series](t, rec); len(got) != 2 {
		t.Errorf("len = %d", len(got))
	}

	rec = do(h, "PATCH", base+"/series/"+itoa(s.ID), `{"title":"New"}`)
	wantStatus(t, rec, 200)
	if got := decode[store.Series](t, rec); got.Title != "New" {
		t.Errorf("title = %q", got.Title)
	}

	wantStatus(t, do(h, "DELETE", base+"/series/"+itoa(s.ID), ""), 204)
	wantStatus(t, do(h, "DELETE", base+"/series/"+itoa(s.ID), ""), 404)
	wantStatus(t, do(h, "PATCH", base+"/series/999", `{"title":"x"}`), 404)
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestSeriesValidation(t *testing.T) {
	e := setup(t, nil)
	h := e.h
	long := strings.Repeat("é", 301)
	for _, tc := range []struct{ name, method, path, body string }{
		{"empty title", "POST", base + "/series", `{"title":"  "}`},
		{"long title", "POST", base + "/series", `{"title":"` + long + `"}`},
		{"bad venue", "POST", base + "/series", `{"title":"a","default_venue":"tv"}`},
		{"bad json", "POST", base + "/series", `{`},
		{"unknown field", "POST", base + "/series", `{"title":"a","bogus":1}`},
		{"patch bad venue", "PATCH", base + "/series/1", `{"default_venue":"tv"}`},
		{"patch empty title", "PATCH", base + "/series/1", `{"title":""}`},
		{"bad id", "PATCH", base + "/series/abc", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(h, tc.method, tc.path, tc.body)
			wantStatus(t, rec, 400)
			if decode[map[string]string](t, rec)["error"] == "" {
				t.Error("missing error message")
			}
		})
	}
	// 300 multibyte runes is fine.
	wantStatus(t, do(h, "POST", base+"/series", `{"title":"`+strings.Repeat("é", 300)+`"}`), 201)
}

func TestBodyTooLarge(t *testing.T) {
	e := setup(t, nil)
	h := e.h
	body := `{"title":"` + strings.Repeat("a", 2<<20) + `"}`
	wantStatus(t, do(h, "POST", base+"/series", body), 400)
}

func TestCreateVideo(t *testing.T) {
	fs := setup(t, nil)
	h := fs.h
	s, _ := fs.CreateSeries(context.Background(), store.Series{ServerID: sid1, Title: "S", DefaultVenue: store.VenueStream})

	// Venue defaults from series.
	rec := do(h, "POST", base+"/videos", `{"title":" Ep 1 ","series_id":`+itoa(s.ID)+`,"episode":"1"}`)
	wantStatus(t, rec, 201)
	v := decode[store.Video](t, rec)
	if v.Venue != store.VenueStream || v.Status != store.StatusQueued || v.CreatedBy != "u1" || v.Title != "Ep 1" {
		t.Errorf("video = %+v", v)
	}

	// Explicit venue wins.
	rec = do(h, "POST", base+"/videos", `{"title":"x","series_id":`+itoa(s.ID)+`,"venue":"watch_party"}`)
	wantStatus(t, rec, 201)
	if got := decode[store.Video](t, rec); got.Venue != store.VenueWatchParty {
		t.Errorf("venue = %q", got.Venue)
	}

	// No series: venue required.
	wantStatus(t, do(h, "POST", base+"/videos", `{"title":"x","venue":"stream"}`), 201)
	for name, body := range map[string]string{
		"no venue":       `{"title":"x"}`,
		"bad venue":      `{"title":"x","venue":"tv"}`,
		"no title":       `{"venue":"stream"}`,
		"unknown series": `{"title":"x","venue":"stream","series_id":12345}`,
		"bad time":       `{"title":"x","venue":"stream","scheduled_for":"soon"}`,
	} {
		t.Run(name, func(t *testing.T) { wantStatus(t, do(h, "POST", base+"/videos", body), 400) })
	}
}

func TestVideoGetDeleteReorder(t *testing.T) {
	fs := setup(t, nil)
	h := fs.h
	v, _ := fs.CreateVideo(context.Background(), store.Video{ServerID: sid1, Title: "a"})
	p := base + "/videos/" + itoa(v.ID)

	rec := do(h, "GET", p, "")
	wantStatus(t, rec, 200)
	if decode[store.Video](t, rec).Title != "a" {
		t.Error("wrong video")
	}
	wantStatus(t, do(h, "GET", base+"/videos/999", ""), 404)

	wantStatus(t, do(h, "POST", base+"/videos/reorder", `{"ids":[3,1,2]}`), 204)
	if len(fs.reordered) != 3 || fs.reordered[0] != 3 {
		t.Errorf("reordered = %v", fs.reordered)
	}
	wantStatus(t, do(h, "POST", base+"/videos/reorder", `{"ids":[]}`), 400)
	wantStatus(t, do(h, "POST", base+"/videos/reorder", `{}`), 400)
	wantStatus(t, do(h, "POST", base+"/videos/reorder", `{"ids":[1,2,1]}`), 400)

	wantStatus(t, do(h, "DELETE", p, ""), 204)
	wantStatus(t, do(h, "DELETE", p, ""), 404)
}

func TestListVideosQuery(t *testing.T) {
	fs := setup(t, nil)
	h := fs.h
	rec := do(h, "GET", base+"/videos?status=queued,reacted&venue=stream&series_id=3", "")
	wantStatus(t, rec, 200)
	if rec.Body.String() != "[]\n" {
		t.Errorf("empty list body = %q", rec.Body.String())
	}
	f := fs.lastVideoFilter
	if len(f.Status) != 2 || f.Status[0] != store.StatusQueued || f.Status[1] != store.StatusReacted ||
		f.Venue != store.VenueStream || f.SeriesID != 3 {
		t.Errorf("filter = %+v", f)
	}
	for _, q := range []string{"status=nope", "status=queued,nope", "venue=tv", "series_id=x", "series_id=0"} {
		wantStatus(t, do(h, "GET", base+"/videos?"+q, ""), 400)
	}
}

func TestPatchVideoTimestamps(t *testing.T) {
	existing := time.Date(2025, 5, 5, 0, 0, 0, 0, time.UTC)
	custom := time.Date(2025, 6, 6, 0, 0, 0, 0, time.UTC)

	tests := []struct {
		name   string
		video  store.Video
		body   string
		field  func(store.VideoPatch) *time.Time
		wantTS *time.Time // nil means the patch must not set it
	}{
		{"reacted stamps", store.Video{}, `{"status":"reacted"}`, func(p store.VideoPatch) *time.Time { return p.ReactedAt }, &testNow},
		{"patreon stamps", store.Video{}, `{"status":"patreon"}`, func(p store.VideoPatch) *time.Time { return p.PatreonAt }, &testNow},
		{"youtube stamps", store.Video{}, `{"status":"youtube"}`, func(p store.VideoPatch) *time.Time { return p.YouTubeAt }, &testNow},
		{"keeps existing", store.Video{ReactedAt: &existing}, `{"status":"reacted"}`, func(p store.VideoPatch) *time.Time { return p.ReactedAt }, nil},
		{"keeps explicit", store.Video{}, `{"status":"reacted","reacted_at":"2025-06-06T00:00:00Z"}`, func(p store.VideoPatch) *time.Time { return p.ReactedAt }, &custom},
		{"editing no stamp", store.Video{}, `{"status":"editing"}`, func(p store.VideoPatch) *time.Time { return p.ReactedAt }, nil},
		{"no status no stamp", store.Video{}, `{"title":"t"}`, func(p store.VideoPatch) *time.Time { return p.ReactedAt }, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fs := setup(t, nil)
			h := fs.h
			v, _ := fs.CreateVideo(context.Background(), withServer(tc.video))
			wantStatus(t, do(h, "PATCH", base+"/videos/"+itoa(v.ID), tc.body), 200)
			got := tc.field(fs.lastPatch)
			switch {
			case tc.wantTS == nil && got != nil:
				t.Errorf("timestamp set to %v, want unset", got)
			case tc.wantTS != nil && (got == nil || !got.Equal(*tc.wantTS)):
				t.Errorf("timestamp = %v, want %v", got, tc.wantTS)
			}
		})
	}
}

func TestPatchVideoValidation(t *testing.T) {
	fs := setup(t, nil)
	h := fs.h
	v, _ := fs.CreateVideo(context.Background(), store.Video{ServerID: sid1, Title: "a"})
	p := base + "/videos/" + itoa(v.ID)
	for name, body := range map[string]string{
		"bad status":     `{"status":"nope"}`,
		"bad venue":      `{"venue":"tv"}`,
		"empty title":    `{"title":" "}`,
		"unknown series": `{"series_id":777}`,
		"both series":    `{"series_id":1,"clear_series":true}`,
		"bad json":       `[`,
	} {
		t.Run(name, func(t *testing.T) { wantStatus(t, do(h, "PATCH", p, body), 400) })
	}
	wantStatus(t, do(h, "PATCH", base+"/videos/999", `{"title":"x"}`), 404)

	s, _ := fs.CreateSeries(context.Background(), store.Series{ServerID: sid1, Title: "S"})
	wantStatus(t, do(h, "PATCH", p, `{"series_id":`+itoa(s.ID)+`,"title":"b"}`), 200)
}

func TestSuggestionsList(t *testing.T) {
	fs := setup(t, nil)
	h := fs.h
	wantStatus(t, do(h, "GET", base+"/suggestions", ""), 200)
	if fs.lastSuggestionFilter.Sort != store.SortNewest || fs.lastSuggestionFilter.Status != "" {
		t.Errorf("filter = %+v", fs.lastSuggestionFilter)
	}
	wantStatus(t, do(h, "GET", base+"/suggestions?status=pending&sort=votes", ""), 200)
	if fs.lastSuggestionFilter.Sort != store.SortVotes || fs.lastSuggestionFilter.Status != store.SuggestionPending {
		t.Errorf("filter = %+v", fs.lastSuggestionFilter)
	}
	wantStatus(t, do(h, "GET", base+"/suggestions?status=x", ""), 400)
	wantStatus(t, do(h, "GET", base+"/suggestions?sort=x", ""), 400)
}

func TestSuggestionsAuthorFilter(t *testing.T) {
	fs := setup(t, nil)
	h := fs.h
	wantStatus(t, do(h, "GET", base+"/suggestions", ""), 200)
	if fs.lastSuggestionFilter.AuthorID != "" {
		t.Errorf("filter = %+v", fs.lastSuggestionFilter)
	}
	const id = "123456789012345678"
	wantStatus(t, do(h, "GET", base+"/suggestions?author_id="+id, ""), 200)
	if fs.lastSuggestionFilter.AuthorID != id {
		t.Errorf("filter = %+v", fs.lastSuggestionFilter)
	}
	fs.lastSuggestionFilter = store.SuggestionFilter{}
	for _, bad := range []string{"abc", "1234567890123456", "123456789012345678901", "12345678901234567x"} {
		rec := do(h, "GET", base+"/suggestions?author_id="+bad, "")
		wantStatus(t, rec, 400)
		if !strings.Contains(rec.Body.String(), "invalid author_id") {
			t.Errorf("body = %s", rec.Body.String())
		}
	}
	if fs.lastSuggestionFilter.AuthorID != "" {
		t.Errorf("store called despite 400: %+v", fs.lastSuggestionFilter)
	}
}

func TestSuggestionsSuggesters(t *testing.T) {
	e := setup(t, nil)
	a := store.Suggester{AuthorID: "111111111111111111", AuthorName: "A", FirstAt: testNow}
	b := store.Suggester{AuthorID: "222222222222222222", AuthorName: "B", FirstAt: testNow.Add(time.Hour)}
	e.suggesters = map[string][]store.Suggester{"vidAAAAAAAA": {a, b}}
	for _, v := range []string{"vidAAAAAAAA", "vidBBBBBBBB", ""} {
		x := store.Suggestion{ID: e.id(), ServerID: sid1, YouTubeVideoID: v, Content: "c"}
		e.suggestions[x.ID] = x
	}
	rec := do(e.h, "GET", base+"/suggestions", "")
	wantStatus(t, rec, 200)
	if len(e.suggestersCalls) != 1 {
		t.Fatalf("SuggestersByVideo calls = %v; want 1", e.suggestersCalls)
	}
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 3 {
		t.Fatalf("items = %d", len(raw))
	}
	for _, it := range raw {
		var vid string
		json.Unmarshal(it["youtube_video_id"], &vid) // flat suggestion field
		if _, ok := it["id"]; !ok {
			t.Errorf("not flat: %v", it)
		}
		var got []store.Suggester
		if string(it["suggesters"]) == "null" || json.Unmarshal(it["suggesters"], &got) != nil {
			t.Fatalf("suggesters = %s", it["suggesters"])
		}
		if vid == "vidAAAAAAAA" {
			if len(got) != 2 || got[0].AuthorID != a.AuthorID || got[1].AuthorID != b.AuthorID {
				t.Errorf("suggesters = %+v", got)
			}
		} else if len(got) != 0 {
			t.Errorf("video %q suggesters = %+v; want []", vid, got)
		}
	}
}

func TestSuggestionsSuggestersSkipsStoreWithoutVideoIDs(t *testing.T) {
	e := setup(t, nil)
	addSuggestion(e, store.SuggestionPending)
	rec := do(e.h, "GET", base+"/suggestions", "")
	wantStatus(t, rec, 200)
	if len(e.suggestersCalls) != 0 {
		t.Errorf("SuggestersByVideo called: %v", e.suggestersCalls)
	}
	if !strings.Contains(rec.Body.String(), `"suggesters":[]`) {
		t.Errorf("body = %s", rec.Body.String())
	}
	e = setup(t, nil)
	wantStatus(t, do(e.h, "GET", base+"/suggestions", ""), 200)
	if len(e.suggestersCalls) != 0 {
		t.Errorf("calls = %v", e.suggestersCalls)
	}
}

func TestSuggestionsSuggestersError(t *testing.T) {
	e := setup(t, nil)
	x := store.Suggestion{ID: e.id(), ServerID: sid1, YouTubeVideoID: "vidAAAAAAAA"}
	e.suggestions[x.ID] = x
	e.suggestersErr = errors.New("boom")
	wantStatus(t, do(e.h, "GET", base+"/suggestions", ""), 500)
}

func addSuggestion(fs *env, st store.SuggestionStatus) store.Suggestion {
	s := store.Suggestion{ID: fs.id(), ServerID: sid1, Status: st, Content: "https://example.com"}
	fs.suggestions[s.ID] = s
	return s
}

func TestApproveSuggestion(t *testing.T) {
	fs := setup(t, nil)
	h := fs.h
	series, _ := fs.CreateSeries(context.Background(), store.Series{ServerID: sid1, Title: "S", DefaultVenue: store.VenueStream})
	sg := addSuggestion(fs, store.SuggestionPending)
	p := base + "/suggestions/" + itoa(sg.ID) + "/approve"

	// Validation errors leave the suggestion pending.
	wantStatus(t, do(h, "POST", p, `{"venue":"stream"}`), 400)
	wantStatus(t, do(h, "POST", p, `{"title":"x"}`), 400)
	wantStatus(t, do(h, "POST", p, `{"title":"x","series_id":999}`), 400)

	rec := do(h, "POST", p, `{"title":"Vid","series_id":`+itoa(series.ID)+`}`)
	wantStatus(t, rec, 200)
	var resp struct {
		Suggestion store.Suggestion `json:"suggestion"`
		Video      store.Video      `json:"video"`
	}
	resp = decode[struct {
		Suggestion store.Suggestion `json:"suggestion"`
		Video      store.Video      `json:"video"`
	}](t, rec)
	if resp.Suggestion.Status != store.SuggestionApproved || resp.Suggestion.ReviewedBy != "u1" ||
		resp.Video.Venue != store.VenueStream || resp.Video.CreatedBy != "u1" || resp.Video.Status != store.StatusQueued {
		t.Errorf("resp = %+v", resp)
	}

	// Already approved.
	wantStatus(t, do(h, "POST", p, `{"title":"Vid","venue":"stream"}`), 409)
	wantStatus(t, do(h, "POST", base+"/suggestions/999/approve", `{"title":"x","venue":"stream"}`), 404)
}

func TestDiscardRestore(t *testing.T) {
	fs := setup(t, nil)
	h := fs.h
	sg := addSuggestion(fs, store.SuggestionPending)
	sp := base + "/suggestions/" + itoa(sg.ID)

	rec := do(h, "POST", sp+"/discard", "")
	wantStatus(t, rec, 200)
	if got := decode[store.Suggestion](t, rec); got.Status != store.SuggestionDiscarded || got.ReviewedBy != "u1" {
		t.Errorf("got %+v", got)
	}
	rec = do(h, "POST", sp+"/restore", "")
	wantStatus(t, rec, 200)
	if got := decode[store.Suggestion](t, rec); got.Status != store.SuggestionPending {
		t.Errorf("got %+v", got)
	}

	approved := addSuggestion(fs, store.SuggestionApproved)
	ab := base + "/suggestions/" + itoa(approved.ID)
	wantStatus(t, do(h, "POST", ab+"/discard", ""), 409)
	wantStatus(t, do(h, "POST", ab+"/restore", ""), 409)
	wantStatus(t, do(h, "POST", base+"/suggestions/999/discard", ""), 404)
	wantStatus(t, do(h, "POST", base+"/suggestions/999/restore", ""), 404)
}

func TestRemoveSuggestions(t *testing.T) {
	e := setup(t, nil)
	h := e.h
	a := addSuggestion(e, store.SuggestionPending)
	b := addSuggestion(e, store.SuggestionApproved)
	foreign := store.Suggestion{ID: e.id(), ServerID: sid2, Status: store.SuggestionPending}
	e.suggestions[foreign.ID] = foreign
	p := base + "/suggestions/remove"

	for _, body := range []string{
		``, `{}`, `{"ids":[]}`, `{"ids":[0]}`, `{"ids":[1,-2]}`, `{"ids":["x"]}`, `{"ids":[1],"x":1}`,
		`{"ids":[` + strings.TrimSuffix(strings.Repeat("1,", 501), ",") + `]}`,
	} {
		wantStatus(t, do(h, "POST", p, body), 400)
	}
	if len(e.removeCalls) != 0 {
		t.Fatalf("store called on invalid input: %v", e.removeCalls)
	}

	// 500 ids is allowed; duplicates collapse before the store call.
	rec := do(h, "POST", p, fmt.Sprintf(`{"ids":[%d,%d,%d,%d,%d]}`, a.ID, b.ID, a.ID, foreign.ID, 99999))
	wantStatus(t, rec, 200)
	if got := decode[map[string]int64](t, rec); got["removed"] != 2 {
		t.Errorf("resp = %v", got)
	}
	if len(e.removeCalls) != 1 || !slices.Equal(e.removeCalls[0], []int64{a.ID, b.ID, foreign.ID, 99999}) {
		t.Errorf("store ids = %v", e.removeCalls)
	}
	if e.suggestions[foreign.ID].RemovedAt != nil {
		t.Error("another server's suggestion was removed")
	}
	max := make([]string, 500)
	for i := range max {
		max[i] = itoa(int64(i + 1))
	}
	wantStatus(t, do(h, "POST", p, `{"ids":[`+strings.Join(max, ",")+`]}`), 200)
}

func TestUnremoveSuggestion(t *testing.T) {
	e := setup(t, nil)
	h := e.h
	a := addSuggestion(e, store.SuggestionPending)
	now := testNow
	a.RemovedAt = &now
	e.suggestions[a.ID] = a
	p := base + "/suggestions/" + itoa(a.ID) + "/unremove"
	wantStatus(t, do(h, "POST", p, ""), 204)
	if e.suggestions[a.ID].RemovedAt != nil {
		t.Error("still removed")
	}
	wantStatus(t, do(h, "POST", base+"/suggestions/999/unremove", ""), 404)
	wantStatus(t, do(h, "POST", base+"/suggestions/abc/unremove", ""), 400)
}

func TestReviewRemovedSuggestionIs404(t *testing.T) {
	e := setup(t, nil)
	h := e.h
	now := testNow
	sg := addSuggestion(e, store.SuggestionPending)
	sg.RemovedAt = &now
	e.suggestions[sg.ID] = sg
	sp := base + "/suggestions/" + itoa(sg.ID)
	for _, c := range []struct{ path, body string }{
		{"/approve", `{"title":"x","venue":"stream"}`}, {"/discard", ""}, {"/restore", ""},
	} {
		rec := do(h, "POST", sp+c.path, c.body)
		wantStatus(t, rec, 404)
		if got := decode[map[string]string](t, rec)["error"]; got != "suggestion not found" {
			t.Errorf("%s error = %q", c.path, got)
		}
	}
	if len(e.videos) != 0 || e.suggestions[sg.ID].Status != store.SuggestionPending {
		t.Error("removed suggestion was modified")
	}
}

func withServer(v store.Video) store.Video { v.ServerID = sid1; return v }

func TestMe(t *testing.T) {
	e := setup(t, nil)
	rec := do(e.h, "GET", "/api/me", "")
	wantStatus(t, rec, 200)
	got := decode[struct {
		User    store.User `json:"user"`
		IsAdmin bool       `json:"is_admin"`
		CanReg  bool       `json:"can_register"`
	}](t, rec)
	if got.User.DiscordID != "u1" || got.IsAdmin || !got.CanReg {
		t.Errorf("me = %+v", got)
	}
	if decode[struct {
		Twitch bool `json:"twitch_enabled"`
	}](t, do(e.h, "GET", "/api/me", "")).Twitch {
		t.Error("twitch_enabled without Twitch configured")
	}
	e.api.twitchEnabled = true
	if !decode[struct {
		Twitch bool `json:"twitch_enabled"`
	}](t, do(e.h, "GET", "/api/me", "")).Twitch {
		t.Error("want twitch_enabled")
	}
	e.asAdmin()
	if !decode[struct {
		IsAdmin bool `json:"is_admin"`
	}](t, do(e.h, "GET", "/api/me", "")).IsAdmin {
		t.Error("want is_admin")
	}

	// can_register follows SELF_REGISTRATION, except admins can always register.
	e = setup(t, nil)
	e.api.selfRegistration = false
	if decode[struct {
		CanReg bool `json:"can_register"`
	}](t, do(e.h, "GET", "/api/me", "")).CanReg {
		t.Error("non-admin can_register with self-registration off")
	}
	e.asAdmin()
	if !decode[struct {
		CanReg bool `json:"can_register"`
	}](t, do(e.h, "GET", "/api/me", "")).CanReg {
		t.Error("admin should always can_register")
	}
}

func TestSync(t *testing.T) {
	e := setup(t, nil)
	rec := do(e.h, "POST", base+"/suggestions/sync", "")
	wantStatus(t, rec, 503)
	if got := decode[map[string]string](t, rec)["error"]; got != "suggestion sync is not configured" {
		t.Errorf("error = %q", got)
	}

	var gotSID int64
	e = setup(t, func(_ context.Context, id int64) error { gotSID = id; return nil })
	wantStatus(t, do(e.h, "POST", base+"/suggestions/sync", ""), 204)
	if gotSID != sid1 {
		t.Errorf("synced server %d", gotSID)
	}

	// Server without a channel.
	e.servers[sid1] = store.Server{ID: sid1, GuildID: guild1, Name: "One"}
	wantStatus(t, do(e.h, "POST", base+"/suggestions/sync", ""), 400)

	e = setup(t, func(context.Context, int64) error { return errors.New("secret discord detail") })
	rec = do(e.h, "POST", base+"/suggestions/sync", "")
	wantStatus(t, rec, 502)
	if bytes.Contains(rec.Body.Bytes(), []byte("secret")) {
		t.Error("error detail leaked")
	}
}

func TestInternalErrorIsGeneric(t *testing.T) {
	fs := setup(t, nil)
	fs.err = errors.New("db exploded: secret")
	rec := do(fs.h, "GET", base+"/series", "")
	wantStatus(t, rec, 500)
	if bytes.Contains(rec.Body.Bytes(), []byte("secret")) {
		t.Error("error detail leaked")
	}
}

func TestRequireWraps(t *testing.T) {
	e := setup(t, nil)
	a := New(Deps{Store: e.fakeStore, Access: e.acc, Bot: e.bot})
	mux := http.NewServeMux()
	a.Register(mux, func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })
	})
	wantStatus(t, do(mux, "GET", base+"/videos", ""), 401)
	wantStatus(t, do(mux, "GET", "/api/servers", ""), 401)
}

// sidRoutes lists every {sid} route; %d is the server ID.
var sidRoutes = []struct{ method, path, body string }{
	{"GET", "/api/servers/%d", ""},
	{"PATCH", "/api/servers/%d", `{"name":"x"}`},
	{"DELETE", "/api/servers/%d", ""},
	{"GET", "/api/servers/%d/discord/roles", ""},
	{"GET", "/api/servers/%d/discord/channels", ""},
	{"GET", "/api/servers/%d/series", ""},
	{"POST", "/api/servers/%d/series", `{"title":"a"}`},
	{"PATCH", "/api/servers/%d/series/1", `{"title":"a"}`},
	{"DELETE", "/api/servers/%d/series/1", ""},
	{"GET", "/api/servers/%d/videos", ""},
	{"POST", "/api/servers/%d/videos", `{"title":"a","venue":"stream"}`},
	{"POST", "/api/servers/%d/videos/reorder", `{"ids":[1]}`},
	{"GET", "/api/servers/%d/videos/1", ""},
	{"PATCH", "/api/servers/%d/videos/1", `{"title":"a"}`},
	{"DELETE", "/api/servers/%d/videos/1", ""},
	{"GET", "/api/servers/%d/suggestions", ""},
	{"POST", "/api/servers/%d/suggestions/sync", ""},
	{"POST", "/api/servers/%d/suggestions/1/approve", `{"title":"a","venue":"stream"}`},
	{"POST", "/api/servers/%d/suggestions/1/discard", ""},
	{"POST", "/api/servers/%d/suggestions/1/restore", ""},
	{"POST", "/api/servers/%d/suggestions/remove", `{"ids":[1]}`},
	{"POST", "/api/servers/%d/suggestions/1/unremove", ""},
	{"GET", "/api/servers/%d/clips", ""},
	{"POST", "/api/servers/%d/clips/sync", ""},
	{"GET", "/api/servers/%d/clips/1", ""},
	{"PATCH", "/api/servers/%d/clips/1", `{"notes":"a"}`},
}

func TestNonMemberGets404(t *testing.T) {
	for _, rt := range sidRoutes {
		for _, sid := range []int{sid2, 999} { // no access, nonexistent
			t.Run(fmt.Sprintf("%s %s %d", rt.method, rt.path, sid), func(t *testing.T) {
				e := setup(t, func(context.Context, int64) error { return nil })
				rec := do(e.h, rt.method, fmt.Sprintf(rt.path, sid), rt.body)
				wantStatus(t, rec, 404)
				if got := decode[map[string]string](t, rec)["error"]; got != "server not found" {
					t.Errorf("error = %q", got)
				}
				if _, ok := e.servers[sid2]; !ok {
					t.Error("server 2 was deleted")
				}
			})
		}
	}
	// Admin sees server 2.
	e := setup(t, nil)
	e.asAdmin()
	wantStatus(t, do(e.h, "GET", "/api/servers/2/series", ""), 200)
}

func TestAccessErrorIs502(t *testing.T) {
	e := setup(t, nil)
	e.acc.errFor[sid1] = errors.New("discord down")
	wantStatus(t, do(e.h, "GET", base+"/series", ""), 502)
	wantStatus(t, do(e.h, "GET", "/api/servers/abc/series", ""), 404)
}

func TestManageRoutes(t *testing.T) {
	manageRoutes := []struct{ method, path, body string }{
		{"PATCH", base, `{"name":"x"}`},
		{"DELETE", base, ""},
		{"GET", base + "/discord/roles", ""},
		{"GET", base + "/discord/channels", ""},
	}
	// A mod (access, no manage) is forbidden.
	e := setup(t, nil)
	for _, rt := range manageRoutes {
		t.Run("mod "+rt.method+" "+rt.path, func(t *testing.T) {
			rec := do(e.h, rt.method, rt.path, rt.body)
			wantStatus(t, rec, 403)
			if got := decode[map[string]string](t, rec)["error"]; got != "you can't manage this server" {
				t.Errorf("error = %q", got)
			}
		})
	}
	if e.servers[sid1].Name != "One" {
		t.Error("server was changed")
	}
	// A non-member gets 404 and server 2 is untouched.
	for _, rt := range manageRoutes {
		path := strings.Replace(rt.path, "/1", "/2", 1)
		wantStatus(t, do(e.h, rt.method, path, rt.body), 404)
	}
	if _, ok := e.servers[sid2]; !ok {
		t.Error("server 2 was deleted")
	}
	// A manager who isn't a mod can access content and manage.
	e = setup(t, nil)
	e.acc.allow = map[int64]bool{}
	e.acc.manage[sid1] = true
	wantStatus(t, do(e.h, "GET", base+"/series", ""), 200)
	wantStatus(t, do(e.h, "GET", base+"/discord/roles", ""), 200)
	wantStatus(t, do(e.h, "PATCH", base, `{"name":"x"}`), 200)
	wantStatus(t, do(e.h, "DELETE", base, ""), 204)
}

func TestCrossServerSeriesRejected(t *testing.T) {
	e := setup(t, nil)
	other, _ := e.CreateSeries(context.Background(), store.Series{ServerID: sid2, Title: "Other", DefaultVenue: store.VenueStream})
	v, _ := e.CreateVideo(context.Background(), store.Video{ServerID: sid1, Title: "a"})
	sg := addSuggestion(e, store.SuggestionPending)

	wantStatus(t, do(e.h, "POST", base+"/videos", `{"title":"x","venue":"stream","series_id":`+itoa(other.ID)+`}`), 400)
	wantStatus(t, do(e.h, "PATCH", base+"/videos/"+itoa(v.ID), `{"series_id":`+itoa(other.ID)+`}`), 400)
	rec := do(e.h, "POST", base+"/suggestions/"+itoa(sg.ID)+"/approve", `{"title":"x","series_id":`+itoa(other.ID)+`}`)
	wantStatus(t, rec, 400)
	if got := decode[map[string]string](t, rec)["error"]; got != "unknown series" {
		t.Errorf("error = %q", got)
	}
	// Direct access to another server's rows is a 404 too.
	wantStatus(t, do(e.h, "PATCH", base+"/series/"+itoa(other.ID), `{"title":"x"}`), 404)
	wantStatus(t, do(e.h, "DELETE", base+"/series/"+itoa(other.ID), ""), 404)
	if rec := do(e.h, "GET", base+"/series", ""); len(decode[[]store.Series](t, rec)) != 0 {
		t.Error("listed another server's series")
	}
	do(e.h, "GET", base+"/videos", "")
	if e.lastVideoFilter.ServerID != sid1 {
		t.Errorf("filter server = %d", e.lastVideoFilter.ServerID)
	}
	do(e.h, "GET", base+"/suggestions", "")
	if e.lastSuggestionFilter.ServerID != sid1 {
		t.Errorf("filter server = %d", e.lastSuggestionFilter.ServerID)
	}
}

func TestListServers(t *testing.T) {
	e := setup(t, nil)
	e.servers[3] = store.Server{ID: 3, GuildID: "3", Name: "Three"}
	e.servers[4] = store.Server{ID: 4, GuildID: "4", Name: "Four"}
	e.acc.allow[4] = true
	e.acc.errFor[3] = errors.New("boom")

	rec := do(e.h, "GET", "/api/servers", "")
	wantStatus(t, rec, 200)
	got := decode[[]ServerView](t, rec)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 4 || got[0].CanManage || got[1].CanManage {
		t.Errorf("servers = %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"mod_role_ids":[]`) || strings.Contains(rec.Body.String(), "null") {
		t.Errorf("lists should be []: %s", rec.Body.String())
	}

	e.acc.manage[4] = true
	got = decode[[]ServerView](t, do(e.h, "GET", "/api/servers", ""))
	if len(got) != 2 || got[0].CanManage || !got[1].CanManage {
		t.Errorf("can_manage = %+v", got)
	}

	e.acc.allow = map[int64]bool{}
	e.acc.manage = map[int64]bool{}
	rec = do(e.h, "GET", "/api/servers", "")
	wantStatus(t, rec, 200)
	if rec.Body.String() != "[]\n" {
		t.Errorf("empty body = %q", rec.Body.String())
	}

	e.acc.errFor = map[int64]error{}
	e.asAdmin()
	got = decode[[]ServerView](t, do(e.h, "GET", "/api/servers", ""))
	if len(got) != 4 || !got[0].CanManage {
		t.Errorf("admin sees %+v", got)
	}
}

func TestGetServer(t *testing.T) {
	e := setup(t, nil)
	rec := do(e.h, "GET", base, "")
	wantStatus(t, rec, 200)
	if got := decode[ServerView](t, rec); got.ID != sid1 || got.Name != "One" || got.CanManage {
		t.Errorf("server = %+v", got)
	}
	if !strings.Contains(rec.Body.String(), `"can_manage":false`) || !strings.Contains(rec.Body.String(), `"name":"One"`) {
		t.Errorf("not flat: %s", rec.Body.String())
	}
	e.acc.manage[sid1] = true
	if !decode[ServerView](t, do(e.h, "GET", base, "")).CanManage {
		t.Error("manager should have can_manage")
	}
}

type guildsResp struct {
	InviteURL string `json:"invite_url"`
	Guilds    []struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Icon       string `json:"icon"`
		ServerID   *int64 `json:"server_id"`
		BotPresent bool   `json:"bot_present"`
		InviteURL  string `json:"invite_url"`
	} `json:"guilds"`
}

const inviteBase = "https://discord.com/oauth2/authorize?client_id=app123&permissions=66560&scope=bot"

func TestGuildsAdmin(t *testing.T) {
	e := setup(t, nil)
	e.asAdmin()
	// The admin's own snapshot adds guilds the bot isn't in; duplicates collapse;
	// non-managed guilds are ignored.
	e.userGuilds = []store.UserGuild{
		{GuildID: guild1, Name: "Guild One", CanManage: true},
		{GuildID: "100000000000000009", Name: "alpha", Icon: "ic9", CanManage: true},
		{GuildID: "100000000000000008", Name: "Plain", CanManage: false},
	}
	rec := do(e.h, "GET", "/api/guilds", "")
	wantStatus(t, rec, 200)
	got := decode[guildsResp](t, rec)
	if got.InviteURL != inviteBase {
		t.Errorf("invite = %q", got.InviteURL)
	}
	if len(got.Guilds) != 3 {
		t.Fatalf("guilds = %+v", got.Guilds)
	}
	// Sorted case-insensitively: alpha, Guild One, Guild Three.
	a, g1, g3 := got.Guilds[0], got.Guilds[1], got.Guilds[2]
	if a.Name != "alpha" || a.BotPresent || a.ServerID != nil || a.Icon != "ic9" ||
		a.InviteURL != inviteBase+"&guild_id=100000000000000009&disable_guild_select=true" {
		t.Errorf("alpha = %+v", a)
	}
	if g1.ID != guild1 || !g1.BotPresent || g1.ServerID == nil || *g1.ServerID != sid1 || g1.Icon != "ic1" || g1.InviteURL != "" {
		t.Errorf("g1 = %+v", g1)
	}
	if g3.Name != "Guild Three" || !g3.BotPresent || g3.ServerID != nil || g3.InviteURL != "" {
		t.Errorf("g3 = %+v", g3)
	}

	e.bot.guilds, e.userGuilds = nil, nil
	if rec := do(e.h, "GET", "/api/guilds", ""); !strings.Contains(rec.Body.String(), `"guilds":[]`) {
		t.Errorf("body = %s", rec.Body.String())
	}
	e.bot.err = errors.New("secret")
	rec = do(e.h, "GET", "/api/guilds", "")
	wantStatus(t, rec, 502)
	if bytes.Contains(rec.Body.Bytes(), []byte("secret")) {
		t.Error("error detail leaked")
	}
}

func TestGuildsNonAdmin(t *testing.T) {
	e := setup(t, nil)
	e.userGuilds = []store.UserGuild{
		{GuildID: "100000000000000007", Name: "Zeta", CanManage: true},
		{GuildID: guild1, Name: "Guild One", CanManage: true},
		{GuildID: "100000000000000008", Name: "Not Managed", CanManage: false},
	}
	rec := do(e.h, "GET", "/api/guilds", "")
	wantStatus(t, rec, 200)
	got := decode[guildsResp](t, rec)
	// Only managed snapshot guilds; the bot's other guilds are not exposed.
	if len(got.Guilds) != 2 || got.Guilds[0].ID != guild1 || got.Guilds[1].ID != "100000000000000007" {
		t.Fatalf("guilds = %+v", got.Guilds)
	}
	if !got.Guilds[0].BotPresent || got.Guilds[0].ServerID == nil || got.Guilds[0].InviteURL != "" {
		t.Errorf("g0 = %+v", got.Guilds[0])
	}
	if z := got.Guilds[1]; z.BotPresent || z.InviteURL != inviteBase+"&guild_id=100000000000000007&disable_guild_select=true" {
		t.Errorf("zeta = %+v", z)
	}

	// With self-registration off only admins may list.
	e.api.selfRegistration = false
	wantStatus(t, do(e.h, "GET", "/api/guilds", ""), 403)
	e.asAdmin()
	wantStatus(t, do(e.h, "GET", "/api/guilds", ""), 200)
}

func TestCreateServer(t *testing.T) {
	e := setup(t, nil)
	e.asAdmin()
	newGuild := "100000000000000003"

	// Name defaults to the guild name; metadata from Discord.
	rec := do(e.h, "POST", "/api/servers", `{"guild_id":"`+newGuild+`"}`)
	wantStatus(t, rec, 201)
	s := decode[ServerView](t, rec)
	if !s.CanManage {
		t.Error("created server should have can_manage")
	}
	if s.Name != "Guild Three" || s.GuildName != "Guild Three" || s.Icon != "ic3" || s.GuildID != newGuild || s.CreatedBy != "u1" {
		t.Errorf("server = %+v", s)
	}
	if !strings.Contains(rec.Body.String(), `"mod_role_ids":[]`) {
		t.Errorf("body = %s", rec.Body.String())
	}

	// Duplicate.
	wantStatus(t, do(e.h, "POST", "/api/servers", `{"guild_id":"`+newGuild+`"}`), 409)

	// Explicit name, trimmed.
	e.bot.guilds = append(e.bot.guilds, discord.Guild{ID: guild2, Name: "G2"})
	delete(e.servers, sid2)
	rec = do(e.h, "POST", "/api/servers", `{"guild_id":"`+guild2+`","name":"  Streamer  "}`)
	wantStatus(t, rec, 201)
	if got := decode[ServerView](t, rec); got.Name != "Streamer" {
		t.Errorf("name = %q", got.Name)
	}

	// Bot missing.
	rec = do(e.h, "POST", "/api/servers", `{"guild_id":"199999999999999999"}`)
	wantStatus(t, rec, 400)
	if got := decode[map[string]string](t, rec)["error"]; got != "bot is not in that server" {
		t.Errorf("error = %q", got)
	}

	for name, body := range map[string]string{
		"bad guild id": `{"guild_id":"abc"}`,
		"no guild id":  `{}`,
		"long name":    `{"guild_id":"` + guild1 + `","name":"` + strings.Repeat("a", 101) + `"}`,
		"bad json":     `{`,
	} {
		t.Run(name, func(t *testing.T) { wantStatus(t, do(e.h, "POST", "/api/servers", body), 400) })
	}

	// Other bot failures are upstream errors.
	e.bot.guildErr = errors.New("discord 500")
	wantStatus(t, do(e.h, "POST", "/api/servers", `{"guild_id":"`+guild1+`"}`), 502)
}

func TestCreateServerSelfRegistration(t *testing.T) {
	post := func(e *env, guild string) *httptest.ResponseRecorder {
		return do(e.h, "POST", "/api/servers", `{"guild_id":"`+guild+`"}`)
	}
	g3 := "100000000000000003"

	// Not a manager of that guild: 403.
	e := setup(t, nil)
	rec := post(e, g3)
	wantStatus(t, rec, 403)
	if got := decode[map[string]string](t, rec)["error"]; got != "you need Manage Server on that Discord server to add it" {
		t.Errorf("error = %q", got)
	}

	// A manager registers it.
	e.acc.manageGuild[g3] = true
	rec = post(e, g3)
	wantStatus(t, rec, 201)
	if got := decode[ServerView](t, rec); !got.CanManage || got.CreatedBy != "u1" {
		t.Errorf("server = %+v", got)
	}
	wantStatus(t, post(e, g3), 409)

	// The bot must be in the guild, checked before permissions.
	e = setup(t, nil)
	rec = post(e, "199999999999999999")
	wantStatus(t, rec, 400)

	// Permission check failure is a 502.
	e.acc.manageErr = errors.New("discord down")
	wantStatus(t, post(e, g3), 502)

	// Self-registration off: managers are refused, admins still register.
	e = setup(t, nil)
	e.api.selfRegistration = false
	e.acc.manageGuild[g3] = true
	wantStatus(t, post(e, g3), 403)
	e.asAdmin()
	wantStatus(t, post(e, g3), 201)
}

func TestPatchServer(t *testing.T) {
	e := setup(t, nil)
	e.acc.manage[sid1] = true
	r1, r2 := "300000000000000001", "300000000000000002"

	rec := do(e.h, "PATCH", base, `{"name":"  New  ","mod_role_ids":["`+r1+`","`+r2+`","`+r1+`"],"allowed_user_ids":[],"suggestions_channel_id":"`+chan1+`"}`)
	wantStatus(t, rec, 200)
	got := decode[ServerView](t, rec)
	if got.Name != "New" || len(got.ModRoleIDs) != 2 || got.ModRoleIDs[0] != r1 || got.ModRoleIDs[1] != r2 ||
		got.AllowedUserIDs == nil || got.SuggestionsChannelID != chan1 {
		t.Errorf("server = %+v", got)
	}

	// Disabling syncing with "" needs no Discord lookup.
	e.bot.err = errors.New("discord down")
	rec = do(e.h, "PATCH", base, `{"suggestions_channel_id":""}`)
	wantStatus(t, rec, 200)
	if decode[store.Server](t, rec).SuggestionsChannelID != "" {
		t.Error("channel not cleared")
	}
	wantStatus(t, do(e.h, "PATCH", base, `{"suggestions_channel_id":"`+chan1+`"}`), 502)
	e.bot.err = nil

	for name, body := range map[string]string{
		"empty name":      `{"name":"  "}`,
		"long name":       `{"name":"` + strings.Repeat("a", 101) + `"}`,
		"bad role":        `{"mod_role_ids":["abc"]}`,
		"short role":      `{"mod_role_ids":["1234567890123456"]}`,
		"bad clip role":   `{"clip_editor_role_ids":["abc"]}`,
		"bad video role":  `{"video_editor_role_ids":["1234567890123456"]}`,
		"long user":       `{"allowed_user_ids":["123456789012345678901"]}`,
		"non-digit user":  `{"allowed_user_ids":["12345678901234567x"]}`,
		"unknown channel": `{"suggestions_channel_id":"200000000000000009"}`,
		"bad channel":     `{"suggestions_channel_id":"general"}`,
		"unknown field":   `{"guild_id":"1"}`,
	} {
		t.Run(name, func(t *testing.T) {
			before := e.servers[sid1]
			wantStatus(t, do(e.h, "PATCH", base, body), 400)
			if after := e.servers[sid1]; after.Name != before.Name || after.SuggestionsChannelID != before.SuggestionsChannelID {
				t.Error("server changed despite 400")
			}
		})
	}
}

func TestPatchServerBlockSelfSuggestions(t *testing.T) {
	e := setup(t, nil)
	// A mod without manage can't change it.
	wantStatus(t, do(e.h, "PATCH", base, `{"block_self_suggestions":true}`), 403)
	if e.lastServerPatch.BlockSelfSuggestions != nil {
		t.Error("store patched without manage")
	}
	e.acc.manage[sid1] = true
	wantStatus(t, do(e.h, "PATCH", base, `{"block_self_suggestions":true}`), 200)
	if p := e.lastServerPatch.BlockSelfSuggestions; p == nil || !*p {
		t.Errorf("patch = %+v", e.lastServerPatch)
	}
}

func TestDeleteServer(t *testing.T) {
	e := setup(t, nil)
	e.acc.manage[sid1] = true
	wantStatus(t, do(e.h, "DELETE", base, ""), 204)
	wantStatus(t, do(e.h, "DELETE", base, ""), 404)
}

func TestDiscordLists(t *testing.T) {
	e := setup(t, nil)
	e.acc.manage[sid1] = true
	e.bot.roles = []discord.Role{{ID: "r1", Name: "Mod"}}
	rec := do(e.h, "GET", base+"/discord/roles", "")
	wantStatus(t, rec, 200)
	if got := decode[[]discord.Role](t, rec); len(got) != 1 || got[0].Name != "Mod" {
		t.Errorf("roles = %+v", got)
	}
	rec = do(e.h, "GET", base+"/discord/channels", "")
	wantStatus(t, rec, 200)
	if got := decode[[]discord.Channel](t, rec); len(got) != 1 || got[0].ID != chan1 {
		t.Errorf("channels = %+v", got)
	}
	e.bot.roles, e.bot.channels = nil, nil
	for _, p := range []string{"/discord/roles", "/discord/channels"} {
		if rec := do(e.h, "GET", base+p, ""); rec.Body.String() != "[]\n" {
			t.Errorf("%s body = %q", p, rec.Body.String())
		}
	}
	e.bot.err = errors.New("down")
	wantStatus(t, do(e.h, "GET", base+"/discord/roles", ""), 502)
	wantStatus(t, do(e.h, "GET", base+"/discord/channels", ""), 502)
}

// --- YouTube ---

func fakeParseVideoID(s string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(s), "https://youtu.be/")
	return id, ok && id != ""
}

func fakeParsePlaylistID(s string) (string, bool) {
	id, ok := strings.CutPrefix(strings.TrimSpace(s), "https://www.youtube.com/playlist?list=")
	return id, ok && id != ""
}

type fakeYT struct {
	video    youtube.Video
	playlist youtube.Playlist
	items    []youtube.PlaylistItem
	err      error
	gotID    string
}

func (y *fakeYT) Video(_ context.Context, id string) (youtube.Video, error) {
	y.gotID = id
	return y.video, y.err
}
func (y *fakeYT) Playlist(_ context.Context, id string) (youtube.Playlist, []youtube.PlaylistItem, error) {
	y.gotID = id
	return y.playlist, y.items, y.err
}

func ytItem(id string, pos int) youtube.PlaylistItem {
	return youtube.PlaylistItem{
		Video: youtube.Video{ID: id, Title: "T " + id, ChannelTitle: "Chan", DurationSeconds: int64(pos) * 60,
			URL: "https://www.youtube.com/watch?v=" + id},
		Position: pos,
	}
}

func withYT(e *env) *fakeYT {
	y := &fakeYT{
		video:    youtube.Video{ID: "vid1", Title: "Hello"},
		playlist: youtube.Playlist{ID: "PLabc", Title: "My List", ChannelTitle: "Chan", URL: "https://www.youtube.com/playlist?list=PLabc"},
		items:    []youtube.PlaylistItem{ytItem("a", 1), ytItem("b", 2), ytItem("c", 4)},
	}
	e.api.yt = y
	return y
}

const plURL = "https://www.youtube.com/playlist?list=PLabc"

type importResp struct {
	Series store.Series  `json:"series"`
	Videos []store.Video `json:"videos"`
}

func TestMeYouTubeEnabled(t *testing.T) {
	type resp struct {
		Enabled bool `json:"youtube_enabled"`
	}
	e := setup(t, nil)
	if decode[resp](t, do(e.h, "GET", "/api/me", "")).Enabled {
		t.Error("youtube_enabled without client")
	}
	withYT(e)
	if !decode[resp](t, do(e.h, "GET", "/api/me", "")).Enabled {
		t.Error("want youtube_enabled")
	}
}

func TestYouTubeLookups(t *testing.T) {
	e := setup(t, nil)
	vp := "/api/youtube/video?url=https://youtu.be/vid1"
	pp := "/api/youtube/playlist?url=" + plURL

	wantStatus(t, do(e.h, "GET", "/api/youtube/video?url=nope", ""), 400)
	wantStatus(t, do(e.h, "GET", "/api/youtube/playlist?url=nope", ""), 400)
	wantStatus(t, do(e.h, "GET", vp, ""), 503)
	wantStatus(t, do(e.h, "GET", pp, ""), 503)

	y := withYT(e)
	rec := do(e.h, "GET", vp, "")
	wantStatus(t, rec, 200)
	if got := decode[youtube.Video](t, rec); got.ID != "vid1" || y.gotID != "vid1" {
		t.Errorf("video = %+v gotID %q", got, y.gotID)
	}
	rec = do(e.h, "GET", pp, "")
	wantStatus(t, rec, 200)
	pl := decode[struct {
		Playlist youtube.Playlist       `json:"playlist"`
		Items    []youtube.PlaylistItem `json:"items"`
	}](t, rec)
	if pl.Playlist.ID != "PLabc" || len(pl.Items) != 3 || y.gotID != "PLabc" {
		t.Errorf("playlist = %+v", pl)
	}

	y.err = youtube.ErrNotFound
	rec = do(e.h, "GET", vp, "")
	wantStatus(t, rec, 404)
	if !strings.Contains(rec.Body.String(), "video not found") {
		t.Errorf("body %s", rec.Body)
	}
	rec = do(e.h, "GET", pp, "")
	wantStatus(t, rec, 404)
	if !strings.Contains(rec.Body.String(), "playlist not found") {
		t.Errorf("body %s", rec.Body)
	}
	y.err = errors.New("quota key=SECRET")
	rec = do(e.h, "GET", vp, "")
	wantStatus(t, rec, 502)
	if !strings.Contains(rec.Body.String(), "YouTube request failed") || strings.Contains(rec.Body.String(), "SECRET") {
		t.Errorf("body %s", rec.Body)
	}
	wantStatus(t, do(e.h, "GET", pp, ""), 502)
}

func TestImportNewSeries(t *testing.T) {
	e := setup(t, nil)
	y := withYT(e)
	p := base + "/series/import"

	wantStatus(t, do(e.h, "POST", p, `{"playlist_url":"nope"}`), 400)
	wantStatus(t, do(e.h, "POST", p, `{"playlist_url":"`+plURL+`","default_venue":"x"}`), 400)
	wantStatus(t, do(e.h, "POST", "/api/servers/2/series/import", `{"playlist_url":"`+plURL+`"}`), 404)

	// Full playlist with defaults.
	rec := do(e.h, "POST", p, `{"playlist_url":"`+plURL+`"}`)
	wantStatus(t, rec, 201)
	got := decode[importResp](t, rec)
	if y.gotID != "PLabc" {
		t.Errorf("fetched %q", y.gotID)
	}
	s := got.Series
	if s.Title != "My List" || s.Creator != "Chan" || s.URL != plURL || s.YouTubePlaylistID != "PLabc" ||
		s.ThumbnailVideoID != "a" || s.DefaultVenue != store.VenueWatchParty || s.ServerID != sid1 {
		t.Errorf("series = %+v", s)
	}
	if len(got.Videos) != 3 {
		t.Fatalf("videos = %+v", got.Videos)
	}
	v := got.Videos[2]
	if v.Episode != "4" || v.Title != "T c" || v.YouTubeVideoID != "c" || v.URL != "https://www.youtube.com/watch?v=c" ||
		v.ChannelTitle != "Chan" || v.DurationSeconds != 240 || v.Venue != store.VenueWatchParty ||
		v.Status != store.StatusQueued || v.CreatedBy != "u1" || v.ServerID != sid1 ||
		v.SeriesID == nil || *v.SeriesID != s.ID {
		t.Errorf("video = %+v", v)
	}

	// Subset, own title and venue, duplicates in video_ids, playlist order kept.
	rec = do(e.h, "POST", p, `{"playlist_url":"`+plURL+`","title":" Mine ","default_venue":"stream","video_ids":["c","b","c"]}`)
	wantStatus(t, rec, 201)
	got = decode[importResp](t, rec)
	if got.Series.Title != "Mine" || got.Series.DefaultVenue != store.VenueStream || got.Series.ThumbnailVideoID != "b" ||
		len(got.Videos) != 2 || got.Videos[0].YouTubeVideoID != "b" || got.Videos[1].YouTubeVideoID != "c" ||
		got.Videos[0].Venue != store.VenueStream {
		t.Errorf("got = %+v", got)
	}

	// Not a subset, or nothing to import.
	n := len(e.series)
	rec = do(e.h, "POST", p, `{"playlist_url":"`+plURL+`","video_ids":["a","zzz"]}`)
	wantStatus(t, rec, 400)
	if !strings.Contains(rec.Body.String(), "unknown video id") {
		t.Errorf("body %s", rec.Body)
	}
	wantStatus(t, do(e.h, "POST", p, `{"playlist_url":"`+plURL+`","video_ids":[]}`), 400)
	y.items = nil
	wantStatus(t, do(e.h, "POST", p, `{"playlist_url":"`+plURL+`"}`), 400)
	if len(e.series) != n {
		t.Error("series created on a failed import")
	}

	// Upstream errors and no client.
	y.err = youtube.ErrNotFound
	wantStatus(t, do(e.h, "POST", p, `{"playlist_url":"`+plURL+`"}`), 404)
	y.err = errors.New("boom")
	wantStatus(t, do(e.h, "POST", p, `{"playlist_url":"`+plURL+`"}`), 502)
	e.api.yt = nil
	wantStatus(t, do(e.h, "POST", p, `{"playlist_url":"`+plURL+`"}`), 503)
}

func TestImportIntoSeries(t *testing.T) {
	e := setup(t, nil)
	y := withYT(e)
	ctx := context.Background()
	s, _ := e.CreateSeries(ctx, store.Series{ServerID: sid1, Title: "S", DefaultVenue: store.VenueStream, YouTubePlaylistID: "PLabc"})
	e.CreateVideo(ctx, store.Video{ServerID: sid1, SeriesID: &s.ID, Title: "have a", YouTubeVideoID: "a"})
	other, _ := e.CreateSeries(ctx, store.Series{ServerID: sid2, Title: "O"})
	p := base + "/series/" + itoa(s.ID) + "/import"

	// Default playlist from the series; "a" is skipped.
	rec := do(e.h, "POST", p, `{}`)
	wantStatus(t, rec, 200)
	got := decode[importResp](t, rec)
	if y.gotID != "PLabc" || len(got.Videos) != 2 || got.Videos[0].YouTubeVideoID != "b" || got.Videos[1].YouTubeVideoID != "c" {
		t.Errorf("got = %+v (fetched %q)", got, y.gotID)
	}
	if v := got.Videos[0]; v.Venue != store.VenueStream || v.Episode != "2" || v.SeriesID == nil || *v.SeriesID != s.ID {
		t.Errorf("video = %+v", v)
	}

	// Everything is present now: 200 with an empty list, not null.
	before := len(e.createdVideos)
	rec = do(e.h, "POST", p, `{}`)
	wantStatus(t, rec, 200)
	if !strings.Contains(rec.Body.String(), `"videos":[]`) || len(e.createdVideos) != before {
		t.Errorf("body %s", rec.Body)
	}

	// video_ids filter and non-subset.
	for id, v := range e.videos {
		if v.YouTubeVideoID == "b" {
			delete(e.videos, id)
		}
	}
	rec = do(e.h, "POST", p, `{"video_ids":["b","b"]}`)
	wantStatus(t, rec, 200)
	if got := decode[importResp](t, rec); len(got.Videos) != 1 || got.Videos[0].YouTubeVideoID != "b" {
		t.Errorf("got = %+v", got)
	}
	wantStatus(t, do(e.h, "POST", p, `{"video_ids":["zzz"]}`), 400)
	wantStatus(t, do(e.h, "POST", p, `{"playlist_url":"nope"}`), 400)

	// No playlist anywhere: 400. With a body URL: sets the series' playlist id.
	bare, _ := e.CreateSeries(ctx, store.Series{ServerID: sid1, Title: "B", DefaultVenue: store.VenueWatchParty})
	bp := base + "/series/" + itoa(bare.ID) + "/import"
	wantStatus(t, do(e.h, "POST", bp, `{}`), 400)
	rec = do(e.h, "POST", bp, `{"playlist_url":"`+plURL+`","video_ids":["c"]}`)
	wantStatus(t, rec, 200)
	got = decode[importResp](t, rec)
	if got.Series.YouTubePlaylistID != "PLabc" || e.series[bare.ID].YouTubePlaylistID != "PLabc" ||
		len(got.Videos) != 1 || got.Videos[0].Venue != store.VenueWatchParty {
		t.Errorf("got = %+v", got)
	}

	// An existing playlist id is kept.
	rec = do(e.h, "POST", p, `{"playlist_url":"https://www.youtube.com/playlist?list=PLother"}`)
	wantStatus(t, rec, 200)
	if e.series[s.ID].YouTubePlaylistID != "PLabc" {
		t.Errorf("playlist id = %q", e.series[s.ID].YouTubePlaylistID)
	}

	// Other servers' series and unknown ids are 404.
	wantStatus(t, do(e.h, "POST", base+"/series/"+itoa(other.ID)+"/import", `{}`), 404)
	wantStatus(t, do(e.h, "POST", base+"/series/9999/import", `{}`), 404)
	wantStatus(t, do(e.h, "POST", base+"/series/x/import", `{}`), 400)

	y.err = youtube.ErrNotFound
	wantStatus(t, do(e.h, "POST", p, `{}`), 404)
	y.err = errors.New("boom")
	wantStatus(t, do(e.h, "POST", p, `{}`), 502)
	e.api.yt = nil
	wantStatus(t, do(e.h, "POST", p, `{}`), 503)
}

func TestVideoYouTubeFields(t *testing.T) {
	e := setup(t, nil)
	h := e.h

	// Create derives the ID; a client-sent youtube_video_id is rejected as unknown.
	rec := do(h, "POST", base+"/videos", `{"title":"V","venue":"stream","url":"https://youtu.be/xyz","channel_title":" Ch ","duration_seconds":90}`)
	wantStatus(t, rec, 201)
	v := decode[store.Video](t, rec)
	if v.YouTubeVideoID != "xyz" || v.ChannelTitle != "Ch" || v.DurationSeconds != 90 {
		t.Errorf("video = %+v", v)
	}
	rec = do(h, "POST", base+"/videos", `{"title":"V","venue":"stream","url":"https://example.com/x","youtube_video_id":"evil"}`)
	if rec.Code == 201 && decode[store.Video](t, rec).YouTubeVideoID == "evil" {
		t.Error("client set youtube_video_id")
	}
	rec = do(h, "POST", base+"/videos", `{"title":"V","venue":"stream","url":"https://example.com/x"}`)
	wantStatus(t, rec, 201)
	if got := decode[store.Video](t, rec); got.YouTubeVideoID != "" {
		t.Errorf("non-youtube url: %+v", got)
	}

	// Validation.
	wantStatus(t, do(h, "POST", base+"/videos", `{"title":"V","venue":"stream","duration_seconds":-1}`), 400)
	wantStatus(t, do(h, "POST", base+"/videos", `{"title":"V","venue":"stream","duration_seconds":10000001}`), 400)
	wantStatus(t, do(h, "POST", base+"/videos", `{"title":"V","venue":"stream","duration_seconds":10000000}`), 201)
	wantStatus(t, do(h, "POST", base+"/videos", `{"title":"V","venue":"stream","channel_title":"`+strings.Repeat("é", 201)+`"}`), 400)
	wantStatus(t, do(h, "POST", base+"/videos", `{"title":"V","venue":"stream","channel_title":"`+strings.Repeat("é", 200)+`"}`), 201)

	// Patch: derives when url is present, clears for non-YouTube URLs, untouched otherwise.
	vp := base + "/videos/" + itoa(v.ID)
	rec = do(h, "PATCH", vp, `{"title":"New","channel_title":"C2","duration_seconds":5}`)
	wantStatus(t, rec, 200)
	if e.lastPatch.YouTubeVideoID != nil {
		t.Error("patch without url touched youtube_video_id")
	}
	if got := decode[store.Video](t, rec); got.YouTubeVideoID != "xyz" || got.ChannelTitle != "C2" || got.DurationSeconds != 5 {
		t.Errorf("got %+v", got)
	}
	rec = do(h, "PATCH", vp, `{"url":"https://youtu.be/abc"}`)
	wantStatus(t, rec, 200)
	if got := decode[store.Video](t, rec); got.YouTubeVideoID != "abc" {
		t.Errorf("got %+v", got)
	}
	rec = do(h, "PATCH", vp, `{"url":"https://example.com/z"}`)
	wantStatus(t, rec, 200)
	if got := decode[store.Video](t, rec); got.YouTubeVideoID != "" || e.lastPatch.YouTubeVideoID == nil {
		t.Errorf("got %+v, patch %+v", got, e.lastPatch)
	}
	rec = do(h, "PATCH", vp, `{"youtube_video_id":"evil"}`)
	if rec.Code != 400 {
		t.Errorf("status %d, want 400 for unknown field", rec.Code)
	}
	wantStatus(t, do(h, "PATCH", vp, `{"duration_seconds":-5}`), 400)
	wantStatus(t, do(h, "PATCH", vp, `{"channel_title":"`+strings.Repeat("x", 201)+`"}`), 400)

	// Approve derives too.
	sg := addSuggestion(e, store.SuggestionPending)
	rec = do(h, "POST", base+"/suggestions/"+itoa(sg.ID)+"/approve",
		`{"title":"A","venue":"stream","url":"https://youtu.be/appr","channel_title":"AC","duration_seconds":7}`)
	wantStatus(t, rec, 200)
	av := decode[struct {
		Video store.Video `json:"video"`
	}](t, rec).Video
	if av.YouTubeVideoID != "appr" || av.ChannelTitle != "AC" || av.DurationSeconds != 7 {
		t.Errorf("approved = %+v", av)
	}
}

func TestSeriesYouTubePlaylistID(t *testing.T) {
	e := setup(t, nil)
	h := e.h
	rec := do(h, "POST", base+"/series", `{"title":"S","url":"`+plURL+`"}`)
	wantStatus(t, rec, 201)
	s := decode[store.Series](t, rec)
	if s.YouTubePlaylistID != "PLabc" {
		t.Errorf("series = %+v", s)
	}
	rec = do(h, "POST", base+"/series", `{"title":"S","url":"https://example.com","youtube_playlist_id":"evil","thumbnail_video_id":"evil"}`)
	wantStatus(t, rec, 400)
	rec = do(h, "POST", base+"/series", `{"title":"S","url":"https://example.com"}`)
	wantStatus(t, rec, 201)
	if got := decode[store.Series](t, rec); got.YouTubePlaylistID != "" {
		t.Errorf("series = %+v", got)
	}

	sp := base + "/series/" + itoa(s.ID)
	wantStatus(t, do(h, "PATCH", sp, `{"title":"T"}`), 200)
	if got := e.series[s.ID].YouTubePlaylistID; got != "PLabc" {
		t.Errorf("untouched patch changed playlist id to %q", got)
	}
	wantStatus(t, do(h, "PATCH", sp, `{"url":"https://example.com"}`), 200)
	if got := e.series[s.ID].YouTubePlaylistID; got != "" {
		t.Errorf("playlist id = %q, want cleared", got)
	}
	wantStatus(t, do(h, "PATCH", sp, `{"url":"https://www.youtube.com/playlist?list=PLnew"}`), 200)
	if got := e.series[s.ID].YouTubePlaylistID; got != "PLnew" {
		t.Errorf("playlist id = %q", got)
	}
}

func TestLinkValidation(t *testing.T) {
	fs := setup(t, nil)
	h := fs.h
	v, _ := fs.CreateVideo(context.Background(), store.Video{ServerID: sid1, Title: "a"})
	s, _ := fs.CreateSeries(context.Background(), store.Series{ServerID: sid1, Title: "s"})
	videoPath := base + "/videos/" + itoa(v.ID)
	seriesPath := base + "/series/" + itoa(s.ID)

	bad := []string{"javascript:alert(1)", "data:text/html,x", "ftp://x.example/f", "https://", "/relative", "example.com/no-scheme",
		"https://x.example/" + strings.Repeat("a", 2000)}
	for _, u := range bad {
		q, _ := json.Marshal(u)
		for _, field := range []string{"url", "vod_url", "edit_url", "patreon_url", "youtube_url"} {
			wantStatus(t, do(h, "PATCH", videoPath, `{"`+field+`":`+string(q)+`}`), 400)
		}
		wantStatus(t, do(h, "POST", base+"/videos", `{"title":"x","venue":"stream","url":`+string(q)+`}`), 400)
		wantStatus(t, do(h, "POST", base+"/series", `{"title":"x","url":`+string(q)+`}`), 400)
		wantStatus(t, do(h, "PATCH", seriesPath, `{"url":`+string(q)+`}`), 400)
	}

	// Valid links are trimmed and stored; empty clears.
	wantStatus(t, do(h, "PATCH", videoPath, `{"edit_url":"  https://drive.example/file?id=1  "}`), 200)
	if fs.lastPatch.EditURL == nil || *fs.lastPatch.EditURL != "https://drive.example/file?id=1" {
		t.Fatalf("edit_url patch = %v", fs.lastPatch.EditURL)
	}
	wantStatus(t, do(h, "PATCH", videoPath, `{"edit_url":""}`), 200)
	if fs.lastPatch.EditURL == nil || *fs.lastPatch.EditURL != "" {
		t.Fatalf("edit_url clear = %v", fs.lastPatch.EditURL)
	}
	wantStatus(t, do(h, "POST", base+"/videos", `{"title":"x","venue":"stream","url":"http://example.com/v"}`), 201)
}

const (
	uAlice = "300000000000000001"
	uBob   = "300000000000000002"
	uCarl  = "300000000000000003"
	uDeny  = "300000000000000004"
	uErr   = "300000000000000005"
)

func withMembers(e *env) {
	e.users = []store.User{
		{DiscordID: uAlice, Username: "alice_u", GlobalName: "Alice", Avatar: "av1"},
		{DiscordID: uBob, Username: "bob_u"},
		{DiscordID: uDeny, Username: "denied"},
		{DiscordID: uErr, Username: "broken"},
	}
	e.acc.userDeny = map[string]bool{uDeny: true}
	e.acc.userErr = map[string]error{uErr: errors.New("boom")}
}

func TestListMembers(t *testing.T) {
	e := setup(t, nil)
	withMembers(e)
	rec := do(e.h, "GET", base+"/members", "")
	wantStatus(t, rec, 200)
	got := decode[[]map[string]string](t, rec)
	if len(got) != 2 || got[0]["discord_id"] != uAlice || got[0]["name"] != "Alice" || got[0]["avatar"] != "av1" ||
		got[1]["discord_id"] != uBob || got[1]["name"] != "bob_u" {
		t.Errorf("members = %v", got)
	}

	e.users = nil
	rec = do(e.h, "GET", base+"/members", "")
	wantStatus(t, rec, 200)
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("empty body = %q", rec.Body.String())
	}

	// me has no access to server 2.
	wantStatus(t, do(e.h, "GET", "/api/servers/2/members", ""), 404)
}

func TestPatchVideoEditor(t *testing.T) {
	type want struct {
		status int
		id     string
		name   string
	}
	tests := []struct {
		name string
		body string
		want want
	}{
		{"member sets name from user", `{"editor_id":"` + uAlice + `","editor_name":"Client Name"}`, want{200, uAlice, "Alice"}},
		{"member username fallback", `{"editor_id":"` + uBob + `"}`, want{200, uBob, "bob_u"}},
		{"unknown user", `{"editor_id":"` + uCarl + `"}`, want{status: 400}},
		{"no access", `{"editor_id":"` + uDeny + `"}`, want{status: 400}},
		{"check error", `{"editor_id":"` + uErr + `"}`, want{status: 502}},
		{"bad snowflake", `{"editor_id":"abc"}`, want{status: 400}},
		{"free text", `{"editor_name":"  Zed  "}`, want{200, "", "Zed"}},
		{"free text with empty id", `{"editor_id":"","editor_name":"Zed"}`, want{200, "", "Zed"}},
		{"too long", `{"editor_name":"` + strings.Repeat("x", 101) + `"}`, want{status: 400}},
		{"clear", `{"editor_id":"","editor_name":""}`, want{200, "", ""}},
		{"untouched", `{"title":"t"}`, want{200, "999", "Old"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, nil)
			withMembers(e)
			v, _ := e.CreateVideo(context.Background(), withServer(store.Video{EditorID: "999", EditorName: "Old"}))
			rec := do(e.h, "PATCH", base+"/videos/"+itoa(v.ID), tc.body)
			wantStatus(t, rec, tc.want.status)
			if tc.want.status != 200 {
				return
			}
			got := decode[store.Video](t, rec)
			if got.EditorID != tc.want.id || got.EditorName != tc.want.name {
				t.Errorf("editor = %q/%q, want %q/%q", got.EditorID, got.EditorName, tc.want.id, tc.want.name)
			}
		})
	}
}

func addClip(e *env, sid int64) store.Clip {
	c := store.Clip{ID: e.id(), ServerID: sid, Status: store.ClipNew, Title: "t"}
	e.clips[c.ID] = c
	return c
}

func TestClipsList(t *testing.T) {
	e := setup(t, nil)
	addClip(e, sid1)
	addClip(e, sid2)
	rec := do(e.h, "GET", base+"/clips", "")
	wantStatus(t, rec, 200)
	if got := decode[[]store.Clip](t, rec); len(got) != 1 {
		t.Errorf("clips = %+v", got)
	}
	if f := e.lastClipFilter; f.ServerID != sid1 || len(f.Statuses) != 0 || f.Sort != store.ClipSortNewest || len(f.Missing) != 0 {
		t.Errorf("default filter = %+v", f)
	}

	wantStatus(t, do(e.h, "GET", base+"/clips?status=pending,edited&sort=status_at&missing=youtube,x", ""), 200)
	f := e.lastClipFilter
	if len(f.Statuses) != 2 || f.Statuses[0] != store.ClipPending || f.Statuses[1] != store.ClipEdited ||
		f.Sort != store.ClipSortStatusAt || len(f.Missing) != 2 || f.Missing[0] != store.PlatformYouTube || f.Missing[1] != store.PlatformX {
		t.Errorf("filter = %+v", f)
	}
	wantStatus(t, do(e.h, "GET", base+"/clips?sort=oldest", ""), 200)
	if e.lastClipFilter.Sort != store.ClipSortOldest {
		t.Errorf("sort = %q", e.lastClipFilter.Sort)
	}

	// An empty result encodes as [] not null.
	e = setup(t, nil)
	rec = do(e.h, "GET", base+"/clips", "")
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("body = %q", rec.Body.String())
	}

	for _, q := range []string{"status=bogus", "status=new,bogus", "sort=votes", "missing=facebook", "missing=youtube,", "status=new,"} {
		t.Run(q, func(t *testing.T) { wantStatus(t, do(e.h, "GET", base+"/clips?"+q, ""), 400) })
	}
}

func TestClipGet(t *testing.T) {
	e := setup(t, nil)
	c := addClip(e, sid1)
	other := addClip(e, sid2)
	rec := do(e.h, "GET", base+"/clips/"+itoa(c.ID), "")
	wantStatus(t, rec, 200)
	if got := decode[store.Clip](t, rec); got.ID != c.ID {
		t.Errorf("clip = %+v", got)
	}
	wantStatus(t, do(e.h, "GET", base+"/clips/"+itoa(other.ID), ""), 404)
	wantStatus(t, do(e.h, "GET", base+"/clips/9999", ""), 404)
	wantStatus(t, do(e.h, "GET", base+"/clips/abc", ""), 400)
}

func TestClipPatch(t *testing.T) {
	e := setup(t, nil)
	c := addClip(e, sid1)
	path := base + "/clips/" + itoa(c.ID)

	rec := do(e.h, "PATCH", path, `{"status":"posted","edited_title":"  Edited  ","edited_url":" https://example.com/e ","youtube_url":"https://youtu.be/x","notes":"n"}`)
	wantStatus(t, rec, 200)
	got := decode[store.Clip](t, rec)
	if got.Status != store.ClipPosted || got.EditedTitle != "Edited" || got.EditedURL != "https://example.com/e" || got.Notes != "n" {
		t.Errorf("clip = %+v", got)
	}
	p := e.lastClipPatch
	if p.Status == nil || *p.Status != store.ClipPosted || p.EditedTitle == nil || *p.EditedTitle != "Edited" ||
		p.EditedURL == nil || *p.EditedURL != "https://example.com/e" || p.InstagramURL != nil || p.XURL != nil || p.TikTokURL != nil {
		t.Errorf("patch = %+v", p)
	}
	if e.lastClipReviewer != me.DiscordID || !e.lastClipNow.Equal(testNow) {
		t.Errorf("reviewer %q now %v", e.lastClipReviewer, e.lastClipNow)
	}

	// Clearing a link is allowed.
	wantStatus(t, do(e.h, "PATCH", path, `{"youtube_url":"","tiktok_url":"https://tiktok.com/a","instagram_url":"http://i.example/a","x_url":"https://x.com/a"}`), 200)

	long := func(n int) string { return strings.Repeat("a", n) }
	for name, body := range map[string]string{
		"bad status":    `{"status":"approved"}`,
		"js link":       `{"edited_url":"javascript:alert(1)"}`,
		"js youtube":    `{"youtube_url":"javascript:alert(1)"}`,
		"bad instagram": `{"instagram_url":"ftp://x/y"}`,
		"bad x":         `{"x_url":"nope"}`,
		"bad tiktok":    `{"tiktok_url":"data:text/html,x"}`,
		"long title":    `{"edited_title":"` + long(301) + `"}`,
		"long notes":    `{"notes":"` + long(2001) + `"}`,
		"unknown field": `{"title":"x"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e.lastClipPatch = store.ClipPatch{}
			wantStatus(t, do(e.h, "PATCH", path, body), 400)
			if e.lastClipPatch != (store.ClipPatch{}) {
				t.Error("store patched despite 400")
			}
		})
	}
	wantStatus(t, do(e.h, "PATCH", path, `{"edited_title":"`+long(300)+`","notes":"`+long(2000)+`"}`), 200)

	wantStatus(t, do(e.h, "PATCH", base+"/clips/9999", `{"notes":"a"}`), 404)
	other := addClip(e, sid2)
	wantStatus(t, do(e.h, "PATCH", base+"/clips/"+itoa(other.ID), `{"notes":"a"}`), 404)
}

func TestClipSync(t *testing.T) {
	e := setup(t, nil)
	e.servers[sid1] = store.Server{ID: sid1, GuildID: guild1, Name: "One", ClipsChannelID: chan1}
	wantStatus(t, do(e.h, "POST", base+"/clips/sync", ""), 503)

	var gotSID int64
	e = setup(t, func(_ context.Context, id int64) error { gotSID = id; return nil })
	// The default server has only a suggestions channel.
	rec := do(e.h, "POST", base+"/clips/sync", "")
	wantStatus(t, rec, 400)
	if got := decode[map[string]string](t, rec)["error"]; got != "this server has no clips channel" {
		t.Errorf("error = %q", got)
	}
	e.servers[sid1] = store.Server{ID: sid1, GuildID: guild1, Name: "One", ClipsChannelID: chan1}
	wantStatus(t, do(e.h, "POST", base+"/clips/sync", ""), 204)
	if gotSID != sid1 {
		t.Errorf("synced server %d", gotSID)
	}

	e = setup(t, func(context.Context, int64) error { return errors.New("secret discord detail") })
	e.servers[sid1] = store.Server{ID: sid1, GuildID: guild1, Name: "One", ClipsChannelID: chan1}
	rec = do(e.h, "POST", base+"/clips/sync", "")
	wantStatus(t, rec, 502)
	if bytes.Contains(rec.Body.Bytes(), []byte("secret")) {
		t.Error("error detail leaked")
	}

	e = setup(t, func(context.Context, int64) error { return fmt.Errorf("sync: %w", discord.ErrMessageContentMissing) })
	e.servers[sid1] = store.Server{ID: sid1, GuildID: guild1, Name: "One", ClipsChannelID: chan1}
	rec = do(e.h, "POST", base+"/clips/sync", "")
	wantStatus(t, rec, 502)
	if !strings.Contains(decode[map[string]string](t, rec)["error"], "Message Content") {
		t.Errorf("body = %s", rec.Body.String())
	}
}

func TestPatchServerClipsChannel(t *testing.T) {
	e := setup(t, nil)
	chan2 := "200000000000000002"
	e.bot.channels = append(e.bot.channels, discord.Channel{ID: chan2, Name: "clips"})

	// A mod without manage is refused.
	wantStatus(t, do(e.h, "PATCH", base, `{"clips_channel_id":"`+chan2+`"}`), 403)
	if e.servers[sid1].ClipsChannelID != "" {
		t.Error("changed without manage")
	}

	e.acc.manage[sid1] = true
	rec := do(e.h, "PATCH", base, `{"clips_channel_id":" `+chan2+` "}`)
	wantStatus(t, rec, 200)
	if decode[store.Server](t, rec).ClipsChannelID != chan2 || e.servers[sid1].SuggestionsChannelID != chan1 {
		t.Errorf("server = %+v", e.servers[sid1])
	}

	// Disabling needs no Discord lookup.
	e.bot.err = errors.New("discord down")
	wantStatus(t, do(e.h, "PATCH", base, `{"clips_channel_id":""}`), 200)
	if e.servers[sid1].ClipsChannelID != "" {
		t.Error("channel not cleared")
	}
	wantStatus(t, do(e.h, "PATCH", base, `{"clips_channel_id":"`+chan2+`"}`), 502)
	e.bot.err = nil

	for name, body := range map[string]string{
		"unknown channel":     `{"clips_channel_id":"200000000000000009"}`,
		"bad channel":         `{"clips_channel_id":"general"}`,
		"same as suggestions": `{"clips_channel_id":"` + chan1 + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, do(e.h, "PATCH", base, body), 400)
			if e.servers[sid1].ClipsChannelID != "" {
				t.Error("server changed despite 400")
			}
		})
	}

	// And the other way round, plus setting both to one channel at once.
	wantStatus(t, do(e.h, "PATCH", base, `{"clips_channel_id":"`+chan2+`"}`), 200)
	rec = do(e.h, "PATCH", base, `{"suggestions_channel_id":"`+chan2+`"}`)
	wantStatus(t, rec, 400)
	if got := decode[map[string]string](t, rec)["error"]; got != "use different channels for clips and suggestions" {
		t.Errorf("error = %q", got)
	}
	if e.servers[sid1].SuggestionsChannelID != chan1 {
		t.Error("suggestions channel changed despite 400")
	}
	wantStatus(t, do(e.h, "PATCH", base, `{"suggestions_channel_id":"`+chan1+`","clips_channel_id":"`+chan1+`"}`), 400)
	// Swapping in one request is fine.
	wantStatus(t, do(e.h, "PATCH", base, `{"suggestions_channel_id":"`+chan2+`","clips_channel_id":"`+chan1+`"}`), 200)
	// Both disabled is fine.
	wantStatus(t, do(e.h, "PATCH", base, `{"suggestions_channel_id":"","clips_channel_id":""}`), 200)
}

// persona perms for the section tests.
var (
	pNone    = Perms{}
	pClips   = Perms{Access: true, Clips: true}
	pVideos  = Perms{Access: true, Videos: true}
	pBoth    = Perms{Access: true, Videos: true, Clips: true}
	pFull    = Perms{Access: true, Full: true, Videos: true, Clips: true}
	pManager = Perms{Access: true, Full: true, Videos: true, Clips: true, Manage: true}
	personas = []struct {
		name string
		p    Perms
	}{{"none", pNone}, {"clip editor", pClips}, {"video editor", pVideos}, {"both editors", pBoth}, {"full", pFull}, {"manager", pManager}}
)

func TestSectionAccess(t *testing.T) {
	routes := []struct {
		method, path, body string
		sec                section
	}{
		{"GET", "", "", secAny},
		{"PATCH", "", `{}`, secManage},
		{"DELETE", "", "", secManage},
		{"GET", "/discord/roles", "", secManage},
		{"GET", "/discord/channels", "", secManage},
		{"GET", "/series", "", secVideos},
		{"POST", "/series", `{}`, secVideos},
		{"POST", "/series/import", `{}`, secVideos},
		{"POST", "/series/1/import", `{}`, secVideos},
		{"PATCH", "/series/1", `{}`, secVideos},
		{"DELETE", "/series/1", "", secVideos},
		{"GET", "/videos", "", secVideos},
		{"POST", "/videos", `{}`, secVideos},
		{"POST", "/videos/reorder", `{}`, secVideos},
		{"GET", "/videos/1", "", secVideos},
		{"PATCH", "/videos/1", `{}`, secVideos},
		{"DELETE", "/videos/1", "", secVideos},
		{"GET", "/members", "", secVideos},
		{"GET", "/suggestions", "", secFull},
		{"POST", "/suggestions/sync", "", secFull},
		{"POST", "/suggestions/1/approve", `{}`, secFull},
		{"POST", "/suggestions/1/discard", "", secFull},
		{"POST", "/suggestions/1/restore", "", secFull},
		{"POST", "/suggestions/remove", `{}`, secFull},
		{"POST", "/suggestions/1/unremove", "", secFull},
		{"GET", "/clips", "", secClips},
		{"POST", "/clips/sync", "", secClips},
		{"GET", "/clips/1", "", secClips},
		{"PATCH", "/clips/1", `{}`, secClips},
	}
	for _, pe := range personas {
		for _, rt := range routes {
			t.Run(pe.name+" "+rt.method+" "+rt.path, func(t *testing.T) {
				e := setup(t, nil)
				e.acc.userPerms = map[string]Perms{me.DiscordID: pe.p}
				rec := do(e.h, rt.method, base+rt.path, rt.body)
				var ok bool
				if rt.sec == secManage {
					ok = pe.p.Manage
				} else {
					ok = pe.p.has(rt.sec)
				}
				switch {
				case !pe.p.Access:
					wantStatus(t, rec, 404)
					if !strings.Contains(rec.Body.String(), "server not found") {
						t.Errorf("body = %s", rec.Body.String())
					}
				case !ok:
					wantStatus(t, rec, 403)
					want := "you don't have access to this section"
					if rt.sec == secManage {
						want = "you can't manage this server"
					}
					if !strings.Contains(rec.Body.String(), want) {
						t.Errorf("body = %s, want %q", rec.Body.String(), want)
					}
				default:
					if rec.Code == 403 || (rec.Code == 404 && strings.Contains(rec.Body.String(), "server not found")) {
						t.Errorf("status = %d body %s, want it to pass the gate", rec.Code, rec.Body.String())
					}
				}
			})
		}
	}
}

func TestServerViewSections(t *testing.T) {
	want := map[string]Sections{
		"clip editor":  {Clips: true},
		"video editor": {Videos: true},
		"both editors": {Videos: true, Clips: true},
		"full":         {Videos: true, Clips: true, Suggestions: true},
		"manager":      {Videos: true, Clips: true, Suggestions: true},
	}
	for _, pe := range personas {
		t.Run(pe.name, func(t *testing.T) {
			e := setup(t, nil)
			e.acc.userPerms = map[string]Perms{me.DiscordID: pe.p}
			rec := do(e.h, "GET", "/api/servers", "")
			wantStatus(t, rec, 200)
			list := decode[[]ServerView](t, rec)
			if pe.name == "none" {
				if len(list) != 0 {
					t.Errorf("list = %+v", list)
				}
				return
			}
			if len(list) == 0 || list[0].ID != sid1 || list[0].Sections != want[pe.name] || list[0].CanManage != pe.p.Manage {
				t.Errorf("list = %+v", list)
			}
			rec = do(e.h, "GET", base, "")
			wantStatus(t, rec, 200)
			if got := decode[ServerView](t, rec); got.Sections != want[pe.name] {
				t.Errorf("sections = %+v", got.Sections)
			}
			if !strings.Contains(rec.Body.String(), `"sections":{"videos":`) || !strings.Contains(rec.Body.String(), `"clip_editor_role_ids":[]`) {
				t.Errorf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestMembersAndEditorSections(t *testing.T) {
	e := setup(t, nil)
	e.users = []store.User{
		{DiscordID: uAlice, Username: "alice"}, {DiscordID: uBob, Username: "bob"}, {DiscordID: uCarl, Username: "carl"},
	}
	e.acc.userPerms = map[string]Perms{uAlice: pVideos, uBob: pClips, uCarl: pBoth, me.DiscordID: pFull}
	rec := do(e.h, "GET", base+"/members", "")
	wantStatus(t, rec, 200)
	got := decode[[]map[string]string](t, rec)
	if len(got) != 2 || got[0]["discord_id"] != uAlice || got[1]["discord_id"] != uCarl {
		t.Errorf("members = %v", got)
	}

	e.videos[5] = store.Video{ID: 5, ServerID: sid1, Title: "v"}
	vp := base + "/videos/5"
	wantStatus(t, do(e.h, "PATCH", vp, `{"editor_id":"`+uBob+`"}`), 400)
	wantStatus(t, do(e.h, "PATCH", vp, `{"editor_id":"`+uAlice+`"}`), 200)
	wantStatus(t, do(e.h, "PATCH", vp, `{"editor_id":"`+uCarl+`"}`), 200)
}

func TestPatchServerEditorRoles(t *testing.T) {
	e := setup(t, nil)
	e.acc.manage[sid1] = true
	r1, r2 := "300000000000000001", "300000000000000002"
	rec := do(e.h, "PATCH", base, `{"clip_editor_role_ids":["`+r1+`","`+r1+`"],"video_editor_role_ids":["`+r2+`","`+r1+`","`+r2+`"]}`)
	wantStatus(t, rec, 200)
	got := decode[ServerView](t, rec)
	if len(got.ClipEditorRoleIDs) != 1 || got.ClipEditorRoleIDs[0] != r1 ||
		len(got.VideoEditorRoleIDs) != 2 || got.VideoEditorRoleIDs[0] != r2 || got.VideoEditorRoleIDs[1] != r1 {
		t.Errorf("server = %+v", got)
	}
	wantStatus(t, do(e.h, "PATCH", base, `{"clip_editor_role_ids":["x"]}`), 400)
	wantStatus(t, do(e.h, "PATCH", base, `{"video_editor_role_ids":["x"]}`), 400)
}
