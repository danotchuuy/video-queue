package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/danotchuuy/video-queue/internal/access"
	"github.com/danotchuuy/video-queue/internal/api"
	"github.com/danotchuuy/video-queue/internal/auth"
	"github.com/danotchuuy/video-queue/internal/clips"
	"github.com/danotchuuy/video-queue/internal/discord"
	"github.com/danotchuuy/video-queue/internal/store"
	"github.com/danotchuuy/video-queue/internal/suggest"
	"github.com/danotchuuy/video-queue/internal/twitch"
	"github.com/danotchuuy/video-queue/internal/youtube"
)

const (
	adminID  = "100000000000000001"
	modID    = "100000000000000002"
	ownerID  = "100000000000000003" // streamer who owns guild B
	clipEdID = "100000000000000004" // clip editor role in guild A
	vidEdID  = "100000000000000005" // video editor role in guild A
	clipRole = "300000000000000002"
	vidRole  = "300000000000000003"
	guildA   = "200000000000000001"
	guildB   = "200000000000000002"
	modRole  = "300000000000000001"
	channelA = "400000000000000001"
)

// fakeBot stands in for the Discord bot: modID has the mod role in guild A
// only, and ownerID owns guild B.
type fakeBot struct{}

func (fakeBot) BotGuilds(context.Context) ([]discord.Guild, error) {
	return []discord.Guild{{ID: guildA, Name: "Streamer A"}, {ID: guildB, Name: "Streamer B"}}, nil
}

func (b fakeBot) Guild(ctx context.Context, id string) (discord.Guild, error) {
	gs, _ := b.BotGuilds(ctx)
	for _, g := range gs {
		if g.ID == id {
			g.Roles = []discord.Role{{ID: id, Permissions: "0"}, {ID: modRole, Permissions: "2048"}}
			if id == guildB {
				g.OwnerID = ownerID
			}
			return g, nil
		}
	}
	return discord.Guild{}, discord.ErrNotFound
}

func (fakeBot) GuildRoles(context.Context, string) ([]discord.Role, error) {
	return []discord.Role{{ID: modRole, Name: "Mod"}}, nil
}

func (fakeBot) GuildTextChannels(context.Context, string) ([]discord.Channel, error) {
	return []discord.Channel{{ID: channelA, Name: "suggestions"}}, nil
}

func (fakeBot) GuildMember(_ context.Context, guildID, userID string) (discord.Member, error) {
	switch {
	case guildID == guildA && userID == modID:
		return discord.Member{Roles: []string{modRole}}, nil
	case guildID == guildA && userID == clipEdID:
		return discord.Member{Roles: []string{clipRole}}, nil
	case guildID == guildA && userID == vidEdID:
		return discord.Member{Roles: []string{vidRole}}, nil
	case guildID == guildB && userID == ownerID:
		return discord.Member{}, nil
	}
	return discord.Member{}, discord.ErrNotFound
}

// fakeYouTube serves one playlist; more items can be appended to simulate new uploads.
type fakeYouTube struct{ items []youtube.PlaylistItem }

func (f *fakeYouTube) Video(_ context.Context, id string) (youtube.Video, error) {
	for _, it := range f.items {
		if it.ID == id {
			return it.Video, nil
		}
	}
	return youtube.Video{}, youtube.ErrNotFound
}

func (f *fakeYouTube) Playlist(_ context.Context, id string) (youtube.Playlist, []youtube.PlaylistItem, error) {
	if id != "PLabridged" {
		return youtube.Playlist{}, nil, youtube.ErrNotFound
	}
	return youtube.Playlist{ID: id, Title: "DBZ Abridged", ChannelTitle: "TeamFourStar",
		URL: "https://www.youtube.com/playlist?list=" + id}, f.items, nil
}

func (f *fakeYouTube) Videos(ctx context.Context, ids []string) ([]youtube.Video, error) {
	var out []youtube.Video
	for _, id := range ids {
		if v, err := f.Video(ctx, id); err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakeYouTube) ChannelHandles(_ context.Context, ids []string) (map[string]string, error) {
	out := map[string]string{}
	for _, id := range ids {
		if id == "UCtfs" {
			out[id] = "@TeamFourStar"
		}
	}
	return out, nil
}

func (f *fakeYouTube) add(position int, id, title string) {
	f.items = append(f.items, youtube.PlaylistItem{Position: position, Video: youtube.Video{
		ID: id, Title: title, ChannelID: "UCtfs", ChannelTitle: "TeamFourStar", DurationSeconds: 600,
		URL: "https://www.youtube.com/watch?v=" + id,
	}})
}

// fakeTwitch knows one clip.
type fakeTwitch struct{}

func (fakeTwitch) Clips(_ context.Context, slugs []string) ([]twitch.Clip, error) {
	var out []twitch.Clip
	for _, s := range slugs {
		if s == "FunnyClip1" {
			out = append(out, twitch.Clip{ID: s, Title: "Chat breaks her", CreatorName: "clipper99",
				BroadcasterName: "druidgrove", Duration: 29.6,
				ThumbnailURL: "https://static-cdn.jtvnw.net/twitch-clips/abc-preview-480x272.jpg"})
		}
	}
	return out, nil
}

// TestEndToEnd wires the real store, auth middleware, access checker and API
// the way main does, with sessions inserted directly instead of a Discord login.
func TestEndToEnd(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	session := func(userID string) string {
		token := "token-" + userID
		sum := sha256.Sum256([]byte(token))
		if err := st.UpsertUser(ctx, store.User{DiscordID: userID, Username: userID}); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateSession(ctx, hex.EncodeToString(sum[:]), userID, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		return token
	}
	admin, mod, owner := session(adminID), session(modID), session(ownerID)
	clipEd, vidEd := session(clipEdID), session(vidEdID)

	yt := &fakeYouTube{}
	yt.add(1, "aaaaaaaaaa1", "Episode 1")
	yt.add(2, "aaaaaaaaaa2", "Episode 2")
	yt.add(3, "aaaaaaaaaa3", "Episode 3")

	logger := slog.New(slog.DiscardHandler)
	authSvc := auth.New(auth.Config{}, st)
	mux := newMux(t.TempDir())
	authSvc.Register(mux)
	api.New(api.Deps{
		Store:            st,
		Access:           accessAdapter{access.New(st, fakeBot{}, []string{adminID}, time.Minute, logger)},
		Bot:              fakeBot{},
		ClientID:         "app",
		SelfRegistration: true,
		YouTube:          yt,
		TwitchEnabled:    true,
		Log:              logger,
	}).Register(mux, authSvc.Require)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	do := func(token, method, path, body string, wantCode int) string {
		t.Helper()
		var r io.Reader
		if body != "" {
			r = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, r)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "vq_session", Value: token})
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != wantCode {
			t.Fatalf("%s %s: status = %d, want %d; body: %s", method, path, resp.StatusCode, wantCode, b)
		}
		return string(b)
	}
	decode := func(body string, v any) {
		t.Helper()
		if err := json.Unmarshal([]byte(body), v); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}

	do("", "GET", "/api/me", "", http.StatusUnauthorized)

	// Admin registers A; B's owner registers B themselves.
	var me struct {
		IsAdmin bool `json:"is_admin"`
	}
	decode(do(admin, "GET", "/api/me", "", http.StatusOK), &me)
	if !me.IsAdmin {
		t.Fatal("admin is not admin")
	}
	var srvA, srvB store.Server
	decode(do(admin, "POST", "/api/servers", `{"guild_id":"`+guildA+`"}`, http.StatusCreated), &srvA)
	do(admin, "POST", "/api/servers", `{"guild_id":"`+guildA+`"}`, http.StatusConflict)
	do(mod, "POST", "/api/servers", `{"guild_id":"`+guildB+`"}`, http.StatusForbidden)
	do(owner, "POST", "/api/servers", `{"guild_id":"`+guildA+`"}`, http.StatusForbidden) // not a manager of A
	decode(do(owner, "POST", "/api/servers", `{"guild_id":"`+guildB+`","name":"B"}`, http.StatusCreated), &srvB)
	if srvA.Name != "Streamer A" {
		t.Fatalf("name not defaulted from guild: %q", srvA.Name)
	}
	a, b := fmt.Sprint(srvA.ID), fmt.Sprint(srvB.ID)
	do(admin, "PATCH", "/api/servers/"+a, `{"mod_role_ids":["`+modRole+`"],"suggestions_channel_id":"`+channelA+`"}`, http.StatusOK)
	do(owner, "PATCH", "/api/servers/"+b, `{"mod_role_ids":["`+modRole+`"]}`, http.StatusOK)

	// The owner manages B without any mod role, and can't see A.
	var ownerServers []struct {
		store.Server
		CanManage bool `json:"can_manage"`
	}
	decode(do(owner, "GET", "/api/servers", "", http.StatusOK), &ownerServers)
	if len(ownerServers) != 1 || ownerServers[0].ID != srvB.ID || !ownerServers[0].CanManage {
		t.Fatalf("owner sees servers %+v, want only B with can_manage", ownerServers)
	}
	do(owner, "GET", "/api/servers/"+a+"/series", "", http.StatusNotFound)
	do(owner, "GET", "/api/servers/"+b+"/discord/roles", "", http.StatusOK)

	// The mod has the role in A's guild only, so they see A and nothing of B.
	var servers []store.Server
	decode(do(mod, "GET", "/api/servers", "", http.StatusOK), &servers)
	if len(servers) != 1 || servers[0].ID != srvA.ID {
		t.Fatalf("mod sees servers %+v, want only A", servers)
	}
	do(mod, "GET", "/api/servers/"+b+"/videos", "", http.StatusNotFound)
	do(mod, "PATCH", "/api/servers/"+a, `{"name":"x"}`, http.StatusForbidden)
	do(mod, "DELETE", "/api/servers/"+a, "", http.StatusForbidden)

	// Content is scoped per server.
	var seriesA, seriesB store.Series
	decode(do(mod, "POST", "/api/servers/"+a+"/series", `{"title":"DBZ Abridged"}`, http.StatusCreated), &seriesA)
	decode(do(owner, "POST", "/api/servers/"+b+"/series", `{"title":"Other show"}`, http.StatusCreated), &seriesB)
	do(mod, "POST", "/api/servers/"+a+"/videos", fmt.Sprintf(`{"title":"Ep","series_id":%d}`, seriesB.ID), http.StatusBadRequest)

	var video store.Video
	decode(do(mod, "POST", "/api/servers/"+a+"/videos", fmt.Sprintf(`{"title":"Episode 1","episode":"1","series_id":%d}`, seriesA.ID), http.StatusCreated), &video)
	if video.Venue != store.VenueWatchParty || video.ServerID != srvA.ID || video.CreatedBy != modID {
		t.Fatalf("unexpected video: %+v", video)
	}
	do(admin, "GET", fmt.Sprintf("/api/servers/%s/videos/%d", b, video.ID), "", http.StatusNotFound)

	decode(do(mod, "PATCH", fmt.Sprintf("/api/servers/%s/videos/%d", a, video.ID), `{"status":"reacted"}`, http.StatusOK), &video)
	if video.ReactedAt == nil {
		t.Fatal("reacted_at not set when status moved to reacted")
	}

	// Editor assignment: only users with access to this server can be picked,
	// and the stored name comes from their profile, not the request.
	videoPath := fmt.Sprintf("/api/servers/%s/videos/%d", a, video.ID)
	var members []struct {
		DiscordID string `json:"discord_id"`
	}
	decode(do(mod, "GET", "/api/servers/"+a+"/members", "", http.StatusOK), &members)
	if len(members) != 2 { // admin + mod; the owner of B has no access to A
		t.Fatalf("members of A = %+v, want admin and mod", members)
	}
	decode(do(mod, "PATCH", videoPath, `{"editor_id":"`+modID+`","editor_name":"spoofed"}`, http.StatusOK), &video)
	if video.EditorID != modID || video.EditorName != modID { // session() names users by their ID
		t.Fatalf("editor = %q / %q", video.EditorID, video.EditorName)
	}
	do(mod, "PATCH", videoPath, `{"editor_id":"`+ownerID+`"}`, http.StatusBadRequest)
	decode(do(mod, "PATCH", videoPath, `{"editor_id":"","editor_name":"Freelance Fred"}`, http.StatusOK), &video)
	if video.EditorID != "" || video.EditorName != "Freelance Fred" {
		t.Fatalf("free-text editor = %q / %q", video.EditorID, video.EditorName)
	}

	// Suggestions approve into the same server.
	if err := st.UpsertSuggestion(ctx, store.Suggestion{
		ServerID: srvA.ID, DiscordMessageID: "500000000000000001", ChannelID: channelA,
		AuthorID: "7", AuthorName: "viewer", Content: "react to https://youtu.be/abc pls", PostedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	var suggestions []store.Suggestion
	decode(do(mod, "GET", "/api/servers/"+a+"/suggestions?status=pending", "", http.StatusOK), &suggestions)
	if len(suggestions) != 1 {
		t.Fatalf("got %d suggestions, want 1", len(suggestions))
	}
	approve := fmt.Sprintf("/api/servers/%s/suggestions/%d/approve", a, suggestions[0].ID)
	do(mod, "POST", approve, `{"title":"abc","venue":"stream"}`, http.StatusOK)
	do(mod, "POST", approve, `{"title":"abc","venue":"stream"}`, http.StatusConflict)
	do(mod, "POST", "/api/servers/"+a+"/suggestions/sync", "", http.StatusServiceUnavailable)

	// YouTube: a pasted youtu.be link gets its video ID derived server-side.
	var ytVideo store.Video
	decode(do(mod, "POST", "/api/servers/"+a+"/videos", `{"title":"One-off","venue":"stream","url":"https://youtu.be/aaaaaaaaaa1?t=30"}`, http.StatusCreated), &ytVideo)
	if ytVideo.YouTubeVideoID != "aaaaaaaaaa1" {
		t.Fatalf("youtube_video_id = %q", ytVideo.YouTubeVideoID)
	}
	do(mod, "GET", "/api/youtube/video?url=https://evil.example/watch?v=aaaaaaaaaa1", "", http.StatusBadRequest)

	// Import episodes 1 and 3 of a playlist as a new series.
	var imported struct {
		Series store.Series  `json:"series"`
		Videos []store.Video `json:"videos"`
	}
	decode(do(mod, "POST", "/api/servers/"+a+"/series/import",
		`{"playlist_url":"https://www.youtube.com/playlist?list=PLabridged","video_ids":["aaaaaaaaaa1","aaaaaaaaaa3"]}`,
		http.StatusCreated), &imported)
	if imported.Series.Title != "DBZ Abridged" || imported.Series.YouTubePlaylistID != "PLabridged" ||
		imported.Series.ThumbnailVideoID != "aaaaaaaaaa1" || len(imported.Videos) != 2 ||
		imported.Videos[1].Episode != "3" || imported.Videos[1].DurationSeconds != 600 {
		t.Fatalf("unexpected import: %+v", imported)
	}

	// A new upload appears; re-checking the playlist adds only what's missing
	// (episode 2, which was deselected, and the new episode 4).
	yt.add(4, "aaaaaaaaaa4", "Episode 4")
	decode(do(mod, "POST", fmt.Sprintf("/api/servers/%s/series/%d/import", a, imported.Series.ID), `{}`, http.StatusOK), &imported)
	if len(imported.Videos) != 2 || imported.Videos[0].YouTubeVideoID != "aaaaaaaaaa2" || imported.Videos[1].YouTubeVideoID != "aaaaaaaaaa4" {
		t.Fatalf("re-import added %+v, want episodes 2 and 4", imported.Videos)
	}
	decode(do(mod, "POST", fmt.Sprintf("/api/servers/%s/series/%d/import", a, imported.Series.ID), `{}`, http.StatusOK), &imported)
	if len(imported.Videos) != 0 {
		t.Fatalf("second re-import added %d videos, want 0", len(imported.Videos))
	}
	// Another server can't import into this series.
	do(owner, "POST", fmt.Sprintf("/api/servers/%s/series/%d/import", b, imported.Series.ID), `{}`, http.StatusNotFound)

	// Suggestions with a YouTube link get the video's title after sync.
	if err := st.UpsertSuggestion(ctx, store.Suggestion{
		ServerID: srvA.ID, DiscordMessageID: "500000000000000002", ChannelID: channelA,
		AuthorID: "7", AuthorName: "viewer", Content: "pls react <youtu.be/aaaaaaaaaa2>", PostedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := suggest.NewEnricher(st, yt, logger).Enrich(ctx, srvA.ID); err != nil {
		t.Fatal(err)
	}
	decode(do(mod, "GET", "/api/servers/"+a+"/suggestions?status=pending", "", http.StatusOK), &suggestions)
	if len(suggestions) != 1 || suggestions[0].VideoTitle != "Episode 2" || suggestions[0].YouTubeVideoID != "aaaaaaaaaa2" ||
		suggestions[0].DurationSeconds != 600 {
		t.Fatalf("enriched suggestions = %+v", suggestions)
	}
	if suggestions[0].ChannelHandle != "@TeamFourStar" || suggestions[0].SelfSuggestion {
		t.Fatalf("viewer's suggestion: handle %q, self %v", suggestions[0].ChannelHandle, suggestions[0].SelfSuggestion)
	}

	// The approved suggestion's video remembers who suggested it.
	var streamQueue []store.Video
	decode(do(mod, "GET", "/api/servers/"+a+"/videos?status=queued&venue=stream", "", http.StatusOK), &streamQueue)
	if len(streamQueue) == 0 || streamQueue[0].SuggestedByID != "7" || streamQueue[0].SuggestedByName != "viewer" {
		t.Fatalf("approved video suggester = %+v", streamQueue)
	}

	// Self-suggestion: the channel itself (Discord name "Team Four Star") suggests its own video.
	if err := st.UpsertSuggestion(ctx, store.Suggestion{
		ServerID: srvA.ID, DiscordMessageID: "500000000000000003", ChannelID: channelA,
		AuthorID: "600000000000000008", AuthorName: "Team Four Star", AuthorUsername: "tfs_official",
		Content: "watch my new ep https://youtu.be/aaaaaaaaaa3", PostedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	enricher := suggest.NewEnricher(st, yt, logger)
	selfPath := "/api/servers/" + a + "/suggestions?author_id=600000000000000008&status="
	if err := enricher.Enrich(ctx, srvA.ID); err != nil {
		t.Fatal(err)
	}
	decode(do(mod, "GET", selfPath+"pending", "", http.StatusOK), &suggestions)
	if len(suggestions) != 1 || !suggestions[0].SelfSuggestion {
		t.Fatalf("self-suggestion not flagged (blocking off): %+v", suggestions)
	}
	selfID := suggestions[0].ID

	// Turning blocking on discards it on the next sync; a mod can restore it and it stays.
	do(mod, "PATCH", "/api/servers/"+a, `{"block_self_suggestions":true}`, http.StatusForbidden)
	do(admin, "PATCH", "/api/servers/"+a, `{"block_self_suggestions":true}`, http.StatusOK)
	if err := enricher.Enrich(ctx, srvA.ID); err != nil {
		t.Fatal(err)
	}
	decode(do(mod, "GET", selfPath+"discarded", "", http.StatusOK), &suggestions)
	if len(suggestions) != 1 || suggestions[0].DiscardReason != store.DiscardSelfSuggestion {
		t.Fatalf("self-suggestion not auto-discarded: %+v", suggestions)
	}
	do(mod, "POST", fmt.Sprintf("/api/servers/%s/suggestions/%d/restore", a, selfID), "", http.StatusOK)
	if err := enricher.Enrich(ctx, srvA.ID); err != nil {
		t.Fatal(err)
	}
	decode(do(mod, "GET", selfPath+"pending", "", http.StatusOK), &suggestions)
	if len(suggestions) != 1 || suggestions[0].DiscardReason != "" {
		t.Fatalf("restored self-suggestion was discarded again: %+v", suggestions)
	}

	// A second viewer links the same video (different URL form): both cards list both suggesters.
	if err := st.UpsertSuggestion(ctx, store.Suggestion{
		ServerID: srvA.ID, DiscordMessageID: "500000000000000004", ChannelID: channelA,
		AuthorID: "600000000000000009", AuthorName: "fan2", Content: "https://www.youtube.com/watch?v=aaaaaaaaaa2&t=5",
		PostedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := enricher.Enrich(ctx, srvA.ID); err != nil {
		t.Fatal(err)
	}
	var withSuggesters []struct {
		store.Suggestion
		Suggesters []store.Suggester `json:"suggesters"`
	}
	decode(do(mod, "GET", "/api/servers/"+a+"/suggestions?status=pending&sort=oldest", "", http.StatusOK), &withSuggesters)
	byAuthor := map[string][]store.Suggester{}
	for _, sg := range withSuggesters {
		byAuthor[sg.AuthorID] = sg.Suggesters
	}
	if got := byAuthor["7"]; len(got) != 2 || got[0].AuthorID != "7" || got[1].AuthorName != "fan2" {
		t.Fatalf("suggesters on viewer's card = %+v", got)
	}
	if got := byAuthor["600000000000000009"]; len(got) != 2 {
		t.Fatalf("suggesters on fan2's card = %+v", got)
	}

	// Clips: a Twitch clip shared in Discord is looked up, selected from the inbox,
	// moved across the board, and filtered by platforms it isn't posted to yet.
	if err := st.UpsertClip(ctx, store.Clip{
		ServerID: srvA.ID, DiscordMessageID: "700000000000000001", ChannelID: "400000000000000002",
		MessageURL: "https://discord.com/channels/" + guildA + "/400000000000000002/700000000000000001",
		AuthorID:   "600000000000000009", AuthorName: "fan2", Content: "lmao clips.twitch.tv/FunnyClip1",
		PostedAt: time.Now(), ClipURL: "https://clips.twitch.tv/FunnyClip1", TwitchClipID: "FunnyClip1", Title: "lmao",
	}); err != nil {
		t.Fatal(err)
	}
	if err := clips.NewEnricher(st, fakeTwitch{}, logger).Enrich(ctx, srvA.ID); err != nil {
		t.Fatal(err)
	}
	var inbox []store.Clip
	decode(do(mod, "GET", "/api/servers/"+a+"/clips?status=new", "", http.StatusOK), &inbox)
	if len(inbox) != 1 || inbox[0].Title != "Chat breaks her" || inbox[0].ClipperName != "clipper99" ||
		inbox[0].DurationSeconds != 30 || inbox[0].ThumbnailURL == "" {
		t.Fatalf("clip inbox = %+v", inbox)
	}
	clipPath := fmt.Sprintf("/api/servers/%s/clips/%d", a, inbox[0].ID)
	do(owner, "GET", fmt.Sprintf("/api/servers/%s/clips/%d", b, inbox[0].ID), "", http.StatusNotFound)
	do(mod, "PATCH", clipPath, `{"status":"pending"}`, http.StatusOK)
	do(mod, "PATCH", clipPath, `{"tiktok_url":"javascript:alert(1)"}`, http.StatusBadRequest)
	var clip store.Clip
	decode(do(mod, "PATCH", clipPath, `{"status":"posted","edited_title":"She lost it","edited_url":"https://drive.example/c1",`+
		`"youtube_url":"https://youtube.com/shorts/abcdefghijk","tiktok_url":"https://www.tiktok.com/@d/video/1"}`, http.StatusOK), &clip)
	if clip.ReviewedBy != modID || clip.FirstPostedAt == nil || clip.EditedTitle != "She lost it" {
		t.Fatalf("posted clip = %+v", clip)
	}
	var posted []store.Clip
	decode(do(mod, "GET", "/api/servers/"+a+"/clips?status=posted&missing=instagram,x", "", http.StatusOK), &posted)
	if len(posted) != 1 {
		t.Fatalf("clip missing instagram/x not listed: %+v", posted)
	}
	decode(do(mod, "GET", "/api/servers/"+a+"/clips?status=posted&missing=youtube,tiktok", "", http.StatusOK), &posted)
	if len(posted) != 0 {
		t.Fatalf("clip already on youtube+tiktok listed as missing: %+v", posted)
	}
	do(mod, "GET", "/api/servers/"+a+"/clips?missing=myspace", "", http.StatusBadRequest)

	// Removing suggestions: the approved one disappears from the list but its video stays,
	// and a later sync of the same Discord message doesn't bring it back.
	var approved []store.Suggestion
	decode(do(mod, "GET", "/api/servers/"+a+"/suggestions?status=approved", "", http.StatusOK), &approved)
	if len(approved) != 1 {
		t.Fatalf("approved suggestions = %+v", approved)
	}
	removedID := approved[0].ID
	var removed struct{ Removed int64 }
	decode(do(mod, "POST", "/api/servers/"+a+"/suggestions/remove", fmt.Sprintf(`{"ids":[%d]}`, removedID), http.StatusOK), &removed)
	if removed.Removed != 1 {
		t.Fatalf("removed = %d", removed.Removed)
	}
	if err := st.UpsertSuggestion(ctx, store.Suggestion{ // what the next sync does
		ServerID: srvA.ID, DiscordMessageID: approved[0].DiscordMessageID, ChannelID: channelA,
		AuthorID: approved[0].AuthorID, AuthorName: "viewer", Content: approved[0].Content, ReactionCount: 5, PostedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	decode(do(mod, "GET", "/api/servers/"+a+"/suggestions?status=approved", "", http.StatusOK), &approved)
	if len(approved) != 0 {
		t.Fatalf("removed suggestion came back after sync: %+v", approved)
	}
	decode(do(mod, "GET", "/api/servers/"+a+"/videos?status=queued&venue=stream", "", http.StatusOK), &streamQueue)
	if len(streamQueue) == 0 || streamQueue[0].SuggestedByName != "viewer" {
		t.Fatalf("approved video should stay in the queue: %+v", streamQueue)
	}
	do(owner, "POST", "/api/servers/"+b+"/suggestions/remove", `{"ids":[1,2,3]}`, http.StatusOK) // other server's ids: no effect
	// Undo brings it back.
	do(mod, "POST", fmt.Sprintf("/api/servers/%s/suggestions/%d/unremove", a, removedID), "", http.StatusNoContent)
	decode(do(mod, "GET", "/api/servers/"+a+"/suggestions?status=approved", "", http.StatusOK), &approved)
	if len(approved) != 1 || approved[0].ID != removedID {
		t.Fatalf("unremoved suggestion not back: %+v", approved)
	}

	// Editor roles: clip editors only reach clip pages, video editors only video pages.
	do(clipEd, "GET", "/api/servers/"+a+"/clips", "", http.StatusNotFound) // no editor roles configured yet
	do(admin, "PATCH", "/api/servers/"+a, `{"clip_editor_role_ids":["`+clipRole+`"],"video_editor_role_ids":["`+vidRole+`"]}`, http.StatusOK)
	type sections struct{ Videos, Clips, Suggestions bool }
	var views []struct {
		ID       int64    `json:"id"`
		Sections sections `json:"sections"`
	}
	decode(do(clipEd, "GET", "/api/servers", "", http.StatusOK), &views)
	if len(views) != 1 || views[0].Sections != (sections{Clips: true}) {
		t.Fatalf("clip editor servers = %+v", views)
	}
	do(clipEd, "GET", "/api/servers/"+a+"/clips", "", http.StatusOK)
	do(clipEd, "PATCH", clipPath, `{"notes":"cut at 0:12"}`, http.StatusOK)
	do(clipEd, "GET", "/api/servers/"+a+"/videos", "", http.StatusForbidden)
	do(clipEd, "GET", "/api/servers/"+a+"/suggestions", "", http.StatusForbidden)
	do(clipEd, "PATCH", "/api/servers/"+a, `{"name":"x"}`, http.StatusForbidden)

	decode(do(vidEd, "GET", "/api/servers", "", http.StatusOK), &views)
	if len(views) != 1 || views[0].Sections != (sections{Videos: true}) {
		t.Fatalf("video editor servers = %+v", views)
	}
	do(vidEd, "GET", "/api/servers/"+a+"/videos", "", http.StatusOK)
	do(vidEd, "GET", "/api/servers/"+a+"/series", "", http.StatusOK)
	do(vidEd, "GET", "/api/servers/"+a+"/clips", "", http.StatusForbidden)
	do(vidEd, "GET", "/api/servers/"+a+"/suggestions", "", http.StatusForbidden)
	do(vidEd, "GET", "/api/servers/"+b+"/videos", "", http.StatusNotFound) // no access to B at all

	// Video editors can be assigned as a video's editor; clip-only editors can't.
	decode(do(mod, "GET", "/api/servers/"+a+"/members", "", http.StatusOK), &members)
	var memberIDs []string
	for _, m := range members {
		memberIDs = append(memberIDs, m.DiscordID)
	}
	if !slices.Contains(memberIDs, vidEdID) || slices.Contains(memberIDs, clipEdID) {
		t.Fatalf("members = %v, want the video editor and not the clip editor", memberIDs)
	}
	do(mod, "PATCH", videoPath, `{"editor_id":"`+vidEdID+`"}`, http.StatusOK)
	do(mod, "PATCH", videoPath, `{"editor_id":"`+clipEdID+`"}`, http.StatusBadRequest)

	// Deleting a server removes it and its content.
	do(owner, "DELETE", "/api/servers/"+b, "", http.StatusNoContent)
	do(admin, "GET", "/api/servers/"+b+"/series", "", http.StatusNotFound)

	do(mod, "POST", "/api/auth/logout", "", http.StatusNoContent)
	do(mod, "GET", "/api/me", "", http.StatusUnauthorized)
}
