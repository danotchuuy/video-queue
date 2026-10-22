package access

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/danotchuuy/video-queue/internal/discord"
	"github.com/danotchuuy/video-queue/internal/store"
)

type fakeStore struct {
	mu      sync.Mutex
	entries map[string]store.AccessEntry
	gets    int
}

func (f *fakeStore) GetAccess(_ context.Context, serverID int64, userID string) (store.AccessEntry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gets++
	e, ok := f.entries[key(serverID, userID)]
	return e, ok, nil
}

func (f *fakeStore) SetAccess(_ context.Context, serverID int64, userID string, e store.AccessEntry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.entries[key(serverID, userID)] = e
	return nil
}

func (f *fakeStore) getCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gets
}

func key(serverID int64, userID string) string {
	return string(rune('0'+serverID)) + "/" + userID
}

type fakeDiscord struct {
	mu          sync.Mutex
	memberCalls int
	guildCalls  int
	member      discord.Member
	memberErr   error
	guild       discord.Guild
	guildErr    error
	release     chan struct{} // if set, GuildMember blocks until closed
}

func (f *fakeDiscord) GuildMember(_ context.Context, _, _ string) (discord.Member, error) {
	f.mu.Lock()
	f.memberCalls++
	f.mu.Unlock()
	if f.release != nil {
		<-f.release
	}
	return f.member, f.memberErr
}

func (f *fakeDiscord) Guild(_ context.Context, _ string) (discord.Guild, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guildCalls++
	return f.guild, f.guildErr
}

func (f *fakeDiscord) calls() (members, guilds int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.memberCalls, f.guildCalls
}

func (f *fakeDiscord) memberCount() int {
	m, _ := f.calls()
	return m
}

var (
	admin   = Perms{Access: true, Full: true, Manage: true, Videos: true, Clips: true}
	modP    = Perms{Access: true, Full: true, Videos: true, Clips: true}
	clipsP  = Perms{Access: true, Clips: true}
	videosP = Perms{Access: true, Videos: true}
	bothP   = Perms{Access: true, Videos: true, Clips: true}

	t0  = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	srv = store.Server{ID: 1, GuildID: "g1", ModRoleIDs: []string{"mod"}, ClipEditorRoleIDs: []string{"clip"}, VideoEditorRoleIDs: []string{"vid"}, AllowedUserIDs: []string{"streamer"}}

	testGuild = discord.Guild{
		ID:      "g1",
		OwnerID: "owner",
		Roles: []discord.Role{
			{ID: "g1", Permissions: "0"},
			{ID: "mgr", Permissions: "32"},
			{ID: "mod", Permissions: "0"},
		},
	}
)

func newChecker(d *fakeDiscord) (*Checker, *fakeStore, *time.Time) {
	if d.guild.ID == "" {
		d.guild = testGuild
	}
	st := &fakeStore{entries: map[string]store.AccessEntry{}}
	c := New(st, d, []string{"admin"}, 10*time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := t0
	c.now = func() time.Time { return now }
	return c, st, &now
}

func TestIsAdmin(t *testing.T) {
	c, _, _ := newChecker(&fakeDiscord{})
	if !c.IsAdmin("admin") || c.IsAdmin("u1") {
		t.Error("IsAdmin wrong")
	}
}

func TestCheckAdminNoFetch(t *testing.T) {
	d := &fakeDiscord{}
	c, st, _ := newChecker(d)
	got, err := c.Check(context.Background(), "admin", store.Server{ID: 1})
	if err != nil || got != admin {
		t.Errorf("got %v, %v", got, err)
	}
	if m, g := d.calls(); m != 0 || g != 0 || st.getCount() != 0 {
		t.Errorf("fetched: member %d guild %d gets %d", m, g, st.getCount())
	}
}

func TestCheckFetch(t *testing.T) {
	tests := []struct {
		name      string
		user      string
		member    discord.Member
		memberErr error
		want      Perms
	}{
		{"owner manages", "owner", discord.Member{}, nil, admin},
		{"manage role", "u1", discord.Member{Roles: []string{"mgr"}}, nil, admin},
		{"mod role", "u1", discord.Member{Roles: []string{"x", "mod"}}, nil, modP},
		{"no matching role", "u1", discord.Member{Roles: []string{"x"}}, nil, Perms{}},
		{"allowlisted member", "streamer", discord.Member{}, nil, modP},
		{"allowlisted non-member", "streamer", discord.Member{}, discord.ErrNotFound, modP},
		{"clip editor", "u1", discord.Member{Roles: []string{"clip"}}, nil, clipsP},
		{"video editor", "u1", discord.Member{Roles: []string{"vid"}}, nil, videosP},
		{"both editor roles", "u1", discord.Member{Roles: []string{"vid", "clip"}}, nil, bothP},
		{"mod and editor", "u1", discord.Member{Roles: []string{"clip", "mod"}}, nil, modP},
		{"non-member", "u1", discord.Member{}, discord.ErrNotFound, Perms{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDiscord{member: tt.member, memberErr: tt.memberErr}
			c, st, _ := newChecker(d)
			got, err := c.Check(context.Background(), tt.user, srv)
			if err != nil || got != tt.want {
				t.Fatalf("got %v, %v; want %v", got, err, tt.want)
			}
			if d.memberCount() != 1 {
				t.Errorf("GuildMember called %d times", d.memberCount())
			}
			e, ok := st.entries[key(srv.ID, tt.user)]
			want := store.AccessEntry{Allowed: tt.want.Full, CanManage: tt.want.Manage, CanVideos: tt.want.Videos, CanClips: tt.want.Clips, CheckedAt: t0}
			if !ok || e != want {
				t.Errorf("cache entry %+v ok=%v, want %+v", e, ok, want)
			}
		})
	}
}

func TestCheckCache(t *testing.T) {
	d := &fakeDiscord{member: discord.Member{Roles: []string{"x"}}}
	c, st, now := newChecker(d)
	st.entries[key(1, "u1")] = store.AccessEntry{Allowed: true, CanManage: true, CheckedAt: t0.Add(-9 * time.Minute)}

	got, err := c.Check(context.Background(), "u1", srv)
	if err != nil || got != admin || d.memberCount() != 0 {
		t.Fatalf("fresh: got %v, %v, calls %d", got, err, d.memberCount())
	}

	*now = t0.Add(2 * time.Minute) // entry is now 11m old
	got, err = c.Check(context.Background(), "u1", srv)
	if err != nil || got != (Perms{}) || d.memberCount() != 1 {
		t.Fatalf("stale: got %v, %v, calls %d", got, err, d.memberCount())
	}
	if e := st.entries[key(1, "u1")]; e.Allowed || e.CanManage || e.CanVideos || e.CanClips || !e.CheckedAt.Equal(*now) {
		t.Errorf("cache not updated: %+v", e)
	}
}

func TestCheckCacheSections(t *testing.T) {
	d := &fakeDiscord{memberErr: discord.ErrNotFound}
	c, st, _ := newChecker(d)
	tests := []struct {
		e    store.AccessEntry
		want Perms
	}{
		{store.AccessEntry{CanClips: true}, clipsP},
		{store.AccessEntry{CanVideos: true}, videosP},
		{store.AccessEntry{CanVideos: true, CanClips: true}, bothP},
		{store.AccessEntry{Allowed: true}, modP},
		{store.AccessEntry{Allowed: true, CanManage: true}, admin},
		{store.AccessEntry{}, Perms{}},
	}
	for i, tt := range tests {
		tt.e.CheckedAt = t0.Add(-time.Minute)
		user := string(rune('a' + i))
		st.entries[key(1, user)] = tt.e
		got, err := c.Check(context.Background(), user, srv)
		if err != nil || got != tt.want {
			t.Errorf("%+v: got %v, %v; want %v", tt.e, got, err, tt.want)
		}
	}
	if d.memberCount() != 0 {
		t.Errorf("fresh cache hit Discord %d times", d.memberCount())
	}
}

func TestCheckFetchError(t *testing.T) {
	boom := errors.New("boom")
	t.Run("recent cache", func(t *testing.T) {
		d := &fakeDiscord{memberErr: boom}
		c, st, _ := newChecker(d)
		st.entries[key(1, "u1")] = store.AccessEntry{Allowed: true, CanManage: true, CheckedAt: t0.Add(-23 * time.Hour)}
		got, err := c.Check(context.Background(), "u1", srv)
		if err != nil || got != admin {
			t.Errorf("got %v, %v; want cached", got, err)
		}
		if d.memberCount() != 1 {
			t.Errorf("calls %d", d.memberCount())
		}
	})
	t.Run("stale cache keeps section flags", func(t *testing.T) {
		c, st, _ := newChecker(&fakeDiscord{memberErr: boom})
		st.entries[key(1, "u1")] = store.AccessEntry{CanClips: true, CheckedAt: t0.Add(-2 * time.Hour)}
		got, err := c.Check(context.Background(), "u1", srv)
		if err != nil || got != clipsP {
			t.Errorf("got %v, %v; want %v", got, err, clipsP)
		}
	})
	t.Run("too old cache", func(t *testing.T) {
		c, st, _ := newChecker(&fakeDiscord{memberErr: boom})
		st.entries[key(1, "u1")] = store.AccessEntry{Allowed: true, CheckedAt: t0.Add(-25 * time.Hour)}
		if _, err := c.Check(context.Background(), "u1", srv); !errors.Is(err, boom) {
			t.Errorf("err %v", err)
		}
	})
	t.Run("allowlisted no cache", func(t *testing.T) {
		c, st, _ := newChecker(&fakeDiscord{memberErr: boom})
		got, err := c.Check(context.Background(), "streamer", srv)
		if err != nil || got != modP {
			t.Errorf("got %v, %v", got, err)
		}
		if len(st.entries) != 0 {
			t.Errorf("error result cached: %+v", st.entries)
		}
	})
	t.Run("no cache", func(t *testing.T) {
		c, st, _ := newChecker(&fakeDiscord{memberErr: boom})
		got, err := c.Check(context.Background(), "u1", srv)
		if !errors.Is(err, boom) || got != (Perms{}) {
			t.Errorf("got %v, %v", got, err)
		}
		if len(st.entries) != 0 {
			t.Errorf("error result cached: %+v", st.entries)
		}
	})
	t.Run("guild error", func(t *testing.T) {
		c, _, _ := newChecker(&fakeDiscord{member: discord.Member{Roles: []string{"mod"}}, guildErr: boom})
		if _, err := c.Check(context.Background(), "u1", srv); !errors.Is(err, boom) {
			t.Errorf("err %v", err)
		}
	})
}

func TestCheckCoalesces(t *testing.T) {
	const n = 10
	d := &fakeDiscord{member: discord.Member{Roles: []string{"mod"}}, release: make(chan struct{})}
	c, st, _ := newChecker(d)

	var wg sync.WaitGroup
	results := make([]Perms, n)
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = c.Check(context.Background(), "u1", srv)
		}()
	}

	// Wait until every caller has passed the cache check and the first one is
	// blocked in GuildMember, then give the others time to join the flight.
	deadline := time.Now().Add(5 * time.Second)
	for st.getCount() < n || d.memberCount() < 1 {
		if time.Now().After(deadline) {
			t.Fatal("callers did not all arrive")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	close(d.release)
	wg.Wait()

	for i := range n {
		if errs[i] != nil || results[i] != modP {
			t.Errorf("caller %d: %v, %v", i, results[i], errs[i])
		}
	}
	if got := d.memberCount(); got != 1 {
		t.Errorf("GuildMember called %d times, want 1", got)
	}
}

func TestGuildCache(t *testing.T) {
	d := &fakeDiscord{member: discord.Member{Roles: []string{"mod"}}}
	c, _, now := newChecker(d)
	ctx := context.Background()

	for _, u := range []string{"u1", "u2"} {
		if _, err := c.Check(ctx, u, srv); err != nil {
			t.Fatal(err)
		}
	}
	if _, g := d.calls(); g != 1 {
		t.Errorf("Guild called %d times within ttl, want 1", g)
	}

	*now = t0.Add(11 * time.Minute)
	if _, err := c.Check(ctx, "u3", srv); err != nil {
		t.Fatal(err)
	}
	if _, g := d.calls(); g != 2 {
		t.Errorf("Guild called %d times after ttl, want 2", g)
	}
}

func TestCanManageGuild(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name      string
		user      string
		member    discord.Member
		memberErr error
		guildErr  error
		want      bool
		wantErr   error
		wantCalls int
	}{
		{"admin", "admin", discord.Member{}, boom, boom, true, nil, 0},
		{"owner", "owner", discord.Member{}, nil, nil, true, nil, 1},
		{"manager role", "u1", discord.Member{Roles: []string{"mgr"}}, nil, nil, true, nil, 1},
		{"plain member", "u1", discord.Member{Roles: []string{"mod"}}, nil, nil, false, nil, 1},
		{"non-member", "u1", discord.Member{}, discord.ErrNotFound, nil, false, nil, 1},
		{"guild not found", "u1", discord.Member{}, nil, discord.ErrNotFound, false, nil, 1},
		{"member error", "u1", discord.Member{}, boom, nil, false, boom, 1},
		{"guild error", "u1", discord.Member{}, nil, boom, false, boom, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &fakeDiscord{member: tt.member, memberErr: tt.memberErr, guildErr: tt.guildErr}
			c, st, _ := newChecker(d)
			got, err := c.CanManageGuild(context.Background(), tt.user, "g1")
			if got != tt.want || !errors.Is(err, tt.wantErr) || (tt.wantErr == nil && err != nil) {
				t.Errorf("got %v, %v; want %v, %v", got, err, tt.want, tt.wantErr)
			}
			if d.memberCount() != tt.wantCalls {
				t.Errorf("GuildMember called %d times, want %d", d.memberCount(), tt.wantCalls)
			}
			if len(st.entries) != 0 || st.getCount() != 0 {
				t.Error("CanManageGuild touched the store")
			}
		})
	}
}
