package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

var ctx = context.Background()

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func mustServer(t *testing.T, s *Store, guild string) Server {
	t.Helper()
	srv, err := s.CreateServer(ctx, Server{GuildID: guild, Name: "srv-" + guild})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

// newStoreSrv returns a store with one server and that server's ID.
func newStoreSrv(t *testing.T) (*Store, int64) {
	t.Helper()
	s := newStore(t)
	return s, mustServer(t, s, "g1").ID
}

func ptr[T any](v T) *T { return &v }

func mustVideo(t *testing.T, s *Store, sid int64, title string) Video {
	t.Helper()
	v, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: title, Venue: VenueStream})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustUser(t *testing.T, s *Store, id string) {
	t.Helper()
	if err := s.UpsertUser(ctx, User{DiscordID: id, Username: "user" + id}); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFileWALAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vq.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var fk int
	if err := s.db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
	sid := mustServer(t, s, "g1").ID
	if _, err := s.CreateSeries(ctx, Series{ServerID: sid, Title: "kept"}); err != nil {
		t.Fatal(err)
	}
	var applied1 string
	if err := s.db.QueryRowContext(ctx, `SELECT applied_at FROM schema_migrations WHERE version = 1`).Scan(&applied1); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s.Close()
	list, err := s.ListSeries(ctx, sid)
	if err != nil || len(list) != 1 || list[0].Title != "kept" {
		t.Fatalf("ListSeries after reopen = %v, %v", list, err)
	}
	var n int
	var applied2 string
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(applied_at) FROM schema_migrations`).Scan(&n, &applied2); err != nil {
		t.Fatal(err)
	}
	if n != 11 || applied2 != applied1 {
		t.Errorf("migrations re-applied: count=%d applied_at %q -> %q", n, applied1, applied2)
	}
}

func TestMemoryForeignKeysOn(t *testing.T) {
	s, sid := newStoreSrv(t)
	_, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: "x", Venue: VenueStream, SeriesID: ptr(int64(999))})
	if err == nil {
		t.Fatal("expected FK violation for unknown series")
	}
}

func TestUsersAndSessions(t *testing.T) {
	s := newStore(t)
	if err := s.UpsertUser(ctx, User{DiscordID: "1", Username: "a", GlobalName: "A", Avatar: "av"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := s.CreateSession(ctx, "h1", "1", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	u, err := s.UserBySession(ctx, "h1", now)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "a" || u.GlobalName != "A" || u.Avatar != "av" || u.CreatedAt.IsZero() {
		t.Errorf("user = %+v", u)
	}
	firstLogin := u.LastLoginAt

	if err := s.UpsertUser(ctx, User{DiscordID: "1", Username: "b", GlobalName: "B", Avatar: "av2"}); err != nil {
		t.Fatal(err)
	}
	u, err = s.UserBySession(ctx, "h1", now)
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "b" || u.GlobalName != "B" || u.Avatar != "av2" {
		t.Errorf("not updated: %+v", u)
	}
	if !u.LastLoginAt.After(firstLogin) {
		t.Errorf("last_login_at not bumped: %v -> %v", firstLogin, u.LastLoginAt)
	}

	if _, err := s.UserBySession(ctx, "nope", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session err = %v", err)
	}
	if err := s.DeleteSession(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySession(ctx, "h1", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted session err = %v", err)
	}
	if err := s.DeleteSession(ctx, "h1"); err != nil {
		t.Errorf("deleting missing session should be nil, got %v", err)
	}
}

func TestSessionExpiry(t *testing.T) {
	s := newStore(t)
	mustUser(t, s, "1")
	base := time.Date(2026, 1, 1, 12, 0, 0, 500, time.UTC)
	if err := s.CreateSession(ctx, "old", "1", base); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, "new", "1", base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UserBySession(ctx, "old", base.Add(-time.Second)); err != nil {
		t.Errorf("unexpired: %v", err)
	}
	// Expiry is exclusive: expires_at == now is expired.
	if _, err := s.UserBySession(ctx, "old", base); !errors.Is(err, ErrNotFound) {
		t.Errorf("at expiry err = %v", err)
	}
	if _, err := s.UserBySession(ctx, "old", base.Add(time.Second)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired err = %v", err)
	}
	if err := s.DeleteExpiredSessions(ctx, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sessions left = %d, want 1", n)
	}
	if _, err := s.UserBySession(ctx, "new", base.Add(time.Minute)); err != nil {
		t.Errorf("new session: %v", err)
	}
}

func TestDeleteUserCascadesSessions(t *testing.T) {
	s := newStore(t)
	mustUser(t, s, "1")
	if err := s.CreateSession(ctx, "h", "1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE discord_id = '1'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("sessions after user delete = %d", n)
	}
}

func TestCreateSessionUnknownUser(t *testing.T) {
	s := newStore(t)
	if err := s.CreateSession(ctx, "h", "ghost", time.Now().Add(time.Hour)); err == nil {
		t.Error("expected FK error")
	}
}

func TestSeriesCRUD(t *testing.T) {
	s, sid := newStoreSrv(t)
	empty, err := s.ListSeries(ctx, sid)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty ListSeries = %#v, %v", empty, err)
	}

	a, err := s.CreateSeries(ctx, Series{ServerID: sid, Title: "Zeta", Creator: "me", URL: "http://x", Notes: "n"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.DefaultVenue != VenueWatchParty || a.CreatedAt.IsZero() || !a.CreatedAt.Equal(a.UpdatedAt) {
		t.Errorf("created = %+v", a)
	}
	if a.Creator != "me" || a.URL != "http://x" || a.Notes != "n" {
		t.Errorf("fields lost: %+v", a)
	}
	b, err := s.CreateSeries(ctx, Series{ServerID: sid, Title: "alpha", DefaultVenue: VenueStream})
	if err != nil {
		t.Fatal(err)
	}
	if b.DefaultVenue != VenueStream {
		t.Errorf("venue = %q", b.DefaultVenue)
	}
	if _, err := s.CreateSeries(ctx, Series{ServerID: sid, Title: "bad", DefaultVenue: "bogus"}); err == nil {
		t.Error("expected CHECK failure for bad venue")
	}

	list, err := s.ListSeries(ctx, sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != b.ID || list[1].ID != a.ID {
		t.Errorf("list order = %+v", list)
	}

	got, err := s.GetSeries(ctx, sid, a.ID)
	if err != nil || got != a {
		t.Errorf("GetSeries = %+v, %v", got, err)
	}
	if _, err := s.GetSeries(ctx, sid, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSeries missing err = %v", err)
	}
}

func TestUpdateSeries(t *testing.T) {
	s, sid := newStoreSrv(t)
	a, _ := s.CreateSeries(ctx, Series{ServerID: sid, Title: "T", Creator: "C", Notes: "N"})
	time.Sleep(2 * time.Millisecond)

	got, err := s.UpdateSeries(ctx, sid, a.ID, SeriesPatch{Title: ptr("T2"), DefaultVenue: ptr(VenueStream), Notes: ptr("")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "T2" || got.DefaultVenue != VenueStream || got.Notes != "" || got.Creator != "C" {
		t.Errorf("patched = %+v", got)
	}
	if !got.UpdatedAt.After(a.UpdatedAt) || !got.CreatedAt.Equal(a.CreatedAt) {
		t.Errorf("timestamps: created %v updated %v (was %v)", got.CreatedAt, got.UpdatedAt, a.UpdatedAt)
	}

	same, err := s.UpdateSeries(ctx, sid, a.ID, SeriesPatch{})
	if err != nil || same != got {
		t.Errorf("empty patch = %+v, %v", same, err)
	}
	if _, err := s.UpdateSeries(ctx, sid, 999, SeriesPatch{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty patch missing err = %v", err)
	}
	if _, err := s.UpdateSeries(ctx, sid, 999, SeriesPatch{Title: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("patch missing err = %v", err)
	}
}

func TestDeleteSeriesNullsVideos(t *testing.T) {
	s, sid := newStoreSrv(t)
	sr, _ := s.CreateSeries(ctx, Series{ServerID: sid, Title: "S"})
	v, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: "ep", Venue: VenueStream, SeriesID: &sr.ID})
	if err != nil {
		t.Fatal(err)
	}
	if v.SeriesID == nil || *v.SeriesID != sr.ID {
		t.Fatalf("series_id = %v", v.SeriesID)
	}
	if err := s.DeleteSeries(ctx, sid, sr.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetVideo(ctx, sid, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.SeriesID != nil {
		t.Errorf("series_id = %v, want nil", *got.SeriesID)
	}
	if err := s.DeleteSeries(ctx, sid, sr.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v", err)
	}
}

func TestCreateVideo(t *testing.T) {
	s, sid := newStoreSrv(t)
	sched := time.Date(2026, 5, 6, 7, 8, 9, 123456789, time.FixedZone("x", 3600))
	v1, err := s.CreateVideo(ctx, Video{ServerID: sid,
		Title: "one", Venue: VenueWatchParty, Episode: "S1E2", URL: "u", ScheduledFor: &sched,
		Notes: "n", CreatedBy: "42",
	})
	if err != nil {
		t.Fatal(err)
	}
	if v1.Status != StatusQueued || v1.Position != 1 || v1.CreatedBy != "42" || v1.Episode != "S1E2" {
		t.Errorf("v1 = %+v", v1)
	}
	if v1.ScheduledFor == nil || !v1.ScheduledFor.Equal(sched) || v1.ScheduledFor.Location() != time.UTC {
		t.Errorf("scheduled_for = %v", v1.ScheduledFor)
	}
	if v1.ReactedAt != nil || v1.PatreonAt != nil || v1.YouTubeAt != nil {
		t.Errorf("unexpected timestamps: %+v", v1)
	}
	if v1.CreatedAt.IsZero() || !v1.CreatedAt.Equal(v1.UpdatedAt) {
		t.Errorf("timestamps: %v %v", v1.CreatedAt, v1.UpdatedAt)
	}

	v2, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: "two", Venue: VenueStream, Status: StatusReacted})
	if err != nil {
		t.Fatal(err)
	}
	if v2.Position != 2 || v2.Status != StatusReacted {
		t.Errorf("v2 = %+v", v2)
	}

	if _, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: "bad", Venue: "nope"}); err == nil {
		t.Error("expected CHECK failure for venue")
	}
	if _, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: "bad", Venue: VenueStream, Status: "nope"}); err == nil {
		t.Error("expected CHECK failure for status")
	}

	if _, err := s.GetVideo(ctx, sid, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetVideo missing err = %v", err)
	}
}

func TestUpdateVideoPatch(t *testing.T) {
	s, sid := newStoreSrv(t)
	sr, _ := s.CreateSeries(ctx, Series{ServerID: sid, Title: "S"})
	sched := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	v, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: "t", Venue: VenueStream, SeriesID: &sr.ID, ScheduledFor: &sched, Notes: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	reacted := time.Date(2026, 2, 3, 4, 5, 6, 7, time.UTC)
	pat := reacted.Add(time.Hour)
	yt := reacted.Add(2 * time.Hour)
	got, err := s.UpdateVideo(ctx, sid, v.ID, VideoPatch{
		Episode: ptr("9"), Title: ptr("new"), URL: ptr("http://u"), Venue: ptr(VenueWatchParty),
		Status: ptr(StatusYouTube), ReactedAt: &reacted, VODURL: ptr("vod"),
		PatreonURL: ptr("pu"), PatreonAt: &pat, YouTubeURL: ptr("yu"), YouTubeAt: &yt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Episode != "9" || got.Title != "new" || got.URL != "http://u" || got.Venue != VenueWatchParty ||
		got.Status != StatusYouTube || got.VODURL != "vod" || got.PatreonURL != "pu" || got.YouTubeURL != "yu" {
		t.Errorf("patched = %+v", got)
	}
	if got.ReactedAt == nil || !got.ReactedAt.Equal(reacted) || got.PatreonAt == nil || !got.PatreonAt.Equal(pat) ||
		got.YouTubeAt == nil || !got.YouTubeAt.Equal(yt) {
		t.Errorf("times = %v %v %v", got.ReactedAt, got.PatreonAt, got.YouTubeAt)
	}
	if got.Notes != "keep" || got.SeriesID == nil || got.ScheduledFor == nil || !got.ScheduledFor.Equal(sched) {
		t.Errorf("untouched fields changed: %+v", got)
	}
	if !got.UpdatedAt.After(v.UpdatedAt) {
		t.Errorf("updated_at not bumped")
	}

	got, err = s.UpdateVideo(ctx, sid, v.ID, VideoPatch{ClearSeries: true, ClearSchedule: true, Notes: ptr("")})
	if err != nil {
		t.Fatal(err)
	}
	if got.SeriesID != nil || got.ScheduledFor != nil || got.Notes != "" {
		t.Errorf("clears failed: %+v", got)
	}

	got, err = s.UpdateVideo(ctx, sid, v.ID, VideoPatch{SeriesID: &sr.ID, ScheduledFor: &sched})
	if err != nil {
		t.Fatal(err)
	}
	if got.SeriesID == nil || *got.SeriesID != sr.ID || got.ScheduledFor == nil || !got.ScheduledFor.Equal(sched) {
		t.Errorf("set after clear: %+v", got)
	}

	same, err := s.UpdateVideo(ctx, sid, v.ID, VideoPatch{})
	if err != nil || same.UpdatedAt != got.UpdatedAt {
		t.Errorf("empty patch = %+v, %v", same, err)
	}
	if _, err := s.UpdateVideo(ctx, sid, 999, VideoPatch{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty missing err = %v", err)
	}
	if _, err := s.UpdateVideo(ctx, sid, 999, VideoPatch{Title: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
	if _, err := s.UpdateVideo(ctx, sid, v.ID, VideoPatch{SeriesID: ptr(int64(999))}); err == nil {
		t.Error("expected FK error for unknown series")
	}
	if _, err := s.UpdateVideo(ctx, sid, v.ID, VideoPatch{Status: ptr(VideoStatus("bogus"))}); err == nil {
		t.Error("expected CHECK error for status")
	}
}

func TestDeleteVideo(t *testing.T) {
	s, sid := newStoreSrv(t)
	v := mustVideo(t, s, sid, "a")
	if err := s.DeleteVideo(ctx, sid, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVideo(ctx, sid, v.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete err = %v", err)
	}
	if err := s.DeleteVideo(ctx, sid, v.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v", err)
	}
}

func ids(vs []Video) []int64 {
	out := []int64{}
	for _, v := range vs {
		out = append(out, v.ID)
	}
	return out
}

func TestListVideosFiltersAndOrder(t *testing.T) {
	s, sid := newStoreSrv(t)
	empty, err := s.ListVideos(ctx, VideoFilter{ServerID: sid})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %#v, %v", empty, err)
	}

	sr, _ := s.CreateSeries(ctx, Series{ServerID: sid, Title: "S"})
	mk := func(title string, venue Venue, st VideoStatus, series *int64) Video {
		v, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: title, Venue: venue, Status: st, SeriesID: series})
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	a := mk("a", VenueStream, StatusQueued, nil)
	b := mk("b", VenueWatchParty, StatusReacted, &sr.ID)
	c := mk("c", VenueStream, StatusEditing, &sr.ID)
	d := mk("d", VenueWatchParty, StatusQueued, &sr.ID)

	tests := []struct {
		name string
		f    VideoFilter
		want []int64
	}{
		{"all", VideoFilter{ServerID: sid}, []int64{a.ID, b.ID, c.ID, d.ID}},
		{"one status", VideoFilter{ServerID: sid, Status: []VideoStatus{StatusQueued}}, []int64{a.ID, d.ID}},
		{"two statuses", VideoFilter{ServerID: sid, Status: []VideoStatus{StatusReacted, StatusEditing}}, []int64{b.ID, c.ID}},
		{"venue", VideoFilter{ServerID: sid, Venue: VenueStream}, []int64{a.ID, c.ID}},
		{"series", VideoFilter{ServerID: sid, SeriesID: sr.ID}, []int64{b.ID, c.ID, d.ID}},
		{"combined", VideoFilter{ServerID: sid, Status: []VideoStatus{StatusQueued}, Venue: VenueWatchParty, SeriesID: sr.ID}, []int64{d.ID}},
		{"none", VideoFilter{ServerID: sid, Status: []VideoStatus{StatusYouTube}}, []int64{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := s.ListVideos(ctx, tt.f)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || !slices.Equal(ids(got), tt.want) {
				t.Errorf("ids = %v, want %v", ids(got), tt.want)
			}
		})
	}
}

func TestReorderVideos(t *testing.T) {
	s, sid := newStoreSrv(t)
	a, b, c := mustVideo(t, s, sid, "a"), mustVideo(t, s, sid, "b"), mustVideo(t, s, sid, "c")
	if err := s.ReorderVideos(ctx, sid, []int64{c.ID, a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListVideos(ctx, VideoFilter{ServerID: sid})
	if !slices.Equal(ids(got), []int64{c.ID, a.ID, b.ID}) {
		t.Errorf("order = %v", ids(got))
	}
	for i, v := range got {
		if v.Position != int64(i) {
			t.Errorf("position[%d] = %d", i, v.Position)
		}
	}
	d := mustVideo(t, s, sid, "d")
	if d.Position != 3 {
		t.Errorf("new position = %d, want 3", d.Position)
	}
	if err := s.ReorderVideos(ctx, sid, nil); err != nil {
		t.Errorf("empty reorder: %v", err)
	}
}

func sug(sid int64, msg string, votes int64, posted time.Time) Suggestion {
	return Suggestion{
		ServerID: sid, DiscordMessageID: msg, ChannelID: "ch", AuthorID: "au", AuthorName: "author-" + msg,
		Content: "content-" + msg, ReactionCount: votes, PostedAt: posted,
	}
}

func suggIDs(t *testing.T, s *Store, f SuggestionFilter) []string {
	t.Helper()
	list, err := s.ListSuggestions(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, x := range list {
		out = append(out, x.DiscordMessageID)
	}
	return out
}

func TestSuggestionSorting(t *testing.T) {
	s, sid := newStoreSrv(t)
	empty, err := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty = %#v, %v", empty, err)
	}

	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	// Whole-second vs fractional timestamps must still sort chronologically.
	in := []Suggestion{
		sug(sid, "m1", 5, t0),
		sug(sid, "m2", 9, t0.Add(500*time.Millisecond)),
		sug(sid, "m3", 5, t0.Add(time.Second)),
		sug(sid, "m4", 0, t0.Add(-time.Hour)),
		sug(sid, "m5", 9, t0.Add(500*time.Millisecond)), // tie with m2 on both keys
	}
	for _, x := range in {
		if err := s.UpsertSuggestion(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		sort SuggestionSort
		want []string
	}{
		{"", []string{"m3", "m5", "m2", "m1", "m4"}},
		{SortNewest, []string{"m3", "m5", "m2", "m1", "m4"}},
		{SortOldest, []string{"m4", "m1", "m2", "m5", "m3"}},
		{SortVotes, []string{"m5", "m2", "m3", "m1", "m4"}},
	}
	for _, tt := range tests {
		got := suggIDs(t, s, SuggestionFilter{ServerID: sid, Sort: tt.sort})
		if !slices.Equal(got, tt.want) {
			t.Errorf("sort %q = %v, want %v", tt.sort, got, tt.want)
		}
	}
}

func TestSuggestionStatusFilter(t *testing.T) {
	s, sid := newStoreSrv(t)
	t0 := time.Now()
	for _, m := range []string{"a", "b", "c"} {
		if err := s.UpsertSuggestion(ctx, sug(sid, m, 0, t0)); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid, Sort: SortOldest})
	if _, err := s.SetSuggestionStatus(ctx, sid, all[0].ID, SuggestionDiscarded, "mod", t0); err != nil {
		t.Fatal(err)
	}
	if got := suggIDs(t, s, SuggestionFilter{ServerID: sid, Status: SuggestionPending, Sort: SortOldest}); !slices.Equal(got, []string{"b", "c"}) {
		t.Errorf("pending = %v", got)
	}
	if got := suggIDs(t, s, SuggestionFilter{ServerID: sid, Status: SuggestionDiscarded}); !slices.Equal(got, []string{"a"}) {
		t.Errorf("discarded = %v", got)
	}
	if got := suggIDs(t, s, SuggestionFilter{ServerID: sid, Status: SuggestionApproved}); len(got) != 0 {
		t.Errorf("approved = %v", got)
	}
}

func TestUpsertSuggestionPreservesReview(t *testing.T) {
	s, sid := newStoreSrv(t)
	posted := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	orig := sug(sid, "m1", 1, posted)
	if err := s.UpsertSuggestion(ctx, orig); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	id := list[0].ID
	first := list[0]
	if first.Status != SuggestionPending || first.ReviewedBy != "" || first.ReviewedAt != nil || first.VideoID != nil {
		t.Errorf("initial = %+v", first)
	}
	if !first.PostedAt.Equal(posted) || first.ChannelID != "ch" || first.AuthorID != "au" {
		t.Errorf("initial fields = %+v", first)
	}

	reviewedAt := posted.Add(time.Hour)
	if _, err := s.SetSuggestionStatus(ctx, sid, id, SuggestionDiscarded, "mod1", reviewedAt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)

	upd := sug(sid, "m1", 12, posted.Add(24*time.Hour))
	upd.Content = "edited"
	upd.AuthorName = "renamed"
	upd.ChannelID = "other"
	upd.AuthorID = "other"
	if err := s.UpsertSuggestion(ctx, upd); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSuggestion(ctx, sid, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "edited" || got.AuthorName != "renamed" || got.ReactionCount != 12 {
		t.Errorf("not updated: %+v", got)
	}
	if got.Status != SuggestionDiscarded || got.ReviewedBy != "mod1" || got.ReviewedAt == nil || !got.ReviewedAt.Equal(reviewedAt) {
		t.Errorf("review state overwritten: %+v", got)
	}
	if !got.PostedAt.Equal(posted) || got.ChannelID != "ch" || got.AuthorID != "au" {
		t.Errorf("immutable fields changed: %+v", got)
	}
	if !got.UpdatedAt.After(first.UpdatedAt) || !got.CreatedAt.Equal(first.CreatedAt) {
		t.Errorf("timestamps: %+v vs %+v", got, first)
	}
	if all, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid}); len(all) != 1 {
		t.Errorf("rows = %d, want 1", len(all))
	}
}

func TestSetSuggestionStatus(t *testing.T) {
	s, sid := newStoreSrv(t)
	if err := s.UpsertSuggestion(ctx, sug(sid, "m1", 0, time.Now())); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	id := list[0].ID
	now := time.Date(2026, 4, 1, 1, 2, 3, 4, time.UTC)

	got, err := s.SetSuggestionStatus(ctx, sid, id, SuggestionDiscarded, "mod", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != SuggestionDiscarded || got.ReviewedBy != "mod" || got.ReviewedAt == nil || !got.ReviewedAt.Equal(now) {
		t.Errorf("discarded = %+v", got)
	}

	got, err = s.SetSuggestionStatus(ctx, sid, id, SuggestionPending, "ignored", now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != SuggestionPending || got.ReviewedBy != "" || got.ReviewedAt != nil {
		t.Errorf("restored = %+v", got)
	}

	if _, err := s.SetSuggestionStatus(ctx, sid, 999, SuggestionDiscarded, "mod", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
	if _, err := s.GetSuggestion(ctx, sid, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("get missing err = %v", err)
	}
}

func TestPendingKeepsVideoID(t *testing.T) {
	s, sid := newStoreSrv(t)
	if err := s.UpsertSuggestion(ctx, sug(sid, "m1", 0, time.Now())); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	_, v, err := s.ApproveSuggestion(ctx, sid, list[0].ID, Video{Title: "t", Venue: VenueStream}, "mod", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SetSuggestionStatus(ctx, sid, list[0].ID, SuggestionPending, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got.VideoID == nil || *got.VideoID != v.ID {
		t.Errorf("video_id = %v, want %d", got.VideoID, v.ID)
	}
}

func TestApproveSuggestion(t *testing.T) {
	s, sid := newStoreSrv(t)
	mustVideo(t, s, sid, "existing")
	if err := s.UpsertSuggestion(ctx, sug(sid, "m1", 3, time.Now())); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	now := time.Date(2026, 5, 5, 5, 5, 5, 5, time.UTC)

	sg, v, err := s.ApproveSuggestion(ctx, sid, list[0].ID, Video{Title: "from suggestion", Venue: VenueWatchParty, CreatedBy: "mod"}, "mod", now)
	if err != nil {
		t.Fatal(err)
	}
	if v.ID == 0 || v.Status != StatusQueued || v.Position != 2 || v.Title != "from suggestion" {
		t.Errorf("video = %+v", v)
	}
	if sg.Status != SuggestionApproved || sg.ReviewedBy != "mod" || sg.ReviewedAt == nil || !sg.ReviewedAt.Equal(now) ||
		sg.VideoID == nil || *sg.VideoID != v.ID {
		t.Errorf("suggestion = %+v", sg)
	}
	stored, _ := s.GetSuggestion(ctx, sid, sg.ID)
	if stored.Status != SuggestionApproved || stored.VideoID == nil || *stored.VideoID != v.ID {
		t.Errorf("stored = %+v", stored)
	}

	// Deleting the video leaves the suggestion with video_id cleared.
	if err := s.DeleteVideo(ctx, sid, v.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ = s.GetSuggestion(ctx, sid, sg.ID)
	if stored.VideoID != nil {
		t.Errorf("video_id = %v after video delete", *stored.VideoID)
	}
}

func TestApproveSuggestionNotFound(t *testing.T) {
	s, sid := newStoreSrv(t)
	_, _, err := s.ApproveSuggestion(ctx, sid, 999, Video{Title: "t", Venue: VenueStream}, "mod", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if vs, _ := s.ListVideos(ctx, VideoFilter{ServerID: sid}); len(vs) != 0 {
		t.Errorf("video created despite missing suggestion: %v", vs)
	}
}

func TestApproveSuggestionRollsBack(t *testing.T) {
	s, sid := newStoreSrv(t)
	if err := s.UpsertSuggestion(ctx, sug(sid, "m1", 0, time.Now())); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	_, _, err := s.ApproveSuggestion(ctx, sid, list[0].ID, Video{Title: "t", Venue: "bogus"}, "mod", time.Now())
	if err == nil {
		t.Fatal("expected error")
	}
	if vs, _ := s.ListVideos(ctx, VideoFilter{ServerID: sid}); len(vs) != 0 {
		t.Errorf("videos = %v", vs)
	}
	got, _ := s.GetSuggestion(ctx, sid, list[0].ID)
	if got.Status != SuggestionPending || got.VideoID != nil {
		t.Errorf("suggestion changed: %+v", got)
	}
	// The store must still be usable (connection released).
	if _, err := s.ListSeries(ctx, sid); err != nil {
		t.Errorf("after rollback: %v", err)
	}
}

func TestKV(t *testing.T) {
	s := newStore(t)
	if v, err := s.GetKV(ctx, "k"); err != nil || v != "" {
		t.Errorf("absent = %q, %v", v, err)
	}
	if err := s.SetKV(ctx, "k", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKV(ctx, "k", "2"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.GetKV(ctx, "k"); err != nil || v != "2" {
		t.Errorf("k = %q, %v", v, err)
	}
	if v, _ := s.GetKV(ctx, "other"); v != "" {
		t.Errorf("other = %q", v)
	}
}

// --- servers ---

func TestServerCRUD(t *testing.T) {
	s := newStore(t)
	if list, err := s.ListServers(ctx); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("empty ListServers = %#v, %v", list, err)
	}
	b, err := s.CreateServer(ctx, Server{GuildID: "g2", Name: "beta", GuildName: "Beta Guild", Icon: "ic", CreatedBy: "u1",
		ModRoleIDs: []string{"r1", "r2"}, AllowedUserIDs: []string{"u9"}, SuggestionsChannelID: "c1", SuggestionsCursor: "55"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateServer(ctx, Server{GuildID: "g1", Name: "Alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if b.ID == 0 || b.GuildName != "Beta Guild" || b.Icon != "ic" || b.CreatedBy != "u1" || b.SuggestionsChannelID != "c1" ||
		b.SuggestionsCursor != "55" || b.CreatedAt.IsZero() || !b.CreatedAt.Equal(b.UpdatedAt) {
		t.Errorf("created = %+v", b)
	}
	// nil lists are stored as [] and read back as empty non-nil slices.
	if a.ModRoleIDs == nil || len(a.ModRoleIDs) != 0 || a.AllowedUserIDs == nil || len(a.AllowedUserIDs) != 0 {
		t.Errorf("lists = %#v %#v", a.ModRoleIDs, a.AllowedUserIDs)
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT mod_role_ids FROM servers WHERE id = ?`, a.ID).Scan(&raw); err != nil || raw != "[]" {
		t.Errorf("raw = %q, %v", raw, err)
	}
	if !slices.Equal(b.ModRoleIDs, []string{"r1", "r2"}) || !slices.Equal(b.AllowedUserIDs, []string{"u9"}) {
		t.Errorf("lists round-trip: %+v", b)
	}

	list, err := s.ListServers(ctx)
	if err != nil || len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Errorf("list order = %+v, %v", list, err)
	}
	got, err := s.GetServer(ctx, b.ID)
	if err != nil || !slices.Equal(got.ModRoleIDs, b.ModRoleIDs) || got.Name != "beta" {
		t.Errorf("GetServer = %+v, %v", got, err)
	}
	got, err = s.GetServerByGuild(ctx, "g1")
	if err != nil || got.ID != a.ID {
		t.Errorf("GetServerByGuild = %+v, %v", got, err)
	}
	if _, err := s.GetServer(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetServer missing err = %v", err)
	}
	if _, err := s.GetServerByGuild(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetServerByGuild missing err = %v", err)
	}

	if _, err := s.CreateServer(ctx, Server{GuildID: "g1", Name: "dup"}); !errors.Is(err, ErrConflict) {
		t.Errorf("duplicate guild err = %v", err)
	}

	time.Sleep(2 * time.Millisecond)
	upd, err := s.UpdateServer(ctx, a.ID, ServerPatch{Name: ptr("Gamma"),
		ModRoleIDs: ptr([]string{"x"}), AllowedUserIDs: ptr([]string{"y", "z"})})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Name != "Gamma" || !slices.Equal(upd.ModRoleIDs, []string{"x"}) || !slices.Equal(upd.AllowedUserIDs, []string{"y", "z"}) ||
		!upd.UpdatedAt.After(a.UpdatedAt) || !upd.CreatedAt.Equal(a.CreatedAt) {
		t.Errorf("updated = %+v", upd)
	}
	// An explicit empty list clears; a nil pointer leaves unchanged.
	upd, err = s.UpdateServer(ctx, a.ID, ServerPatch{ModRoleIDs: ptr([]string(nil))})
	if err != nil || upd.ModRoleIDs == nil || len(upd.ModRoleIDs) != 0 || !slices.Equal(upd.AllowedUserIDs, []string{"y", "z"}) {
		t.Errorf("cleared = %+v, %v", upd, err)
	}
	same, err := s.UpdateServer(ctx, a.ID, ServerPatch{})
	if err != nil || same.UpdatedAt != upd.UpdatedAt {
		t.Errorf("empty patch = %+v, %v", same, err)
	}
	if _, err := s.UpdateServer(ctx, 999, ServerPatch{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty patch missing err = %v", err)
	}
	if _, err := s.UpdateServer(ctx, 999, ServerPatch{Name: ptr("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("patch missing err = %v", err)
	}

	if err := s.DeleteServer(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetServer(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("get after delete err = %v", err)
	}
	if err := s.DeleteServer(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second delete err = %v", err)
	}
}

func TestSuggestionsCursorAndChannelReset(t *testing.T) {
	s := newStore(t)
	srv, err := s.CreateServer(ctx, Server{GuildID: "g", Name: "n", SuggestionsChannelID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSuggestionsCursor(ctx, srv.ID, "123"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSuggestionsCursor(ctx, 999, "1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
	cursor := func() string {
		g, err := s.GetServer(ctx, srv.ID)
		if err != nil {
			t.Fatal(err)
		}
		return g.SuggestionsCursor
	}
	if c := cursor(); c != "123" {
		t.Fatalf("cursor = %q", c)
	}
	// Same channel and other fields keep the cursor.
	if _, err := s.UpdateServer(ctx, srv.ID, ServerPatch{SuggestionsChannelID: ptr("c1"), Name: ptr("n2")}); err != nil {
		t.Fatal(err)
	}
	if c := cursor(); c != "123" {
		t.Errorf("cursor after same channel = %q", c)
	}
	got, err := s.UpdateServer(ctx, srv.ID, ServerPatch{SuggestionsChannelID: ptr("c2")})
	if err != nil {
		t.Fatal(err)
	}
	if got.SuggestionsChannelID != "c2" || got.SuggestionsCursor != "" {
		t.Errorf("after channel change = %+v", got)
	}
}

func TestAccessCache(t *testing.T) {
	s := newStore(t)
	a, b := mustServer(t, s, "ga"), mustServer(t, s, "gb")
	if _, found, err := s.GetAccess(ctx, a.ID, "u"); err != nil || found {
		t.Fatalf("empty GetAccess found=%v err=%v", found, err)
	}
	t1 := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	if err := s.SetAccess(ctx, a.ID, "u", AccessEntry{Allowed: true, CanManage: true, CheckedAt: t1}); err != nil {
		t.Fatal(err)
	}
	e, found, err := s.GetAccess(ctx, a.ID, "u")
	if err != nil || !e.Allowed || !e.CanManage || !found || !e.CheckedAt.Equal(t1) {
		t.Errorf("GetAccess = %+v %v %v", e, found, err)
	}
	// Allowed without manage round-trips too.
	if err := s.SetAccess(ctx, a.ID, "v", AccessEntry{Allowed: true, CheckedAt: t1}); err != nil {
		t.Fatal(err)
	}
	if e, found, err := s.GetAccess(ctx, a.ID, "v"); err != nil || !found || !e.Allowed || e.CanManage {
		t.Errorf("allowed-only GetAccess = %+v %v %v", e, found, err)
	}
	// Upsert.
	t2 := t1.Add(time.Hour)
	if err := s.SetAccess(ctx, a.ID, "u", AccessEntry{Allowed: false, CanManage: false, CheckedAt: t2}); err != nil {
		t.Fatal(err)
	}
	e, found, err = s.GetAccess(ctx, a.ID, "u")
	if err != nil || e.Allowed || e.CanManage || !found || !e.CheckedAt.Equal(t2) {
		t.Errorf("after upsert = %+v %v %v", e, found, err)
	}
	if err := s.SetAccess(ctx, a.ID, "u", AccessEntry{CanManage: true, CheckedAt: t2}); err != nil {
		t.Fatal(err)
	}
	if e, _, _ := s.GetAccess(ctx, a.ID, "u"); e.Allowed || !e.CanManage {
		t.Errorf("manage-only upsert = %+v", e)
	}
	if _, found, _ := s.GetAccess(ctx, b.ID, "u"); found {
		t.Error("access leaked across servers")
	}
	if err := s.SetAccess(ctx, 999, "u", AccessEntry{Allowed: true, CheckedAt: t1}); err == nil {
		t.Error("expected FK error for unknown server")
	}

	// Changing roles or allowed users clears that server's cache only.
	for _, p := range []ServerPatch{{ModRoleIDs: ptr([]string{"r"})}, {AllowedUserIDs: ptr([]string{"x"})},
		{ClipEditorRoleIDs: ptr([]string{"c"})}, {VideoEditorRoleIDs: ptr([]string{"v"})}} {
		for _, sv := range []Server{a, b} {
			if err := s.SetAccess(ctx, sv.ID, "u", AccessEntry{Allowed: true, CanManage: true, CheckedAt: t1}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.UpdateServer(ctx, a.ID, p); err != nil {
			t.Fatal(err)
		}
		if _, found, _ := s.GetAccess(ctx, a.ID, "u"); found {
			t.Errorf("cache not cleared by %+v", p)
		}
		if _, found, _ := s.GetAccess(ctx, b.ID, "u"); !found {
			t.Errorf("other server's cache cleared by %+v", p)
		}
	}
	// Other changes keep the cache.
	if err := s.SetAccess(ctx, a.ID, "u", AccessEntry{Allowed: true, CheckedAt: t1}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateServer(ctx, a.ID, ServerPatch{Name: ptr("renamed"), SuggestionsChannelID: ptr("c")}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.GetAccess(ctx, a.ID, "u"); !found {
		t.Error("cache cleared by unrelated change")
	}
}

func TestUserGuilds(t *testing.T) {
	s := newStore(t)
	for _, id := range []string{"u1", "u2"} {
		if err := s.UpsertUser(ctx, User{DiscordID: id, Username: id}); err != nil {
			t.Fatal(err)
		}
	}
	if l, err := s.ListUserGuilds(ctx, "u1"); err != nil || l == nil || len(l) != 0 {
		t.Fatalf("empty list = %#v, %v", l, err)
	}
	now := time.Date(2026, 5, 6, 7, 8, 9, 10, time.UTC)
	first := []UserGuild{
		{GuildID: "g3", Name: "beta", Icon: "i3", CanManage: true},
		{GuildID: "g2", Name: "Alpha"},
		{GuildID: "g1", Name: "alpha", CanManage: true},
		{GuildID: "g4", Name: "Zed"},
	}
	if err := s.ReplaceUserGuilds(ctx, "u1", first, now); err != nil {
		t.Fatal(err)
	}
	other := []UserGuild{{GuildID: "g9", Name: "other", CanManage: true}}
	if err := s.ReplaceUserGuilds(ctx, "u2", other, now); err != nil {
		t.Fatal(err)
	}
	// Case-insensitive name order, then guild_id.
	want := []UserGuild{first[2], first[1], first[0], first[3]}
	got, err := s.ListUserGuilds(ctx, "u1")
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("list = %+v, %v; want %+v", got, err, want)
	}
	var upd string
	if err := s.db.QueryRowContext(ctx, `SELECT updated_at FROM user_guilds WHERE user_id = 'u1' AND guild_id = 'g1'`).Scan(&upd); err != nil || upd != fmtTime(now) {
		t.Errorf("updated_at = %q, %v", upd, err)
	}

	// Replace semantics: old rows gone, new ones present.
	second := []UserGuild{{GuildID: "g1", Name: "renamed"}, {GuildID: "g5", Name: "new", CanManage: true}}
	if err := s.ReplaceUserGuilds(ctx, "u1", second, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListUserGuilds(ctx, "u1")
	if want := []UserGuild{second[1], second[0]}; !slices.Equal(got, want) {
		t.Errorf("after replace = %+v, want %+v", got, want)
	}
	if got, _ := s.ListUserGuilds(ctx, "u2"); !slices.Equal(got, other) {
		t.Errorf("other user changed: %+v", got)
	}
	// Replacing with nothing clears.
	if err := s.ReplaceUserGuilds(ctx, "u1", nil, now); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListUserGuilds(ctx, "u1"); err != nil || got == nil || len(got) != 0 {
		t.Errorf("after clear = %#v, %v", got, err)
	}

	// Cascade when the user is deleted.
	if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE discord_id = 'u2'`); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM user_guilds WHERE user_id = 'u2'`); n != 0 {
		t.Errorf("user_guilds not cascaded: %d", n)
	}
}

func count(t *testing.T, s *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestDeleteServerCascades(t *testing.T) {
	s := newStore(t)
	a, b := mustServer(t, s, "ga"), mustServer(t, s, "gb")
	for _, sv := range []Server{a, b} {
		sr, err := s.CreateSeries(ctx, Series{ServerID: sv.ID, Title: "s"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateVideo(ctx, Video{ServerID: sv.ID, Title: "v", Venue: VenueStream, SeriesID: &sr.ID}); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertSuggestion(ctx, sug(sv.ID, "m"+sv.GuildID, 0, time.Now())); err != nil {
			t.Fatal(err)
		}
		if err := s.SetAccess(ctx, sv.ID, "u", AccessEntry{Allowed: true, CanManage: true, CheckedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteServer(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"series", "videos", "suggestions", "server_access"} {
		if n := count(t, s, `SELECT COUNT(*) FROM `+table+` WHERE server_id = ?`, a.ID); n != 0 {
			t.Errorf("%s rows left for deleted server = %d", table, n)
		}
		if n := count(t, s, `SELECT COUNT(*) FROM `+table+` WHERE server_id = ?`, b.ID); n != 1 {
			t.Errorf("%s rows for other server = %d, want 1", table, n)
		}
	}
}

func TestCreateRequiresServerID(t *testing.T) {
	s, _ := newStoreSrv(t)
	if _, err := s.CreateSeries(ctx, Series{Title: "x"}); err == nil {
		t.Error("CreateSeries without server_id should fail")
	}
	if _, err := s.CreateVideo(ctx, Video{Title: "x", Venue: VenueStream}); err == nil {
		t.Error("CreateVideo without server_id should fail")
	}
	if err := s.UpsertSuggestion(ctx, sug(0, "m", 0, time.Now())); err == nil {
		t.Error("UpsertSuggestion without server_id should fail")
	}
}

func TestVideoPositionPerServer(t *testing.T) {
	s := newStore(t)
	a, b := mustServer(t, s, "ga"), mustServer(t, s, "gb")
	mustVideo(t, s, a.ID, "a1")
	mustVideo(t, s, a.ID, "a2")
	if v := mustVideo(t, s, b.ID, "b1"); v.Position != 1 || v.ServerID != b.ID {
		t.Errorf("b1 = %+v", v)
	}
	if v := mustVideo(t, s, a.ID, "a3"); v.Position != 3 || v.ServerID != a.ID {
		t.Errorf("a3 = %+v", v)
	}
}

func TestCrossServerIsolation(t *testing.T) {
	s := newStore(t)
	a, b := mustServer(t, s, "ga").ID, mustServer(t, s, "gb").ID
	now := time.Now()

	sr, err := s.CreateSeries(ctx, Series{ServerID: a, Title: "S"})
	if err != nil {
		t.Fatal(err)
	}
	v1, v2 := mustVideo(t, s, a, "v1"), mustVideo(t, s, a, "v2")
	if err := s.UpsertSuggestion(ctx, sug(a, "m1", 1, now)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertSuggestion(ctx, sug(a, "m2", 1, now)); err != nil {
		t.Fatal(err)
	}
	sgs, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: a})
	sg1, sg2 := sgs[0], sgs[1]
	// A video in b, so reorder can mix ids.
	bv := mustVideo(t, s, b, "bv")

	beforeSeries, _ := s.GetSeries(ctx, a, sr.ID)
	beforeV1, _ := s.GetVideo(ctx, a, v1.ID)
	beforeSg, _ := s.GetSuggestion(ctx, a, sg1.ID)

	// Series.
	if l, err := s.ListSeries(ctx, b); err != nil || len(l) != 0 {
		t.Errorf("ListSeries(b) = %v, %v", l, err)
	}
	if _, err := s.GetSeries(ctx, b, sr.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSeries err = %v", err)
	}
	if _, err := s.UpdateSeries(ctx, b, sr.ID, SeriesPatch{Title: ptr("hacked")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateSeries err = %v", err)
	}
	if _, err := s.UpdateSeries(ctx, b, sr.ID, SeriesPatch{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty UpdateSeries err = %v", err)
	}
	if err := s.DeleteSeries(ctx, b, sr.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteSeries err = %v", err)
	}

	// Videos.
	if l, err := s.ListVideos(ctx, VideoFilter{ServerID: b}); err != nil || !slices.Equal(ids(l), []int64{bv.ID}) {
		t.Errorf("ListVideos(b) = %v, %v", ids(l), err)
	}
	if l, err := s.ListVideos(ctx, VideoFilter{ServerID: b, SeriesID: sr.ID}); err != nil || len(l) != 0 {
		t.Errorf("ListVideos(b, series) = %v, %v", l, err)
	}
	if _, err := s.GetVideo(ctx, b, v1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetVideo err = %v", err)
	}
	if _, err := s.UpdateVideo(ctx, b, v1.ID, VideoPatch{Title: ptr("hacked")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateVideo err = %v", err)
	}
	if _, err := s.UpdateVideo(ctx, b, v1.ID, VideoPatch{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty UpdateVideo err = %v", err)
	}
	if err := s.DeleteVideo(ctx, b, v1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteVideo err = %v", err)
	}
	// Reorder with a foreign id fails and changes nothing, from either side.
	if err := s.ReorderVideos(ctx, b, []int64{bv.ID, v1.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReorderVideos(b) err = %v", err)
	}
	if err := s.ReorderVideos(ctx, a, []int64{v2.ID, v1.ID, bv.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("ReorderVideos(a) err = %v", err)
	}
	if l, _ := s.ListVideos(ctx, VideoFilter{ServerID: a}); !slices.Equal(ids(l), []int64{v1.ID, v2.ID}) {
		t.Errorf("a order changed: %v", ids(l))
	}
	if got, _ := s.GetVideo(ctx, b, bv.ID); got.Position != bv.Position {
		t.Errorf("b position changed: %d -> %d", bv.Position, got.Position)
	}

	// Suggestions.
	if l, err := s.ListSuggestions(ctx, SuggestionFilter{ServerID: b}); err != nil || len(l) != 0 {
		t.Errorf("ListSuggestions(b) = %v, %v", l, err)
	}
	if _, err := s.GetSuggestion(ctx, b, sg1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetSuggestion err = %v", err)
	}
	if _, err := s.SetSuggestionStatus(ctx, b, sg1.ID, SuggestionDiscarded, "mod", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetSuggestionStatus err = %v", err)
	}
	if _, _, err := s.ApproveSuggestion(ctx, b, sg2.ID, Video{Title: "t", Venue: VenueStream}, "mod", now); !errors.Is(err, ErrNotFound) {
		t.Errorf("ApproveSuggestion err = %v", err)
	}
	// Upserting a message ID owned by another server must not modify it.
	other := sug(b, "m1", 99, now)
	other.Content = "hacked"
	if err := s.UpsertSuggestion(ctx, other); err != nil {
		t.Fatal(err)
	}

	// Nothing changed in a.
	if got, _ := s.GetSeries(ctx, a, sr.ID); got != beforeSeries {
		t.Errorf("series changed: %+v", got)
	}
	if got, _ := s.GetVideo(ctx, a, v1.ID); got.Title != beforeV1.Title || got.UpdatedAt != beforeV1.UpdatedAt {
		t.Errorf("video changed: %+v", got)
	}
	got, _ := s.GetSuggestion(ctx, a, sg1.ID)
	if got.Status != beforeSg.Status || got.Content != beforeSg.Content || got.ReactionCount != beforeSg.ReactionCount {
		t.Errorf("suggestion changed: %+v", got)
	}
	if got, _ := s.GetSuggestion(ctx, a, sg2.ID); got.Status != SuggestionPending || got.VideoID != nil {
		t.Errorf("sg2 changed: %+v", got)
	}
	if l, _ := s.ListVideos(ctx, VideoFilter{ServerID: a}); len(l) != 2 {
		t.Errorf("videos in a = %d", len(l))
	}
	if l, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: b}); len(l) != 0 {
		t.Errorf("suggestion leaked into b: %v", l)
	}
}

func TestApproveCreatesVideoInSameServer(t *testing.T) {
	s := newStore(t)
	a, b := mustServer(t, s, "ga").ID, mustServer(t, s, "gb").ID
	mustVideo(t, s, a, "a1")
	if err := s.UpsertSuggestion(ctx, sug(b, "m", 0, time.Now())); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: b})
	// A wrong ServerID on the template video is overridden.
	sg, v, err := s.ApproveSuggestion(ctx, b, list[0].ID, Video{ServerID: a, Title: "t", Venue: VenueStream}, "mod", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v.ServerID != b || v.Position != 1 || sg.ServerID != b || sg.VideoID == nil || *sg.VideoID != v.ID {
		t.Errorf("sg=%+v v=%+v", sg, v)
	}
	if l, _ := s.ListVideos(ctx, VideoFilter{ServerID: a}); len(l) != 1 {
		t.Errorf("videos in a = %d, want 1", len(l))
	}
}

// openLegacy builds a database at path containing only migration 001.
func openLegacy(t *testing.T, path string) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	s := &Store{db: db}
	if err := s.migrateUpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestClaimUnassigned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	old := openLegacy(t, path)
	now := fmtTime(time.Now())
	for _, q := range []string{
		`INSERT INTO series (id, title, created_at, updated_at) VALUES (1, 'old series', '` + now + `', '` + now + `')`,
		`INSERT INTO videos (id, series_id, title, venue, position, created_at, updated_at) VALUES (1, 1, 'old video', 'stream', 1, '` + now + `', '` + now + `')`,
		`INSERT INTO videos (id, title, venue, position, created_at, updated_at) VALUES (2, 'old video 2', 'stream', 2, '` + now + `', '` + now + `')`,
		`INSERT INTO suggestions (id, discord_message_id, channel_id, author_id, author_name, content, posted_at, created_at, updated_at)
		 VALUES (1, 'm1', 'c', 'a', 'n', 'x', '` + now + `', '` + now + `', '` + now + `')`,
		`INSERT INTO kv (key, value) VALUES ('suggestions_last_message_id', '777')`,
	} {
		if _, err := old.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	// Opening applies 002 cleanly on top of existing rows.
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	defer s.Close()
	if n := count(t, s, `SELECT COUNT(*) FROM videos WHERE server_id IS NULL`); n != 2 {
		t.Fatalf("unassigned videos = %d, want 2", n)
	}

	in := Server{GuildID: "g1", Name: "Legacy", ModRoleIDs: []string{"r"}, SuggestionsChannelID: "c"}
	srv, n, err := s.ClaimUnassigned(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Errorf("claimed = %d, want 4", n)
	}
	if srv.ID == 0 || srv.GuildID != "g1" || srv.Name != "Legacy" || srv.SuggestionsCursor != "777" ||
		!slices.Equal(srv.ModRoleIDs, []string{"r"}) {
		t.Errorf("server = %+v", srv)
	}
	if v, _ := s.GetKV(ctx, "suggestions_last_message_id"); v != "" {
		t.Errorf("kv key not deleted: %q", v)
	}
	if l, err := s.ListSeries(ctx, srv.ID); err != nil || len(l) != 1 || l[0].ServerID != srv.ID {
		t.Errorf("series = %+v, %v", l, err)
	}
	vs, err := s.ListVideos(ctx, VideoFilter{ServerID: srv.ID})
	if err != nil || len(vs) != 2 || vs[0].ServerID != srv.ID {
		t.Errorf("videos = %+v, %v", vs, err)
	}
	if l, err := s.ListSuggestions(ctx, SuggestionFilter{ServerID: srv.ID}); err != nil || len(l) != 1 || l[0].ServerID != srv.ID {
		t.Errorf("suggestions = %+v, %v", l, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM series WHERE server_id IS NULL`) +
		count(t, s, `SELECT COUNT(*) FROM videos WHERE server_id IS NULL`) +
		count(t, s, `SELECT COUNT(*) FROM suggestions WHERE server_id IS NULL`); n != 0 {
		t.Errorf("still unassigned: %d", n)
	}
	// New videos continue after the claimed positions.
	if v := mustVideo(t, s, srv.ID, "new"); v.Position != 3 {
		t.Errorf("new position = %d, want 3", v.Position)
	}

	// Second call reuses the server and claims nothing.
	srv2, n, err := s.ClaimUnassigned(ctx, in)
	if err != nil || n != 0 || srv2.ID != srv.ID || srv2.SuggestionsCursor != "777" {
		t.Errorf("second claim = %+v, %d, %v", srv2, n, err)
	}
	if l, _ := s.ListServers(ctx); len(l) != 1 {
		t.Errorf("servers = %d, want 1", len(l))
	}
}

func TestClaimUnassignedKeepsExistingCursor(t *testing.T) {
	s := newStore(t)
	srv, err := s.CreateServer(ctx, Server{GuildID: "g", Name: "n", SuggestionsCursor: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetKV(ctx, "suggestions_last_message_id", "9"); err != nil {
		t.Fatal(err)
	}
	got, n, err := s.ClaimUnassigned(ctx, Server{GuildID: "g", Name: "other"})
	if err != nil || n != 0 || got.ID != srv.ID || got.SuggestionsCursor != "5" || got.Name != "n" {
		t.Errorf("got %+v, %d, %v", got, n, err)
	}
	if v, _ := s.GetKV(ctx, "suggestions_last_message_id"); v != "9" {
		t.Errorf("kv = %q, want untouched", v)
	}
}

func TestMigration003WithExistingAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.db")
	old := openLegacy(t, path)
	if err := old.migrateUpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	srv, err := oldServer(old)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.UpsertUser(ctx, User{DiscordID: "u", Username: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(ctx,
		`INSERT INTO server_access (server_id, user_id, allowed, checked_at) VALUES (?, 'u', 1, ?)`, srv.ID, fmtTime(time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open with 001+002 and access rows: %v", err)
	}
	defer s.Close()
	if _, found, err := s.GetAccess(ctx, srv.ID, "u"); err != nil || found {
		t.Errorf("stale access entry kept: found=%v err=%v", found, err)
	}
	if err := s.ReplaceUserGuilds(ctx, "u", []UserGuild{{GuildID: "g", Name: "n"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestYouTubeFieldsRoundTrip(t *testing.T) {
	s, sid := newStoreSrv(t)
	ser, err := s.CreateSeries(ctx, Series{ServerID: sid, Title: "S", YouTubePlaylistID: "PL1", ThumbnailVideoID: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if ser.YouTubePlaylistID != "PL1" || ser.ThumbnailVideoID != "abc" {
		t.Errorf("created series = %+v", ser)
	}
	ser, err = s.UpdateSeries(ctx, sid, ser.ID, SeriesPatch{YouTubePlaylistID: ptr("PL2"), ThumbnailVideoID: ptr("")})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSeries(ctx, sid, ser.ID); got.YouTubePlaylistID != "PL2" || got.ThumbnailVideoID != "" {
		t.Errorf("patched series = %+v", got)
	}
	if list, _ := s.ListSeries(ctx, sid); len(list) != 1 || list[0].YouTubePlaylistID != "PL2" {
		t.Errorf("list series = %+v", list)
	}

	v, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: "v", Venue: VenueStream,
		YouTubeVideoID: "vid1", ChannelTitle: "Chan", DurationSeconds: 123})
	if err != nil {
		t.Fatal(err)
	}
	if v.YouTubeVideoID != "vid1" || v.ChannelTitle != "Chan" || v.DurationSeconds != 123 {
		t.Errorf("created video = %+v", v)
	}
	if _, err := s.UpdateVideo(ctx, sid, v.ID, VideoPatch{YouTubeVideoID: ptr("vid2"), ChannelTitle: ptr("C2"), DurationSeconds: ptr(int64(0))}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetVideo(ctx, sid, v.ID)
	if got.YouTubeVideoID != "vid2" || got.ChannelTitle != "C2" || got.DurationSeconds != 0 {
		t.Errorf("patched video = %+v", got)
	}
	if list, _ := s.ListVideos(ctx, VideoFilter{ServerID: sid}); len(list) != 1 || list[0].YouTubeVideoID != "vid2" {
		t.Errorf("list videos = %+v", list)
	}
}

func TestApproveSuggestionKeepsYouTubeFields(t *testing.T) {
	s, sid := newStoreSrv(t)
	if err := s.UpsertSuggestion(ctx, Suggestion{ServerID: sid, DiscordMessageID: "m1", PostedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	sugs, err := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	if err != nil || len(sugs) != 1 {
		t.Fatalf("suggestions = %v, %v", sugs, err)
	}
	_, v, err := s.ApproveSuggestion(ctx, sid, sugs[0].ID, Video{Title: "x", Venue: VenueStream,
		YouTubeVideoID: "yy", ChannelTitle: "ch", DurationSeconds: 9}, "rev", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if v.YouTubeVideoID != "yy" || v.ChannelTitle != "ch" || v.DurationSeconds != 9 {
		t.Errorf("video = %+v", v)
	}
}

func TestCreateVideos(t *testing.T) {
	s, sid := newStoreSrv(t)
	mustVideo(t, s, sid, "old1")
	mustVideo(t, s, sid, "old2")
	if got, err := s.CreateVideos(ctx, nil); err != nil || got == nil || len(got) != 0 {
		t.Errorf("empty = %v, %v", got, err)
	}
	got, err := s.CreateVideos(ctx, []Video{
		{ServerID: sid, Title: "n1", Venue: VenueStream, YouTubeVideoID: "a"},
		{ServerID: sid, Title: "n2", Venue: VenueStream, YouTubeVideoID: "b"},
		{ServerID: sid, Title: "n3", Venue: VenueStream},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d", len(got))
	}
	for i, v := range got {
		if v.ID == 0 || v.CreatedAt.IsZero() || v.Position != int64(3+i) || v.Title != "n"+string(rune('1'+i)) {
			t.Errorf("got[%d] = %+v", i, v)
		}
	}
	if got[0].YouTubeVideoID != "a" {
		t.Errorf("youtube id lost: %+v", got[0])
	}
}

func TestCreateVideosErrorsRollBack(t *testing.T) {
	s := newStore(t)
	a, b := mustServer(t, s, "ga").ID, mustServer(t, s, "gb").ID
	if _, err := s.CreateVideos(ctx, []Video{
		{ServerID: a, Title: "x", Venue: VenueStream},
		{ServerID: b, Title: "y", Venue: VenueStream},
	}); err == nil {
		t.Error("mixed servers: want error")
	}
	if _, err := s.CreateVideos(ctx, []Video{{Title: "x", Venue: VenueStream}}); err == nil {
		t.Error("zero server: want error")
	}
	// Failure on the second insert rolls back the first.
	if _, err := s.CreateVideos(ctx, []Video{
		{ServerID: a, Title: "ok", Venue: VenueStream},
		{ServerID: a, Title: "bad", Venue: "nope"},
	}); err == nil {
		t.Error("invalid venue: want error")
	}
	for _, sid := range []int64{a, b} {
		if vs, _ := s.ListVideos(ctx, VideoFilter{ServerID: sid}); len(vs) != 0 {
			t.Errorf("server %d has %d videos, want 0", sid, len(vs))
		}
	}
}

func TestCreateSeriesWithVideos(t *testing.T) {
	s := newStore(t)
	a, b := mustServer(t, s, "ga").ID, mustServer(t, s, "gb").ID
	mustVideo(t, s, a, "existing")
	ser, vs, err := s.CreateSeriesWithVideos(ctx, Series{ServerID: a, Title: "S", YouTubePlaylistID: "PL"}, []Video{
		{ServerID: a, Title: "e1", Venue: VenueStream},
		{ServerID: a, Title: "e2", Venue: VenueStream},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ser.ID == 0 || ser.YouTubePlaylistID != "PL" || len(vs) != 2 {
		t.Fatalf("ser=%+v vs=%+v", ser, vs)
	}
	for i, v := range vs {
		if v.SeriesID == nil || *v.SeriesID != ser.ID || v.Position != int64(2+i) {
			t.Errorf("vs[%d] = %+v", i, v)
		}
	}
	if got, _ := s.ListVideos(ctx, VideoFilter{ServerID: a, SeriesID: ser.ID}); len(got) != 2 {
		t.Errorf("series videos = %d", len(got))
	}

	// Rollback on a CHECK violation.
	if _, _, err := s.CreateSeriesWithVideos(ctx, Series{ServerID: a, Title: "Doomed"}, []Video{
		{ServerID: a, Title: "ok", Venue: VenueStream},
		{ServerID: a, Title: "bad", Venue: "nope"},
	}); err == nil {
		t.Error("invalid venue: want error")
	}
	// Server mismatch.
	if _, _, err := s.CreateSeriesWithVideos(ctx, Series{ServerID: a, Title: "Mismatch"}, []Video{
		{ServerID: b, Title: "x", Venue: VenueStream},
	}); err == nil {
		t.Error("server mismatch: want error")
	}
	list, _ := s.ListSeries(ctx, a)
	if len(list) != 1 || list[0].ID != ser.ID {
		t.Errorf("series after failures = %+v", list)
	}
	if all, _ := s.ListVideos(ctx, VideoFilter{ServerID: a}); len(all) != 3 {
		t.Errorf("videos after failures = %d, want 3", len(all))
	}
	if all, _ := s.ListVideos(ctx, VideoFilter{ServerID: b}); len(all) != 0 {
		t.Errorf("other server videos = %d", len(all))
	}
}

func TestMigration004WithExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v3.db")
	old := openLegacy(t, path)
	if err := old.migrateUpTo(ctx, 3); err != nil {
		t.Fatal(err)
	}
	srv, err := oldServer(old)
	if err != nil {
		t.Fatal(err)
	}
	now := fmtTime(time.Now())
	if _, err := old.db.ExecContext(ctx,
		`INSERT INTO series (server_id, title, created_at, updated_at) VALUES (?, 'ser', ?, ?)`, srv.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(ctx,
		`INSERT INTO videos (server_id, title, venue, status, position, created_at, updated_at)
VALUES (?, 'vid', 'stream', 'queued', 1, ?, ?)`, srv.ID, now, now); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	vs, err := s.ListVideos(ctx, VideoFilter{ServerID: srv.ID})
	if err != nil || len(vs) != 1 || vs[0].YouTubeVideoID != "" || vs[0].ChannelTitle != "" || vs[0].DurationSeconds != 0 {
		t.Errorf("videos = %+v, %v", vs, err)
	}
	ss, err := s.ListSeries(ctx, srv.ID)
	if err != nil || len(ss) != 1 || ss[0].YouTubePlaylistID != "" || ss[0].ThumbnailVideoID != "" {
		t.Errorf("series = %+v, %v", ss, err)
	}
}

func TestEditURLRoundTrip(t *testing.T) {
	s, sid := newStoreSrv(t)
	v, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: "v", Venue: VenueStream, EditURL: "https://drive.example/a"})
	if err != nil {
		t.Fatal(err)
	}
	if v.EditURL != "https://drive.example/a" {
		t.Errorf("created edit_url = %q", v.EditURL)
	}
	if _, err := s.UpdateVideo(ctx, sid, v.ID, VideoPatch{EditURL: ptr("https://drive.example/b")}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetVideo(ctx, sid, v.ID); got.EditURL != "https://drive.example/b" {
		t.Errorf("patched edit_url = %q", got.EditURL)
	}
	// Other fields' patches leave it alone.
	if _, err := s.UpdateVideo(ctx, sid, v.ID, VideoPatch{Notes: ptr("n")}); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.ListVideos(ctx, VideoFilter{ServerID: sid}); len(list) != 1 || list[0].EditURL != "https://drive.example/b" {
		t.Errorf("listed = %+v", list)
	}
}

func TestSuggestionVideoRoundTrip(t *testing.T) {
	s, sid := newStoreSrv(t)
	if err := s.UpsertSuggestion(ctx, sug(sid, "m1", 0, time.Now())); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	id := list[0].ID
	if list[0].VideoLookupAt != nil || list[0].YouTubeVideoID != "" || list[0].DurationSeconds != 0 {
		t.Errorf("initial = %+v", list[0])
	}
	at := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	v := SuggestionVideo{YouTubeVideoID: "abcdefghijk", Title: "T", ChannelTitle: "C", DurationSeconds: 125}
	if err := s.SetSuggestionVideo(ctx, sid, id, v, at); err != nil {
		t.Fatal(err)
	}
	check := func(label string, got Suggestion) {
		t.Helper()
		if got.YouTubeVideoID != "abcdefghijk" || got.VideoTitle != "T" || got.ChannelTitle != "C" ||
			got.DurationSeconds != 125 || got.VideoLookupAt == nil || !got.VideoLookupAt.Equal(at) {
			t.Errorf("%s = %+v", label, got)
		}
	}
	got, err := s.GetSuggestion(ctx, sid, id)
	if err != nil {
		t.Fatal(err)
	}
	check("get", got)
	list, _ = s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	check("list", list[0])
	if got, err = s.SetSuggestionStatus(ctx, sid, id, SuggestionDiscarded, "mod", at); err != nil {
		t.Fatal(err)
	}
	check("after status", got)
}

func TestUpsertSuggestionVideoReset(t *testing.T) {
	s, sid := newStoreSrv(t)
	posted := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	in := sug(sid, "m1", 1, posted)
	if err := s.UpsertSuggestion(ctx, in); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	id := list[0].ID
	at := posted.Add(time.Hour)
	v := SuggestionVideo{YouTubeVideoID: "abcdefghijk", Title: "T", ChannelTitle: "C", DurationSeconds: 60}
	if err := s.SetSuggestionVideo(ctx, sid, id, v, at); err != nil {
		t.Fatal(err)
	}

	// Same content (only votes change): lookup is kept.
	in.ReactionCount = 7
	if err := s.UpsertSuggestion(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSuggestion(ctx, sid, id)
	if got.ReactionCount != 7 || got.YouTubeVideoID != "abcdefghijk" || got.VideoTitle != "T" ||
		got.ChannelTitle != "C" || got.DurationSeconds != 60 || got.VideoLookupAt == nil {
		t.Errorf("same content reset lookup: %+v", got)
	}

	// Changed content: lookup is reset.
	in.Content = "edited"
	if err := s.UpsertSuggestion(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSuggestion(ctx, sid, id)
	if got.Content != "edited" || got.YouTubeVideoID != "" || got.VideoTitle != "" ||
		got.ChannelTitle != "" || got.DurationSeconds != 0 || got.VideoLookupAt != nil {
		t.Errorf("changed content kept lookup: %+v", got)
	}
}

func TestPendingVideoLookups(t *testing.T) {
	s, sid := newStoreSrv(t)
	other := mustServer(t, s, "g2").ID
	t0 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i, m := range []string{"m3", "m1", "m2", "done"} {
		// posted order: m1 < m2 < m3 < done
		posted := t0.Add(time.Duration([]int{3, 1, 2, 4}[i]) * time.Hour)
		if err := s.UpsertSuggestion(ctx, sug(sid, m, 0, posted)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.UpsertSuggestion(ctx, sug(other, "foreign", 0, t0)); err != nil {
		t.Fatal(err)
	}
	all, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	for _, x := range all {
		if x.DiscordMessageID == "done" {
			if err := s.SetSuggestionVideo(ctx, sid, x.ID, SuggestionVideo{}, t0); err != nil {
				t.Fatal(err)
			}
		}
	}
	ids := func(list []Suggestion) []string {
		out := []string{}
		for _, x := range list {
			out = append(out, x.DiscordMessageID)
		}
		return out
	}
	got, err := s.PendingVideoLookups(ctx, sid, 10)
	if err != nil || !slices.Equal(ids(got), []string{"m1", "m2", "m3"}) {
		t.Errorf("pending = %v, %v", ids(got), err)
	}
	got, err = s.PendingVideoLookups(ctx, sid, 2)
	if err != nil || !slices.Equal(ids(got), []string{"m1", "m2"}) {
		t.Errorf("limited = %v, %v", ids(got), err)
	}
	got, err = s.PendingVideoLookups(ctx, other, 10)
	if err != nil || !slices.Equal(ids(got), []string{"foreign"}) {
		t.Errorf("other server = %v, %v", ids(got), err)
	}
	got, err = s.PendingVideoLookups(ctx, sid+1000, 10)
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("unknown server = %#v, %v", got, err)
	}
}

func TestSetSuggestionVideoOtherServer(t *testing.T) {
	s, sid := newStoreSrv(t)
	other := mustServer(t, s, "g2").ID
	if err := s.UpsertSuggestion(ctx, sug(sid, "m1", 0, time.Now())); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	err := s.SetSuggestionVideo(ctx, other, list[0].ID, SuggestionVideo{YouTubeVideoID: "abcdefghijk", Title: "x"}, time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	got, _ := s.GetSuggestion(ctx, sid, list[0].ID)
	if got.VideoLookupAt != nil || got.VideoTitle != "" {
		t.Errorf("foreign write leaked: %+v", got)
	}
	if err := s.SetSuggestionVideo(ctx, sid, list[0].ID+999, SuggestionVideo{}, time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing id err = %v", err)
	}
}

func TestMigration006WithExistingRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5.db")
	old := openLegacy(t, path)
	if err := old.migrateUpTo(ctx, 5); err != nil {
		t.Fatal(err)
	}
	srv, err := oldServer(old)
	if err != nil {
		t.Fatal(err)
	}
	now := fmtTime(time.Now())
	if _, err := old.db.ExecContext(ctx,
		`INSERT INTO suggestions (server_id, discord_message_id, channel_id, author_id, author_name, content, reaction_count,
    posted_at, created_at, updated_at) VALUES (?, 'm1', 'ch', 'au', 'name', 'hi', 0, ?, ?, ?)`, srv.ID, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	got, err := s.PendingVideoLookups(ctx, srv.ID, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("pending = %+v, %v", got, err)
	}
	x := got[0]
	if x.DiscordMessageID != "m1" || x.YouTubeVideoID != "" || x.VideoTitle != "" || x.ChannelTitle != "" ||
		x.DurationSeconds != 0 || x.VideoLookupAt != nil {
		t.Errorf("migrated row = %+v", x)
	}
}

func firstSuggID(t *testing.T, s *Store, sid int64) int64 {
	t.Helper()
	list, err := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	if err != nil || len(list) == 0 {
		t.Fatalf("list = %v, %v", list, err)
	}
	return list[0].ID
}

func TestBlockSelfSuggestionsRoundTrip(t *testing.T) {
	s := newStore(t)
	srv, err := s.CreateServer(ctx, Server{GuildID: "g1", Name: "A", BlockSelfSuggestions: true})
	if err != nil || !srv.BlockSelfSuggestions {
		t.Fatalf("create = %+v, %v", srv, err)
	}
	got, err := s.UpdateServer(ctx, srv.ID, ServerPatch{BlockSelfSuggestions: ptr(false)})
	if err != nil || got.BlockSelfSuggestions {
		t.Fatalf("patch false = %+v, %v", got, err)
	}
	got, err = s.UpdateServer(ctx, srv.ID, ServerPatch{BlockSelfSuggestions: ptr(true)})
	if err != nil || !got.BlockSelfSuggestions {
		t.Fatalf("patch true = %+v, %v", got, err)
	}
	if g, _ := s.GetServer(ctx, srv.ID); !g.BlockSelfSuggestions {
		t.Errorf("get = %+v", g)
	}
	list, _ := s.ListServers(ctx)
	if len(list) != 1 || !list[0].BlockSelfSuggestions {
		t.Errorf("list = %+v", list)
	}
}

func TestAuthorUsernameAndSelfReset(t *testing.T) {
	s, sid := newStoreSrv(t)
	posted := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	in := sug(sid, "m1", 1, posted)
	in.AuthorUsername = "user1"
	if err := s.UpsertSuggestion(ctx, in); err != nil {
		t.Fatal(err)
	}
	id := firstSuggID(t, s, sid)
	if got, _ := s.GetSuggestion(ctx, sid, id); got.AuthorUsername != "user1" {
		t.Errorf("username = %q", got.AuthorUsername)
	}
	v := SuggestionVideo{YouTubeVideoID: "abcdefghijk", ChannelHandle: "@chan", SelfSuggestion: true}
	if err := s.SetSuggestionVideo(ctx, sid, id, v, posted); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetSuggestion(ctx, sid, id)
	if got.ChannelHandle != "@chan" || !got.SelfSuggestion {
		t.Errorf("video not stored: %+v", got)
	}

	// Username changes without content change: handle/flag kept.
	in.AuthorUsername = "user2"
	if err := s.UpsertSuggestion(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSuggestion(ctx, sid, id)
	if got.AuthorUsername != "user2" || got.ChannelHandle != "@chan" || !got.SelfSuggestion {
		t.Errorf("same content: %+v", got)
	}

	// Discard automatically, then edit content: handle/flag reset, status and reason kept.
	if _, err := s.DiscardSelfSuggestions(ctx, sid, posted); err != nil {
		t.Fatal(err)
	}
	in.Content = "edited"
	if err := s.UpsertSuggestion(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetSuggestion(ctx, sid, id)
	if got.ChannelHandle != "" || got.SelfSuggestion || got.VideoLookupAt != nil ||
		got.Status != SuggestionDiscarded || got.DiscardReason != DiscardSelfSuggestion {
		t.Errorf("changed content: %+v", got)
	}
}

func TestDiscardSelfSuggestions(t *testing.T) {
	s, sid := newStoreSrv(t)
	other := mustServer(t, s, "g2").ID
	posted := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 1, 1, 2, 3, 0, time.UTC)
	flag := SuggestionVideo{SelfSuggestion: true}
	idOf := func(server int64, msg string) int64 {
		t.Helper()
		if err := s.UpsertSuggestion(ctx, sug(server, msg, 0, posted)); err != nil {
			t.Fatal(err)
		}
		list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: server})
		for _, x := range list {
			if x.DiscordMessageID == msg {
				return x.ID
			}
		}
		t.Fatal("missing", msg)
		return 0
	}
	a := idOf(sid, "a")   // flagged pending: discarded
	b := idOf(sid, "b")   // not flagged
	c := idOf(sid, "c")   // flagged but approved
	d := idOf(sid, "d")   // flagged, restored once (overridden)
	e := idOf(other, "e") // other server
	for _, p := range []struct{ server, id int64 }{{sid, a}, {sid, c}, {sid, d}, {other, e}} {
		if err := s.SetSuggestionVideo(ctx, p.server, p.id, flag, posted); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetSuggestionVideo(ctx, sid, b, SuggestionVideo{}, posted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSuggestionStatus(ctx, sid, c, SuggestionApproved, "mod", posted); err != nil {
		t.Fatal(err)
	}
	// d: discard automatically, restore (sets override). First flag it as discarded by auto.
	if n, err := s.DiscardSelfSuggestions(ctx, sid, posted); err != nil || n != 2 {
		t.Fatalf("first discard n=%d err=%v", n, err)
	}
	if _, err := s.SetSuggestionStatus(ctx, sid, d, SuggestionPending, "mod", posted); err != nil {
		t.Fatal(err)
	}
	// a was auto discarded; reset it to pending directly so only a and not-d qualify.
	if _, err := s.db.ExecContext(ctx, `UPDATE suggestions SET status='pending', discard_reason='', reviewed_by='', reviewed_at=NULL WHERE id = ?`, a); err != nil {
		t.Fatal(err)
	}

	n, err := s.DiscardSelfSuggestions(ctx, sid, now)
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, _ := s.GetSuggestion(ctx, sid, a)
	if got.Status != SuggestionDiscarded || got.DiscardReason != DiscardSelfSuggestion || got.ReviewedBy != "" ||
		got.ReviewedAt == nil || !got.ReviewedAt.Equal(now) {
		t.Errorf("a = %+v", got)
	}
	for id, want := range map[int64]SuggestionStatus{b: SuggestionPending, c: SuggestionApproved, d: SuggestionPending} {
		if x, _ := s.GetSuggestion(ctx, sid, id); x.Status != want {
			t.Errorf("id %d status = %s, want %s", id, x.Status, want)
		}
	}
	if x, _ := s.GetSuggestion(ctx, other, e); x.Status != SuggestionPending {
		t.Errorf("other server touched: %+v", x)
	}
	if n, err := s.DiscardSelfSuggestions(ctx, sid, now); err != nil || n != 0 {
		t.Errorf("repeat n=%d err=%v", n, err)
	}
}

func TestRestoreSelfDiscardSetsOverride(t *testing.T) {
	s, sid := newStoreSrv(t)
	posted := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	if err := s.UpsertSuggestion(ctx, sug(sid, "m1", 0, posted)); err != nil {
		t.Fatal(err)
	}
	id := firstSuggID(t, s, sid)
	if err := s.SetSuggestionVideo(ctx, sid, id, SuggestionVideo{SelfSuggestion: true}, posted); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.DiscardSelfSuggestions(ctx, sid, posted); n != 1 {
		t.Fatalf("n = %d", n)
	}
	got, err := s.SetSuggestionStatus(ctx, sid, id, SuggestionPending, "mod", posted)
	if err != nil || got.Status != SuggestionPending || !got.SelfOverride || got.DiscardReason != "" {
		t.Fatalf("restored = %+v, %v", got, err)
	}
	if n, _ := s.DiscardSelfSuggestions(ctx, sid, posted); n != 0 {
		t.Errorf("second discard n = %d", n)
	}
	if x, _ := s.GetSuggestion(ctx, sid, id); x.Status != SuggestionPending {
		t.Errorf("status = %s", x.Status)
	}
}

func TestManualDiscardHasNoReason(t *testing.T) {
	s, sid := newStoreSrv(t)
	posted := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	if err := s.UpsertSuggestion(ctx, sug(sid, "m1", 0, posted)); err != nil {
		t.Fatal(err)
	}
	id := firstSuggID(t, s, sid)
	got, err := s.SetSuggestionStatus(ctx, sid, id, SuggestionDiscarded, "mod", posted)
	if err != nil || got.DiscardReason != "" || got.ReviewedBy != "mod" {
		t.Fatalf("manual = %+v, %v", got, err)
	}
	// Restoring a manual discard does not set override.
	got, _ = s.SetSuggestionStatus(ctx, sid, id, SuggestionPending, "mod", posted)
	if got.SelfOverride {
		t.Errorf("override set: %+v", got)
	}
	// Manually discarding an auto-discarded one clears the reason.
	if err := s.SetSuggestionVideo(ctx, sid, id, SuggestionVideo{SelfSuggestion: true}, posted); err != nil {
		t.Fatal(err)
	}
	s.DiscardSelfSuggestions(ctx, sid, posted)
	got, _ = s.SetSuggestionStatus(ctx, sid, id, SuggestionDiscarded, "mod", posted)
	if got.DiscardReason != "" {
		t.Errorf("reason = %q", got.DiscardReason)
	}
}

func TestApproveSetsSuggestedBy(t *testing.T) {
	s, sid := newStoreSrv(t)
	posted := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	in := sug(sid, "m1", 0, posted)
	in.AuthorID, in.AuthorName = "u42", "Viewer"
	if err := s.UpsertSuggestion(ctx, in); err != nil {
		t.Fatal(err)
	}
	id := firstSuggID(t, s, sid)
	if err := s.SetSuggestionVideo(ctx, sid, id, SuggestionVideo{SelfSuggestion: true}, posted); err != nil {
		t.Fatal(err)
	}
	s.DiscardSelfSuggestions(ctx, sid, posted)
	sg, v, err := s.ApproveSuggestion(ctx, sid, id, Video{Title: "T", Venue: VenueStream}, "mod", posted)
	if err != nil {
		t.Fatal(err)
	}
	if v.SuggestedByID != "u42" || v.SuggestedByName != "Viewer" || sg.DiscardReason != "" {
		t.Errorf("video = %+v, sug = %+v", v, sg)
	}
	got, _ := s.GetVideo(ctx, sid, v.ID)
	if got.SuggestedByID != "u42" || got.SuggestedByName != "Viewer" {
		t.Errorf("get = %+v", got)
	}
	list, _ := s.ListVideos(ctx, VideoFilter{ServerID: sid})
	if len(list) != 1 || list[0].SuggestedByID != "u42" {
		t.Errorf("list = %+v", list)
	}
	// Not patchable: an update leaves it unchanged.
	up, err := s.UpdateVideo(ctx, sid, v.ID, VideoPatch{Title: ptr("T2")})
	if err != nil || up.SuggestedByID != "u42" {
		t.Errorf("update = %+v, %v", up, err)
	}
}

func TestListSuggestionsAuthorFilter(t *testing.T) {
	s, sid := newStoreSrv(t)
	other := mustServer(t, s, "g2").ID
	posted := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		server        int64
		msg, authorID string
	}{{sid, "a", "u1"}, {sid, "b", "u2"}, {sid, "c", "u1"}, {other, "d", "u1"}} {
		in := sug(c.server, c.msg, 0, posted)
		in.AuthorID = c.authorID
		if err := s.UpsertSuggestion(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	got := suggIDs(t, s, SuggestionFilter{ServerID: sid, AuthorID: "u1", Sort: SortOldest})
	if len(got) != 2 {
		t.Errorf("u1 = %v", got)
	}
	if got := suggIDs(t, s, SuggestionFilter{ServerID: sid, AuthorID: "nobody"}); len(got) != 0 {
		t.Errorf("nobody = %v", got)
	}
	if got := suggIDs(t, s, SuggestionFilter{ServerID: other, AuthorID: "u2"}); len(got) != 0 {
		t.Errorf("other u2 = %v", got)
	}
	if got := suggIDs(t, s, SuggestionFilter{ServerID: sid}); len(got) != 3 {
		t.Errorf("no filter = %v", got)
	}
}

func TestMigration007ResetsLookups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v6.db")
	old := openLegacy(t, path)
	if err := old.migrateUpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	srv, err := oldServer(old)
	if err != nil {
		t.Fatal(err)
	}
	now := fmtTime(time.Now())
	if _, err := old.db.ExecContext(ctx,
		`INSERT INTO suggestions (server_id, discord_message_id, channel_id, author_id, author_name, content, reaction_count,
    posted_at, video_lookup_at, created_at, updated_at) VALUES (?, 'm1', 'ch', 'au', 'name', 'hi', 0, ?, ?, ?, ?)`,
		srv.ID, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	got, err := s.PendingVideoLookups(ctx, srv.ID, 10)
	if err != nil || len(got) != 1 {
		t.Fatalf("pending = %+v, %v", got, err)
	}
	x := got[0]
	if x.VideoLookupAt != nil || x.AuthorUsername != "" || x.ChannelHandle != "" || x.SelfSuggestion || x.DiscardReason != "" || x.SelfOverride {
		t.Errorf("migrated row = %+v", x)
	}
}

// oldServer inserts a server with raw SQL using only pre-007 columns, for
// databases migrated to an older schema.
func oldServer(s *Store) (Server, error) {
	now := fmtTime(time.Now())
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO servers (guild_id, name, created_at, updated_at) VALUES ('g1', 'S', ?, ?)`, now, now)
	if err != nil {
		return Server{}, err
	}
	id, err := res.LastInsertId()
	return Server{ID: id}, err
}

func TestVideoEditorRoundTrip(t *testing.T) {
	s, sid := newStoreSrv(t)
	v, err := s.CreateVideo(ctx, Video{ServerID: sid, Title: "x", Venue: VenueStream, EditorID: "123", EditorName: "Ed"})
	if err != nil {
		t.Fatal(err)
	}
	if v.EditorID != "123" || v.EditorName != "Ed" {
		t.Fatalf("created = %q/%q", v.EditorID, v.EditorName)
	}
	// Absent fields are untouched.
	if v, err = s.UpdateVideo(ctx, sid, v.ID, VideoPatch{Notes: ptr("n")}); err != nil || v.EditorID != "123" || v.EditorName != "Ed" {
		t.Fatalf("untouched = %+v, %v", v, err)
	}
	if v, err = s.UpdateVideo(ctx, sid, v.ID, VideoPatch{EditorID: ptr(""), EditorName: ptr("Free Text")}); err != nil ||
		v.EditorID != "" || v.EditorName != "Free Text" {
		t.Fatalf("free text = %+v, %v", v, err)
	}
	l, err := s.ListVideos(ctx, VideoFilter{ServerID: sid})
	if err != nil || len(l) != 1 || l[0].EditorName != "Free Text" {
		t.Fatalf("list = %+v, %v", l, err)
	}
	if v, err = s.UpdateVideo(ctx, sid, v.ID, VideoPatch{EditorID: ptr(""), EditorName: ptr("")}); err != nil ||
		v.EditorID != "" || v.EditorName != "" {
		t.Fatalf("cleared = %+v, %v", v, err)
	}
}

func TestListUsersAndGetUser(t *testing.T) {
	s, _ := newStoreSrv(t)
	l, err := s.ListUsers(ctx)
	if err != nil || l == nil || len(l) != 0 {
		t.Fatalf("empty ListUsers = %#v, %v", l, err)
	}
	for _, u := range []User{
		{DiscordID: "3", Username: "zed"},
		{DiscordID: "2", Username: "zzz", GlobalName: "bob"},
		{DiscordID: "1", Username: "Carl"},
		{DiscordID: "4", Username: "x", GlobalName: "Alice"},
		{DiscordID: "0", Username: "ALICE"},
	} {
		if err := s.UpsertUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	l, err = s.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, u := range l {
		ids = append(ids, u.DiscordID)
	}
	if !slices.Equal(ids, []string{"0", "4", "2", "1", "3"}) {
		t.Errorf("order = %v", ids)
	}
	u, err := s.GetUser(ctx, "2")
	if err != nil || u.GlobalName != "bob" || u.CreatedAt.IsZero() {
		t.Errorf("GetUser = %+v, %v", u, err)
	}
	if _, err := s.GetUser(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetUser missing err = %v", err)
	}
}

func TestMigration008OnExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old := openLegacy(t, path)
	if err := old.migrateUpTo(ctx, 7); err != nil {
		t.Fatal(err)
	}
	now := fmtTime(time.Now())
	if _, err := old.db.ExecContext(ctx, `INSERT INTO servers (id, guild_id, name, created_at, updated_at) VALUES (1, 'g', 'n', '`+now+`', '`+now+`')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(ctx, `INSERT INTO videos (server_id, title, venue, position, created_at, updated_at) VALUES (1, 'old', 'stream', 1, '`+now+`', '`+now+`')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	l, err := s.ListVideos(ctx, VideoFilter{ServerID: 1})
	if err != nil || len(l) != 1 || l[0].EditorID != "" || l[0].EditorName != "" {
		t.Fatalf("videos = %+v, %v", l, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM schema_migrations`); n != 11 {
		t.Errorf("migrations = %d, want 11", n)
	}
}

// removeFixture creates one suggestion per status plus one in another server and returns their ids.
func removeFixture(t *testing.T) (s *Store, sid, other int64, ids map[string]int64) {
	t.Helper()
	s, sid = newStoreSrv(t)
	other = mustServer(t, s, "g2").ID
	posted := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	ids = map[string]int64{}
	for _, p := range []struct {
		server int64
		msg    string
	}{{sid, "pending"}, {sid, "approved"}, {sid, "discarded"}, {sid, "keep"}, {other, "foreign"}} {
		if err := s.UpsertSuggestion(ctx, sug(p.server, p.msg, 1, posted)); err != nil {
			t.Fatal(err)
		}
		list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: p.server})
		for _, x := range list {
			if x.DiscordMessageID == p.msg {
				ids[p.msg] = x.ID
			}
		}
	}
	if _, _, err := s.ApproveSuggestion(ctx, sid, ids["approved"], Video{Title: "t", Venue: VenueStream}, "mod", posted); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetSuggestionStatus(ctx, sid, ids["discarded"], SuggestionDiscarded, "mod", posted); err != nil {
		t.Fatal(err)
	}
	return s, sid, other, ids
}

func TestRemoveSuggestionsHidesRows(t *testing.T) {
	s, sid, other, ids := removeFixture(t)
	now := time.Date(2026, 4, 1, 1, 2, 3, 0, time.UTC)
	n, err := s.RemoveSuggestions(ctx, sid, []int64{ids["pending"], ids["approved"], ids["discarded"], ids["foreign"]}, now)
	if err != nil || n != 3 {
		t.Fatalf("RemoveSuggestions = %d, %v; want 3", n, err)
	}
	// Already removed rows are not counted twice.
	if n, err := s.RemoveSuggestions(ctx, sid, []int64{ids["pending"], ids["keep"]}, now); err != nil || n != 1 {
		t.Fatalf("second RemoveSuggestions = %d, %v; want 1", n, err)
	}
	if n, err := s.RemoveSuggestions(ctx, sid, nil, now); err != nil || n != 0 {
		t.Fatalf("empty RemoveSuggestions = %d, %v", n, err)
	}
	for _, st := range []SuggestionStatus{"", SuggestionPending, SuggestionApproved, SuggestionDiscarded} {
		if got := suggIDs(t, s, SuggestionFilter{ServerID: sid, Status: st}); len(got) != 0 {
			t.Errorf("status %q lists %v after removal", st, got)
		}
	}
	// The other server's row is untouched.
	if got := suggIDs(t, s, SuggestionFilter{ServerID: other}); !slices.Equal(got, []string{"foreign"}) {
		t.Errorf("other server = %v", got)
	}
	// GetSuggestion still returns removed rows, with RemovedAt set.
	g, err := s.GetSuggestion(ctx, sid, ids["approved"])
	if err != nil || g.RemovedAt == nil || !g.RemovedAt.Equal(now) {
		t.Errorf("GetSuggestion = %+v, %v", g, err)
	}
	// Unremoved rows show RemovedAt nil.
	if g, err := s.GetSuggestion(ctx, other, ids["foreign"]); err != nil || g.RemovedAt != nil {
		t.Errorf("foreign = %+v, %v", g, err)
	}
}

func TestRemovedSuggestionsSkippedByLookupsAndSelfDiscard(t *testing.T) {
	s, sid, _, ids := removeFixture(t)
	posted := time.Now()
	if err := s.SetSuggestionVideo(ctx, sid, ids["keep"], SuggestionVideo{SelfSuggestion: true}, posted); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSuggestionVideo(ctx, sid, ids["pending"], SuggestionVideo{SelfSuggestion: true}, posted); err != nil {
		t.Fatal(err)
	}
	if n, err := s.RemoveSuggestions(ctx, sid, []int64{ids["pending"], ids["discarded"]}, posted); err != nil || n != 2 {
		t.Fatalf("remove = %d, %v", n, err)
	}
	lookups, err := s.PendingVideoLookups(ctx, sid, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range lookups {
		if x.ID == ids["pending"] || x.ID == ids["discarded"] {
			t.Errorf("removed suggestion %d in PendingVideoLookups", x.ID)
		}
	}
	if len(lookups) != 1 || lookups[0].ID != ids["approved"] {
		t.Errorf("lookups = %+v", lookups)
	}
	if n, err := s.DiscardSelfSuggestions(ctx, sid, posted); err != nil || n != 1 {
		t.Fatalf("DiscardSelfSuggestions = %d, %v; want 1 (removed row skipped)", n, err)
	}
	g, _ := s.GetSuggestion(ctx, sid, ids["pending"])
	if g.Status != SuggestionPending {
		t.Errorf("removed self-suggestion status = %q", g.Status)
	}
}

func TestUnremoveSuggestion(t *testing.T) {
	s, sid, other, ids := removeFixture(t)
	if _, err := s.RemoveSuggestions(ctx, sid, []int64{ids["keep"]}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.UnremoveSuggestion(ctx, other, ids["keep"]); !errors.Is(err, ErrNotFound) {
		t.Errorf("other server err = %v", err)
	}
	if g, _ := s.GetSuggestion(ctx, sid, ids["keep"]); g.RemovedAt == nil {
		t.Error("restored by another server")
	}
	if err := s.UnremoveSuggestion(ctx, sid, ids["keep"]); err != nil {
		t.Fatal(err)
	}
	if got := suggIDs(t, s, SuggestionFilter{ServerID: sid, Status: SuggestionPending}); !slices.Contains(got, "keep") {
		t.Errorf("pending = %v", got)
	}
	if err := s.UnremoveSuggestion(ctx, sid, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing err = %v", err)
	}
}

func TestUpsertSuggestionKeepsRemoved(t *testing.T) {
	s, sid, _, ids := removeFixture(t)
	if _, err := s.RemoveSuggestions(ctx, sid, []int64{ids["keep"]}, time.Now()); err != nil {
		t.Fatal(err)
	}
	in := sug(sid, "keep", 42, time.Now())
	in.Content = "new content"
	if err := s.UpsertSuggestion(ctx, in); err != nil {
		t.Fatal(err)
	}
	g, err := s.GetSuggestion(ctx, sid, ids["keep"])
	if err != nil || g.RemovedAt == nil || g.Content != "new content" || g.ReactionCount != 42 {
		t.Errorf("after upsert = %+v, %v", g, err)
	}
	if got := suggIDs(t, s, SuggestionFilter{ServerID: sid}); slices.Contains(got, "keep") {
		t.Errorf("removed row listed: %v", got)
	}
	if lookups, _ := s.PendingVideoLookups(ctx, sid, 100); slices.ContainsFunc(lookups, func(x Suggestion) bool { return x.ID == ids["keep"] }) {
		t.Error("removed row re-queued for lookup after content change")
	}
}

func TestReviewRemovedSuggestionNotFound(t *testing.T) {
	s, sid, _, ids := removeFixture(t)
	if _, err := s.RemoveSuggestions(ctx, sid, []int64{ids["keep"], ids["discarded"]}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, st := range []SuggestionStatus{SuggestionApproved, SuggestionDiscarded, SuggestionPending} {
		if _, err := s.SetSuggestionStatus(ctx, sid, ids["keep"], st, "mod", time.Now()); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetSuggestionStatus(%s) err = %v", st, err)
		}
	}
	if _, err := s.SetSuggestionStatus(ctx, sid, ids["discarded"], SuggestionPending, "mod", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Errorf("restore err = %v", err)
	}
	_, _, err := s.ApproveSuggestion(ctx, sid, ids["keep"], Video{Title: "t", Venue: VenueStream}, "mod", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("ApproveSuggestion err = %v", err)
	}
	if vs, _ := s.ListVideos(ctx, VideoFilter{ServerID: sid}); len(vs) != 1 {
		t.Errorf("videos = %d, want 1 (only the earlier approval)", len(vs))
	}
	if g, _ := s.GetSuggestion(ctx, sid, ids["keep"]); g.Status != SuggestionPending {
		t.Errorf("status changed to %q", g.Status)
	}
}

func TestMigration009OnExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old := openLegacy(t, path)
	if err := old.migrateUpTo(ctx, 8); err != nil {
		t.Fatal(err)
	}
	now := fmtTime(time.Now())
	if _, err := old.db.ExecContext(ctx, `INSERT INTO servers (id, guild_id, name, created_at, updated_at) VALUES (1, 'g', 'n', '`+now+`', '`+now+`')`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(ctx, `INSERT INTO suggestions (server_id, discord_message_id, channel_id, author_id, author_name, content, posted_at, created_at, updated_at) VALUES (1, 'm', 'c', 'a', 'n', 'x', '`+now+`', '`+now+`', '`+now+`')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	l, err := s.ListSuggestions(ctx, SuggestionFilter{ServerID: 1})
	if err != nil || len(l) != 1 || l[0].RemovedAt != nil {
		t.Fatalf("suggestions = %+v, %v", l, err)
	}
	if n, err := s.RemoveSuggestions(ctx, 1, []int64{l[0].ID}, time.Now()); err != nil || n != 1 {
		t.Fatalf("remove = %d, %v", n, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM schema_migrations`); n != 11 {
		t.Errorf("migrations = %d, want 11", n)
	}
}

func TestSuggestersByVideo(t *testing.T) {
	s, sid := newStoreSrv(t)
	other := mustServer(t, s, "g2").ID
	t0 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	add := func(server int64, msg, author, name, vid string, at time.Time, status SuggestionStatus) {
		t.Helper()
		x := sug(server, msg, 0, at)
		x.AuthorID, x.AuthorName = author, name
		if err := s.UpsertSuggestion(ctx, x); err != nil {
			t.Fatal(err)
		}
		list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: server})
		for _, l := range list {
			if l.DiscordMessageID == msg {
				if err := s.SetSuggestionVideo(ctx, server, l.ID, SuggestionVideo{YouTubeVideoID: vid}, at); err != nil {
					t.Fatal(err)
				}
				if status != SuggestionPending {
					if _, err := s.SetSuggestionStatus(ctx, server, l.ID, status, "mod", at); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	add(sid, "m1", "bob", "Bob", "vidA", t0.Add(2*time.Hour), SuggestionPending)
	add(sid, "m2", "al", "Al old", "vidA", t0.Add(time.Hour), SuggestionApproved)
	add(sid, "m3", "al", "Al new", "vidA", t0.Add(3*time.Hour), SuggestionDiscarded)
	add(sid, "m4", "cy", "Cy", "vidA", t0, SuggestionPending) // will be removed
	add(sid, "m5", "dee", "Dee", "vidB", t0, SuggestionPending)
	add(other, "m6", "eve", "Eve", "vidA", t0, SuggestionPending)
	list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
	for _, l := range list {
		if l.DiscordMessageID == "m4" {
			if _, err := s.RemoveSuggestions(ctx, sid, []int64{l.ID}, t0); err != nil {
				t.Fatal(err)
			}
		}
	}
	got, err := s.SuggestersByVideo(ctx, sid, []string{"vidA", "vidA", "", "vidB", "vidNone"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got = %+v", got)
	}
	a := got["vidA"]
	if len(a) != 2 || a[0].AuthorID != "al" || a[0].AuthorName != "Al new" || !a[0].FirstAt.Equal(t0.Add(time.Hour)) ||
		a[1].AuthorID != "bob" || a[1].AuthorName != "Bob" || !a[1].FirstAt.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("vidA = %+v", a)
	}
	if b := got["vidB"]; len(b) != 1 || b[0].AuthorID != "dee" {
		t.Errorf("vidB = %+v", b)
	}
	if _, ok := got["vidNone"]; ok {
		t.Error("vidNone present")
	}
	for _, in := range [][]string{nil, {}, {""}} {
		m, err := s.SuggestersByVideo(ctx, sid, in)
		if err != nil || m == nil || len(m) != 0 {
			t.Errorf("SuggestersByVideo(%v) = %v, %v", in, m, err)
		}
	}
	// A closed store errors on any query, so empty input must not query.
	s2 := newStore(t)
	s2.Close()
	if m, err := s2.SuggestersByVideo(ctx, 1, []string{""}); err != nil || len(m) != 0 {
		t.Errorf("queried for no IDs: %v, %v", m, err)
	}
}

func TestSuggestersByVideoChunks(t *testing.T) {
	s, sid := newStoreSrv(t)
	t0 := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	var ids []string
	for i := range 1100 {
		vid := fmt.Sprintf("vid%04d", i)
		ids = append(ids, vid)
		if i%100 != 0 {
			continue
		}
		if err := s.UpsertSuggestion(ctx, sug(sid, vid, 0, t0)); err != nil {
			t.Fatal(err)
		}
		list, _ := s.ListSuggestions(ctx, SuggestionFilter{ServerID: sid})
		for _, l := range list {
			if l.DiscordMessageID == vid {
				if err := s.SetSuggestionVideo(ctx, sid, l.ID, SuggestionVideo{YouTubeVideoID: vid}, t0); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	got, err := s.SuggestersByVideo(ctx, sid, ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 11 {
		t.Errorf("len = %d; want 11", len(got))
	}
}

// --- clips ---

func clip(sid int64, msg string, posted time.Time) Clip {
	return Clip{
		ServerID: sid, DiscordMessageID: msg, ChannelID: "ch", MessageURL: "https://discord.com/channels/g/ch/" + msg,
		AuthorID: "au", AuthorName: "author-" + msg, Content: "content-" + msg, PostedAt: posted,
		ClipURL: "https://clips.twitch.tv/" + msg, TwitchClipID: "slug-" + msg, Title: "title-" + msg,
	}
}

func mustClip(t *testing.T, s *Store, c Clip) Clip {
	t.Helper()
	if err := s.UpsertClip(ctx, c); err != nil {
		t.Fatal(err)
	}
	l, err := s.ListClips(ctx, ClipFilter{ServerID: c.ServerID})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range l {
		if x.DiscordMessageID == c.DiscordMessageID {
			return x
		}
	}
	t.Fatalf("clip %s not found", c.DiscordMessageID)
	return Clip{}
}

func clipMsgs(t *testing.T, s *Store, f ClipFilter) []string {
	t.Helper()
	l, err := s.ListClips(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, x := range l {
		out = append(out, x.DiscordMessageID)
	}
	return out
}

func TestServerClipsChannel(t *testing.T) {
	s := newStore(t)
	a, err := s.CreateServer(ctx, Server{GuildID: "g", Name: "n", ClipsChannelID: "c0", ClipsCursor: "5"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ClipsChannelID != "c0" || a.ClipsCursor != "5" {
		t.Fatalf("created = %+v", a)
	}
	if err := s.SetClipsCursor(ctx, a.ID, "77"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSuggestionsCursor(ctx, a.ID, "sug"); err != nil {
		t.Fatal(err)
	}
	g, _ := s.GetServer(ctx, a.ID)
	if g.ClipsCursor != "77" {
		t.Errorf("cursor = %q", g.ClipsCursor)
	}
	// same value keeps the cursor
	g, err = s.UpdateServer(ctx, a.ID, ServerPatch{ClipsChannelID: ptr("c0")})
	if err != nil || g.ClipsCursor != "77" {
		t.Fatalf("same channel: %+v, %v", g, err)
	}
	// a different value resets only the clips cursor
	g, err = s.UpdateServer(ctx, a.ID, ServerPatch{ClipsChannelID: ptr("c1")})
	if err != nil || g.ClipsChannelID != "c1" || g.ClipsCursor != "" || g.SuggestionsCursor != "sug" {
		t.Fatalf("changed channel: %+v, %v", g, err)
	}
	if err := s.SetClipsCursor(ctx, 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetClipsCursor missing = %v", err)
	}
	byGuild, err := s.GetServerByGuild(ctx, "g")
	if err != nil || byGuild.ClipsChannelID != "c1" {
		t.Errorf("by guild = %+v, %v", byGuild, err)
	}
}

func TestUpsertClipInsertDefaults(t *testing.T) {
	s, sid := newStoreSrv(t)
	posted := time.Date(2026, 5, 1, 1, 2, 3, 456, time.UTC)
	before := time.Now().Add(-time.Second)
	c := mustClip(t, s, clip(sid, "m1", posted))
	if c.Status != ClipNew || c.ID == 0 || c.ServerID != sid || !c.PostedAt.Equal(posted) {
		t.Fatalf("clip = %+v", c)
	}
	if c.StatusAt.Before(before) || c.CreatedAt.Before(before) || c.UpdatedAt.Before(before) {
		t.Errorf("timestamps not now: %+v", c)
	}
	if c.TwitchLookupAt != nil || c.ReviewedAt != nil || c.FirstPostedAt != nil || c.ClipCreatedAt != nil {
		t.Errorf("expected nil times: %+v", c)
	}
	if c.ClipURL != "https://clips.twitch.tv/m1" || c.TwitchClipID != "slug-m1" || c.Title != "title-m1" || c.MessageURL == "" {
		t.Errorf("fields = %+v", c)
	}
	if err := s.UpsertClip(ctx, Clip{DiscordMessageID: "x"}); err == nil {
		t.Error("want error for missing server")
	}
}

func TestUpsertClipConflict(t *testing.T) {
	s, sid := newStoreSrv(t)
	other := mustServer(t, s, "g2").ID
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	c := mustClip(t, s, clip(sid, "m1", t0))
	looked := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	tw := ClipTwitch{Title: "T", ClipperName: "cl", BroadcasterName: "br", ThumbnailURL: "th", DurationSeconds: 30, CreatedAt: &looked}
	if err := s.SetClipTwitch(ctx, sid, c.ID, tw, looked); err != nil {
		t.Fatal(err)
	}
	before, _ := s.GetClip(ctx, sid, c.ID)

	// same content: author may change, everything else is kept
	in := clip(sid, "m1", t0)
	in.AuthorName, in.AuthorUsername = "renamed", "uname"
	in.ClipURL, in.Title = "https://other", "other"
	if err := s.UpsertClip(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetClip(ctx, sid, c.ID)
	if got.AuthorName != "renamed" || got.AuthorUsername != "uname" {
		t.Errorf("author not updated: %+v", got)
	}
	got.AuthorName, got.AuthorUsername, got.UpdatedAt = before.AuthorName, before.AuthorUsername, before.UpdatedAt
	if !reflect.DeepEqual(got, before) {
		t.Errorf("same content changed clip:\n got %+v\nwant %+v", got, before)
	}

	// changed content while new: clip fields replaced, twitch fields reset
	in = clip(sid, "m1", t0)
	in.Content = "edited text"
	in.ClipURL, in.TwitchClipID, in.Title = "https://clips.twitch.tv/new", "newslug", "new title"
	if err := s.UpsertClip(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetClip(ctx, sid, c.ID)
	if got.Content != "edited text" || got.ClipURL != in.ClipURL || got.TwitchClipID != "newslug" || got.Title != "new title" {
		t.Errorf("not replaced: %+v", got)
	}
	if got.ClipperName != "" || got.BroadcasterName != "" || got.ThumbnailURL != "" || got.DurationSeconds != 0 ||
		got.ClipCreatedAt != nil || got.TwitchLookupAt != nil {
		t.Errorf("twitch fields not reset: %+v", got)
	}

	// changed content after selection: clip fields kept
	if err := s.SetClipTwitch(ctx, sid, c.ID, tw, looked); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateClip(ctx, sid, c.ID, ClipPatch{Status: ptr(ClipPending)}, "mod", time.Now()); err != nil {
		t.Fatal(err)
	}
	before, _ = s.GetClip(ctx, sid, c.ID)
	in2 := clip(sid, "m1", t0)
	in2.Content = "edited again"
	in2.ClipURL, in2.TwitchClipID, in2.Title = "https://x", "zzz", "zzz"
	if err := s.UpsertClip(ctx, in2); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetClip(ctx, sid, c.ID)
	if got.Content != "edited again" {
		t.Errorf("content = %q", got.Content)
	}
	got.Content, got.UpdatedAt = before.Content, before.UpdatedAt
	if !reflect.DeepEqual(got, before) {
		t.Errorf("selected clip fields changed:\n got %+v\nwant %+v", got, before)
	}

	// another server's use of the message ID doesn't overwrite
	foreign := clip(other, "m1", t0)
	foreign.Content, foreign.AuthorName = "hijack", "evil"
	if err := s.UpsertClip(ctx, foreign); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetClip(ctx, sid, c.ID)
	if got.Content != "edited again" || got.AuthorName == "evil" {
		t.Errorf("overwritten: %+v", got)
	}
	if l, _ := s.ListClips(ctx, ClipFilter{ServerID: other}); len(l) != 0 {
		t.Errorf("other server has %+v", l)
	}
}

func TestListClips(t *testing.T) {
	s, sid := newStoreSrv(t)
	other := mustServer(t, s, "g2").ID
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	ids := map[string]int64{}
	for i, m := range []string{"a", "b", "c", "d"} {
		ids[m] = mustClip(t, s, clip(sid, m, t0.Add(time.Duration(i)*time.Hour))).ID
	}
	// tie with "a" on posted_at, higher id
	ids["e"] = mustClip(t, s, clip(sid, "e", t0)).ID
	mustClip(t, s, clip(other, "z", t0))

	base := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	upd := func(m string, p ClipPatch, at time.Time) {
		t.Helper()
		if _, err := s.UpdateClip(ctx, sid, ids[m], p, "mod", at); err != nil {
			t.Fatal(err)
		}
	}
	upd("a", ClipPatch{Status: ptr(ClipPending)}, base.Add(3*time.Hour))
	upd("b", ClipPatch{Status: ptr(ClipPending)}, base.Add(time.Hour))
	upd("c", ClipPatch{Status: ptr(ClipPosted), YouTubeURL: ptr("y"), InstagramURL: ptr("i"), XURL: ptr("x"), TikTokURL: ptr("t")}, base)
	upd("d", ClipPatch{Status: ptr(ClipPosted), YouTubeURL: ptr("y")}, base.Add(2*time.Hour))
	upd("e", ClipPatch{Status: ptr(ClipDiscarded)}, base)

	eq := func(name string, f ClipFilter, want ...string) {
		t.Helper()
		f.ServerID = sid
		if got := clipMsgs(t, s, f); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	eq("default", ClipFilter{}, "d", "c", "b", "e", "a")
	eq("newest", ClipFilter{Sort: ClipSortNewest}, "d", "c", "b", "e", "a")
	eq("oldest", ClipFilter{Sort: ClipSortOldest}, "a", "e", "b", "c", "d")
	eq("status_at", ClipFilter{Sort: ClipSortStatusAt}, "c", "e", "b", "d", "a")
	eq("status", ClipFilter{Statuses: []ClipStatus{ClipPending}, Sort: ClipSortStatusAt}, "b", "a")
	eq("statuses", ClipFilter{Statuses: []ClipStatus{ClipPosted, ClipDiscarded}, Sort: ClipSortOldest}, "e", "c", "d")
	eq("missing x", ClipFilter{Statuses: []ClipStatus{ClipPosted}, Missing: []Platform{PlatformX}}, "d")
	eq("missing or", ClipFilter{Statuses: []ClipStatus{ClipPosted}, Missing: []Platform{PlatformYouTube, PlatformTikTok}}, "d")
	eq("missing none", ClipFilter{Statuses: []ClipStatus{ClipPosted}, Missing: []Platform{PlatformYouTube}})
	eq("missing all", ClipFilter{Statuses: []ClipStatus{ClipPosted}, Missing: []Platform{PlatformYouTube, PlatformInstagram, PlatformX, PlatformTikTok}}, "d")

	l, err := s.ListClips(ctx, ClipFilter{ServerID: sid, Statuses: []ClipStatus{ClipNew}})
	if err != nil || l == nil || len(l) != 0 {
		t.Errorf("empty = %#v, %v", l, err)
	}
	if got := clipMsgs(t, s, ClipFilter{ServerID: other}); !slices.Equal(got, []string{"z"}) {
		t.Errorf("other = %v", got)
	}
}

func TestUpdateClip(t *testing.T) {
	s, sid := newStoreSrv(t)
	other := mustServer(t, s, "g2").ID
	c := mustClip(t, s, clip(sid, "m1", time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)))
	t1 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	t2, t3, t4, t5 := t1.Add(time.Hour), t1.Add(2*time.Hour), t1.Add(3*time.Hour), t1.Add(4*time.Hour)

	// links saved without a status change: status_at untouched
	g, err := s.UpdateClip(ctx, sid, c.ID, ClipPatch{EditedTitle: ptr("et"), EditedURL: ptr("eu"), YouTubeURL: ptr("y"),
		InstagramURL: ptr("i"), XURL: ptr("x"), TikTokURL: ptr("t"), Notes: ptr("n")}, "mod", t1)
	if err != nil {
		t.Fatal(err)
	}
	if g.EditedTitle != "et" || g.EditedURL != "eu" || g.YouTubeURL != "y" || g.InstagramURL != "i" ||
		g.XURL != "x" || g.TikTokURL != "t" || g.Notes != "n" {
		t.Errorf("links = %+v", g)
	}
	if !g.StatusAt.Equal(c.StatusAt) || g.Status != ClipNew || g.ReviewedAt != nil || !g.UpdatedAt.After(c.UpdatedAt) && !g.UpdatedAt.Equal(c.UpdatedAt) {
		t.Errorf("after links: %+v", g)
	}
	// same status again: status_at unchanged
	g, _ = s.UpdateClip(ctx, sid, c.ID, ClipPatch{Status: ptr(ClipNew)}, "mod", t1)
	if !g.StatusAt.Equal(c.StatusAt) {
		t.Errorf("status_at changed on same status: %v", g.StatusAt)
	}
	// discarding doesn't review
	g, _ = s.UpdateClip(ctx, sid, c.ID, ClipPatch{Status: ptr(ClipDiscarded)}, "mod1", t1)
	if g.Status != ClipDiscarded || !g.StatusAt.Equal(t1) || g.ReviewedAt != nil || g.ReviewedBy != "" {
		t.Errorf("discarded: %+v", g)
	}
	// leaving the inbox sets reviewed_*
	g, _ = s.UpdateClip(ctx, sid, c.ID, ClipPatch{Status: ptr(ClipPending)}, "mod2", t2)
	if g.ReviewedBy != "mod2" || g.ReviewedAt == nil || !g.ReviewedAt.Equal(t2) || !g.StatusAt.Equal(t2) || g.FirstPostedAt != nil {
		t.Errorf("pending: %+v", g)
	}
	// reviewed_* set once
	g, _ = s.UpdateClip(ctx, sid, c.ID, ClipPatch{Status: ptr(ClipNew)}, "mod3", t3)
	g, _ = s.UpdateClip(ctx, sid, c.ID, ClipPatch{Status: ptr(ClipEdited)}, "mod3", t3)
	if g.ReviewedBy != "mod2" || !g.ReviewedAt.Equal(t2) || !g.StatusAt.Equal(t3) {
		t.Errorf("reviewed overwritten: %+v", g)
	}
	// first_posted_at set once
	g, _ = s.UpdateClip(ctx, sid, c.ID, ClipPatch{Status: ptr(ClipPosted)}, "mod3", t4)
	if g.FirstPostedAt == nil || !g.FirstPostedAt.Equal(t4) || !g.StatusAt.Equal(t4) {
		t.Errorf("posted: %+v", g)
	}
	g, _ = s.UpdateClip(ctx, sid, c.ID, ClipPatch{Status: ptr(ClipEdited)}, "mod3", t5)
	g, _ = s.UpdateClip(ctx, sid, c.ID, ClipPatch{Status: ptr(ClipPosted)}, "mod3", t5.Add(time.Hour))
	if !g.FirstPostedAt.Equal(t4) || !g.StatusAt.Equal(t5.Add(time.Hour)) {
		t.Errorf("re-posted: %+v", g)
	}
	// direct new -> posted sets both
	c2 := mustClip(t, s, clip(sid, "m2", t1))
	g, _ = s.UpdateClip(ctx, sid, c2.ID, ClipPatch{Status: ptr(ClipPosted)}, "mod", t2)
	if g.ReviewedBy != "mod" || g.FirstPostedAt == nil {
		t.Errorf("direct posted: %+v", g)
	}

	if _, err := s.UpdateClip(ctx, other, c.ID, ClipPatch{Notes: ptr("x")}, "m", t1); !errors.Is(err, ErrNotFound) {
		t.Errorf("other server = %v", err)
	}
	if _, err := s.UpdateClip(ctx, sid, 999, ClipPatch{}, "m", t1); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
	if _, err := s.GetClip(ctx, other, c.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetClip other = %v", err)
	}
	if got, _ := s.GetClip(ctx, sid, c.ID); got.Notes != "n" {
		t.Errorf("other server modified clip: %+v", got)
	}
}

func TestPendingClipLookupsAndSetTwitch(t *testing.T) {
	s, sid := newStoreSrv(t)
	other := mustServer(t, s, "g2").ID
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	c1 := mustClip(t, s, clip(sid, "m1", t0.Add(2*time.Hour)))
	c2 := mustClip(t, s, clip(sid, "m2", t0.Add(time.Hour)))
	c3 := clip(sid, "m3", t0)
	c3.TwitchClipID = "" // not a Twitch clip
	mustClip(t, s, c3)
	c4 := mustClip(t, s, clip(sid, "m4", t0.Add(3*time.Hour)))
	mustClip(t, s, clip(other, "z", t0))

	l, err := s.PendingClipLookups(ctx, sid, 10)
	if err != nil || len(l) != 3 || l[0].ID != c2.ID || l[1].ID != c1.ID || l[2].ID != c4.ID {
		t.Fatalf("pending = %+v, %v", l, err)
	}
	if l, _ = s.PendingClipLookups(ctx, sid, 2); len(l) != 2 {
		t.Errorf("limit: %d", len(l))
	}

	created := time.Date(2026, 4, 30, 1, 2, 3, 4, time.UTC)
	at := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC)
	if err := s.SetClipTwitch(ctx, sid, c1.ID, ClipTwitch{Title: "Real title", ClipperName: "cl", BroadcasterName: "br",
		ThumbnailURL: "th", DurationSeconds: 28, CreatedAt: &created}, at); err != nil {
		t.Fatal(err)
	}
	g, _ := s.GetClip(ctx, sid, c1.ID)
	if g.Title != "Real title" || g.ClipperName != "cl" || g.BroadcasterName != "br" || g.ThumbnailURL != "th" ||
		g.DurationSeconds != 28 || g.ClipCreatedAt == nil || !g.ClipCreatedAt.Equal(created) ||
		g.TwitchLookupAt == nil || !g.TwitchLookupAt.Equal(at) {
		t.Errorf("clip = %+v", g)
	}
	// not found on Twitch: zero value, title kept, still marked looked up
	if err := s.SetClipTwitch(ctx, sid, c2.ID, ClipTwitch{}, at); err != nil {
		t.Fatal(err)
	}
	g, _ = s.GetClip(ctx, sid, c2.ID)
	if g.Title != "title-m2" || g.TwitchLookupAt == nil || g.ClipCreatedAt != nil {
		t.Errorf("zero lookup = %+v", g)
	}
	l, _ = s.PendingClipLookups(ctx, sid, 10)
	if len(l) != 1 || l[0].ID != c4.ID {
		t.Errorf("pending after = %+v", l)
	}
	if err := s.SetClipTwitch(ctx, other, c4.ID, ClipTwitch{Title: "x"}, at); !errors.Is(err, ErrNotFound) {
		t.Errorf("other server = %v", err)
	}
	if l, _ := s.PendingClipLookups(ctx, other, 10); len(l) != 1 {
		t.Errorf("other pending = %+v", l)
	}
}

func TestDeleteServerCascadesClips(t *testing.T) {
	s := newStore(t)
	a, b := mustServer(t, s, "ga"), mustServer(t, s, "gb")
	mustClip(t, s, clip(a.ID, "m1", time.Now()))
	mustClip(t, s, clip(b.ID, "m2", time.Now()))
	if err := s.DeleteServer(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM clips WHERE server_id = ?`, a.ID); n != 0 {
		t.Errorf("clips left = %d", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM clips WHERE server_id = ?`, b.ID); n != 1 {
		t.Errorf("other clips = %d", n)
	}
}

func TestMigration010OnExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old := openLegacy(t, path)
	if err := old.migrateUpTo(ctx, 9); err != nil {
		t.Fatal(err)
	}
	now := fmtTime(time.Now())
	if _, err := old.db.ExecContext(ctx, `INSERT INTO servers (id, guild_id, name, suggestions_channel_id, created_at, updated_at) VALUES (1, 'g', 'n', 'sc', '`+now+`', '`+now+`')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	g, err := s.GetServer(ctx, 1)
	if err != nil || g.ClipsChannelID != "" || g.ClipsCursor != "" || g.SuggestionsChannelID != "sc" {
		t.Fatalf("server = %+v, %v", g, err)
	}
	if _, err := s.UpdateServer(ctx, 1, ServerPatch{ClipsChannelID: ptr("cc")}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertClip(ctx, clip(1, "m", time.Now())); err != nil {
		t.Fatal(err)
	}
	if l, err := s.ListClips(ctx, ClipFilter{ServerID: 1}); err != nil || len(l) != 1 {
		t.Fatalf("clips = %+v, %v", l, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM schema_migrations`); n != 11 {
		t.Errorf("migrations = %d, want 11", n)
	}
}

func TestEditorRolesRoundTrip(t *testing.T) {
	s := newStore(t)
	a, err := s.CreateServer(ctx, Server{GuildID: "g1", Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	if a.ClipEditorRoleIDs == nil || len(a.ClipEditorRoleIDs) != 0 || a.VideoEditorRoleIDs == nil || len(a.VideoEditorRoleIDs) != 0 {
		t.Errorf("nil lists = %#v %#v", a.ClipEditorRoleIDs, a.VideoEditorRoleIDs)
	}
	b, err := s.CreateServer(ctx, Server{GuildID: "g2", Name: "b", ClipEditorRoleIDs: []string{"c1", "c2"}, VideoEditorRoleIDs: []string{"v1"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.ClipEditorRoleIDs, []string{"c1", "c2"}) || !slices.Equal(b.VideoEditorRoleIDs, []string{"v1"}) {
		t.Errorf("created = %#v %#v", b.ClipEditorRoleIDs, b.VideoEditorRoleIDs)
	}
	got, err := s.GetServerByGuild(ctx, "g2")
	if err != nil || !slices.Equal(got.ClipEditorRoleIDs, b.ClipEditorRoleIDs) || !slices.Equal(got.VideoEditorRoleIDs, b.VideoEditorRoleIDs) {
		t.Errorf("by guild = %+v, %v", got, err)
	}
	list, err := s.ListServers(ctx)
	if err != nil || len(list) != 2 || !slices.Equal(list[1].VideoEditorRoleIDs, []string{"v1"}) || list[0].VideoEditorRoleIDs == nil {
		t.Errorf("list = %+v, %v", list, err)
	}
	upd, err := s.UpdateServer(ctx, a.ID, ServerPatch{ClipEditorRoleIDs: ptr([]string{"x"})})
	if err != nil || !slices.Equal(upd.ClipEditorRoleIDs, []string{"x"}) || upd.VideoEditorRoleIDs == nil || len(upd.VideoEditorRoleIDs) != 0 {
		t.Errorf("clip patch = %+v, %v", upd, err)
	}
	upd, err = s.UpdateServer(ctx, a.ID, ServerPatch{VideoEditorRoleIDs: ptr([]string{"y", "z"})})
	if err != nil || !slices.Equal(upd.ClipEditorRoleIDs, []string{"x"}) || !slices.Equal(upd.VideoEditorRoleIDs, []string{"y", "z"}) {
		t.Errorf("video patch = %+v, %v", upd, err)
	}
	upd, err = s.UpdateServer(ctx, a.ID, ServerPatch{ClipEditorRoleIDs: ptr([]string(nil)), VideoEditorRoleIDs: ptr([]string(nil))})
	if err != nil || upd.ClipEditorRoleIDs == nil || len(upd.ClipEditorRoleIDs) != 0 || upd.VideoEditorRoleIDs == nil || len(upd.VideoEditorRoleIDs) != 0 {
		t.Errorf("nil patch = %+v, %v", upd, err)
	}
	var raw string
	if err := s.db.QueryRowContext(ctx, `SELECT video_editor_role_ids FROM servers WHERE id = ?`, a.ID).Scan(&raw); err != nil || raw != "[]" {
		t.Errorf("stored = %q, %v", raw, err)
	}
}

func TestAccessSectionFlagsRoundTrip(t *testing.T) {
	s := newStore(t)
	a, err := s.CreateServer(ctx, Server{GuildID: "g1", Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	t1 := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < 16; i++ {
		in := AccessEntry{Allowed: i&1 != 0, CanManage: i&2 != 0, CanVideos: i&4 != 0, CanClips: i&8 != 0, CheckedAt: t1}
		if err := s.SetAccess(ctx, a.ID, "u", in); err != nil {
			t.Fatal(err)
		}
		e, found, err := s.GetAccess(ctx, a.ID, "u")
		if err != nil || !found || e.Allowed != in.Allowed || e.CanManage != in.CanManage ||
			e.CanVideos != in.CanVideos || e.CanClips != in.CanClips || !e.CheckedAt.Equal(t1) {
			t.Errorf("flags %04b: got %+v found=%v err=%v", i, e, found, err)
		}
	}
}

func TestMigration011OnExistingDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old := openLegacy(t, path)
	if err := old.migrateUpTo(ctx, 10); err != nil {
		t.Fatal(err)
	}
	now := fmtTime(time.Now())
	if _, err := old.db.ExecContext(ctx, `INSERT INTO servers (id, guild_id, name, created_at, updated_at) VALUES (1, 'g', 'n', '`+now+`', '`+now+`')`); err != nil {
		t.Fatal(err)
	}
	if err := old.UpsertUser(ctx, User{DiscordID: "u", Username: "u"}); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(ctx, `INSERT INTO server_access (server_id, user_id, allowed, can_manage, checked_at) VALUES (1, 'u', 1, 1, '`+now+`')`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	if _, found, err := s.GetAccess(ctx, 1, "u"); err != nil || found {
		t.Errorf("access kept: found=%v err=%v", found, err)
	}
	g, err := s.GetServer(ctx, 1)
	if err != nil || g.ClipEditorRoleIDs == nil || len(g.ClipEditorRoleIDs) != 0 || g.VideoEditorRoleIDs == nil || len(g.VideoEditorRoleIDs) != 0 {
		t.Fatalf("server = %+v, %v", g, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM schema_migrations`); n != 11 {
		t.Errorf("migrations = %d, want 11", n)
	}
}
