package discord

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danotchuuy/video-queue/internal/store"
)

type fakeStore struct {
	mu      sync.Mutex
	servers map[int64]store.Server
	sugg    map[string]store.Suggestion // key: serverID/messageID
	saves   map[int64][]string          // cursor saves per server
	clips   map[string]store.Clip       // key: serverID/messageID
	// clipSaves records clips cursor saves per server.
	clipSaves map[int64][]string
	// onSetCursor, if set, runs before each cursor save is recorded.
	onSetCursor func(serverID int64)
}

func newFakeStore(servers ...store.Server) *fakeStore {
	f := &fakeStore{servers: map[int64]store.Server{}, sugg: map[string]store.Suggestion{}, saves: map[int64][]string{}, clips: map[string]store.Clip{}, clipSaves: map[int64][]string{}}
	for _, s := range servers {
		f.servers[s.ID] = s
	}
	return f
}

func (f *fakeStore) ListServers(context.Context) ([]store.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Server
	for _, s := range f.servers {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) GetServer(_ context.Context, id int64) (store.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.servers[id]
	if !ok {
		return store.Server{}, store.ErrNotFound
	}
	return s, nil
}

func (f *fakeStore) UpsertSuggestion(_ context.Context, in store.Suggestion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sugg[suggKey(in.ServerID, in.DiscordMessageID)] = in
	return nil
}

func (f *fakeStore) SetSuggestionsCursor(_ context.Context, id int64, cursor string) error {
	if f.onSetCursor != nil {
		f.onSetCursor(id)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.servers[id]
	s.SuggestionsCursor = cursor
	f.servers[id] = s
	f.saves[id] = append(f.saves[id], cursor)
	return nil
}

func (f *fakeStore) UpsertClip(_ context.Context, in store.Clip) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clips[suggKey(in.ServerID, in.DiscordMessageID)] = in
	return nil
}

func (f *fakeStore) SetClipsCursor(_ context.Context, id int64, cursor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.servers[id]
	s.ClipsCursor = cursor
	f.servers[id] = s
	f.clipSaves[id] = append(f.clipSaves[id], cursor)
	return nil
}

func (f *fakeStore) setClipsChannel(id int64, channel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.servers[id]
	s.ClipsChannelID = channel
	s.ClipsCursor = ""
	f.servers[id] = s
}

func (f *fakeStore) clipsCursor(id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.servers[id].ClipsCursor
}

func (f *fakeStore) clip(serverID int64, msgID string) (store.Clip, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.clips[suggKey(serverID, msgID)]
	return c, ok
}

func (f *fakeStore) clipCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clips)
}

func (f *fakeStore) setChannel(id int64, channel string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.servers[id]
	s.SuggestionsChannelID = channel
	s.SuggestionsCursor = ""
	f.servers[id] = s
}

func (f *fakeStore) cursor(id int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.servers[id].SuggestionsCursor
}

func (f *fakeStore) suggestion(serverID int64, msgID string) (store.Suggestion, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sugg[suggKey(serverID, msgID)]
	return s, ok
}

func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sugg)
}

func suggKey(serverID int64, msgID string) string {
	return strconv.FormatInt(serverID, 10) + "/" + msgID
}

// fakeDiscord serves the messages endpoint over in-memory channels.
type fakeDiscord struct {
	mu       sync.Mutex
	msgs     map[string][]Message // by channel; any order
	failing  map[string]bool      // channels answering 500
	requests []string             // "channel?rawquery"
}

func (d *fakeDiscord) set(channel string, m Message) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.msgs == nil {
		d.msgs = map[string][]Message{}
	}
	list := d.msgs[channel]
	for i := range list {
		if list[i].ID == m.ID {
			list[i] = m
			return
		}
	}
	d.msgs[channel] = append(list, m)
}

func (d *fakeDiscord) fail(channel string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failing == nil {
		d.failing = map[string]bool{}
	}
	d.failing[channel] = true
}

func (d *fakeDiscord) reqs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.requests...)
}

func (d *fakeDiscord) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	channel := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/channels/"), "/messages")
	d.requests = append(d.requests, channel+"?"+r.URL.RawQuery)
	if d.failing[channel] {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	after, _ := strconv.ParseUint(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	var out []Message
	for _, m := range d.msgs[channel] {
		id, _ := strconv.ParseUint(m.ID, 10, 64)
		if id > after {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { // newest first
		a, _ := strconv.ParseUint(out[i].ID, 10, 64)
		b, _ := strconv.ParseUint(out[j].ID, 10, 64)
		return a > b
	})
	if r.URL.Query().Get("after") != "" && limit < len(out) {
		// Real Discord returns the 100 oldest after the cursor.
		out = out[len(out)-limit:]
	} else if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	json.NewEncoder(w).Encode(out)
}

func msg(id int, content string) Message {
	return Message{
		ID: strconv.Itoa(id), Content: content, Timestamp: "2024-05-06T07:08:09.5+00:00",
		Author: Author{ID: "u1", Username: "user", GlobalName: "User One"},
	}
}

func setup(t *testing.T, servers ...store.Server) (*fakeDiscord, *fakeStore, *Poller) {
	t.Helper()
	d := &fakeDiscord{}
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	c := NewClient("tok")
	c.baseURL = srv.URL
	st := newFakeStore(servers...)
	p := NewPoller(c, st, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return d, st, p
}

func srvRow(id int64, channel string) store.Server {
	return store.Server{ID: id, Name: "srv" + strconv.FormatInt(id, 10), SuggestionsChannelID: channel}
}

func TestSyncPaginatesAndPersistsCursor(t *testing.T) {
	d, st, p := setup(t, srvRow(1, "chan"))
	for i := 1; i <= 250; i++ {
		d.set("chan", msg(i, "video "+strconv.Itoa(i)))
	}
	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if st.count() != 250 {
		t.Errorf("suggestions = %d, want 250", st.count())
	}
	if st.cursor(1) != "250" {
		t.Errorf("cursor = %q", st.cursor(1))
	}
	if got := strings.Join(st.saves[1], ","); got != "100,200,250" {
		t.Errorf("cursor saves = %s", got)
	}
	s, _ := st.suggestion(1, "1")
	if s.ServerID != 1 || s.ChannelID != "chan" || s.AuthorName != "User One" || s.AuthorID != "u1" || s.PostedAt.IsZero() {
		t.Errorf("suggestion = %+v", s)
	}
	// 3 forward pages (100,100,50) + 1 recent refresh.
	if len(d.reqs()) != 4 {
		t.Errorf("requests = %v", d.reqs())
	}
}

func TestSyncStartsFromServerCursor(t *testing.T) {
	row := srvRow(1, "chan")
	row.SuggestionsCursor = "5"
	d, st, p := setup(t, row)
	for i := 1; i <= 8; i++ {
		d.set("chan", msg(i, "v"))
	}
	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if got := d.reqs()[0]; got != "chan?after=5&limit=100" {
		t.Errorf("first request = %q", got)
	}
	if st.cursor(1) != "8" {
		t.Errorf("cursor = %q", st.cursor(1))
	}
}

func TestSyncFiltersAndContent(t *testing.T) {
	d, st, p := setup(t, srvRow(1, "chan"))
	bot := msg(1, "from bot")
	bot.Author.Bot = true
	sys := msg(2, "joined")
	sys.Type = 7
	reply := msg(3, "a reply")
	reply.Type = 19
	embed := msg(4, "")
	embed.Embeds = []Embed{{URL: "http://embed"}}
	attach := msg(5, "")
	attach.Attachments = []Attachment{{URL: "http://attach"}}
	empty := msg(6, "")
	noGlobal := msg(7, "plain")
	noGlobal.Author.GlobalName = ""
	voted := msg(8, "popular")
	voted.Reactions = []Reaction{{Count: 2}, {Count: 5}}
	for _, m := range []Message{bot, sys, reply, embed, attach, empty, noGlobal, voted} {
		d.set("chan", m)
	}
	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2", "6"} {
		if _, ok := st.suggestion(1, id); ok {
			t.Errorf("message %s should be skipped", id)
		}
	}
	if st.count() != 5 {
		t.Errorf("suggestions = %d, want 5", st.count())
	}
	get := func(id string) store.Suggestion { s, _ := st.suggestion(1, id); return s }
	if get("3").Content != "a reply" {
		t.Error("reply not kept")
	}
	if get("4").Content != "http://embed" || get("5").Content != "http://attach" {
		t.Errorf("fallback: %q %q", get("4").Content, get("5").Content)
	}
	if get("7").AuthorName != "user" || get("7").AuthorUsername != "user" {
		t.Errorf("author = %q / %q", get("7").AuthorName, get("7").AuthorUsername)
	}
	if g := get("8"); g.AuthorName != "User One" || g.AuthorUsername != "user" {
		t.Errorf("author = %q / %q", g.AuthorName, g.AuthorUsername)
	}
	if get("8").ReactionCount != 7 {
		t.Errorf("reactions = %d", get("8").ReactionCount)
	}
	if st.cursor(1) != "8" {
		t.Errorf("cursor = %q (skipped messages must still advance it)", st.cursor(1))
	}
}

func TestSecondSyncFetchesNewerAndRefreshesReactions(t *testing.T) {
	d, st, p := setup(t, srvRow(1, "chan"))
	d.set("chan", msg(1, "one"))
	d.set("chan", msg(2, "two"))
	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}

	liked := msg(2, "two")
	liked.Reactions = []Reaction{{Count: 4}}
	d.set("chan", liked)
	d.set("chan", msg(3, "three"))
	d.mu.Lock()
	d.requests = nil
	d.mu.Unlock()

	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if s, _ := st.suggestion(1, "2"); s.ReactionCount != 4 {
		t.Errorf("reaction count not refreshed: %d", s.ReactionCount)
	}
	if _, ok := st.suggestion(1, "3"); !ok || st.cursor(1) != "3" {
		t.Errorf("new message missing; cursor %q", st.cursor(1))
	}
	want := []string{"chan?after=2&limit=100", "chan?limit=100"}
	got := d.reqs()
	if len(got) != len(want) {
		t.Fatalf("requests = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestServersSyncIndependently(t *testing.T) {
	d, st, p := setup(t, srvRow(1, "chanA"), srvRow(2, "chanB"))
	d.set("chanA", msg(10, "a1"))
	d.set("chanA", msg(11, "a2"))
	d.set("chanB", msg(5, "b1"))
	ctx := context.Background()
	if err := p.SyncServer(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if st.cursor(1) != "11" || st.cursor(2) != "" {
		t.Errorf("cursors = %q, %q", st.cursor(1), st.cursor(2))
	}
	if st.count() != 2 {
		t.Errorf("suggestions = %d, want 2", st.count())
	}
	if err := p.SyncServer(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if st.cursor(1) != "11" || st.cursor(2) != "5" {
		t.Errorf("cursors = %q, %q", st.cursor(1), st.cursor(2))
	}
	b, ok := st.suggestion(2, "5")
	if !ok || b.ServerID != 2 || b.ChannelID != "chanB" {
		t.Errorf("server 2 suggestion = %+v ok=%v", b, ok)
	}
	if _, ok := st.suggestion(2, "10"); ok {
		t.Error("server 2 got server 1's message")
	}
}

func TestSyncServerWithoutChannelIsNoop(t *testing.T) {
	d, st, p := setup(t, srvRow(1, ""))
	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(d.reqs()) != 0 || st.count() != 0 {
		t.Errorf("requests = %v, suggestions = %d", d.reqs(), st.count())
	}
}

func TestSyncServerUnknown(t *testing.T) {
	_, _, p := setup(t)
	if err := p.SyncServer(context.Background(), 9); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestRunSkipsServersWithoutChannelAndIsolatesErrors(t *testing.T) {
	d, st, p := setup(t, srvRow(1, "bad"), srvRow(2, ""), srvRow(3, "good"))
	d.fail("bad")
	d.set("bad", msg(1, "x"))
	d.set("good", msg(7, "ok"))
	p.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	waitFor(t, func() bool { return st.cursor(3) == "7" })
	cancel()
	<-done
	if st.cursor(1) != "" {
		t.Errorf("failing server cursor = %q", st.cursor(1))
	}
	for _, r := range d.reqs() {
		if strings.HasPrefix(r, "?") {
			t.Errorf("request for server without channel: %q", r)
		}
	}
}

func TestChannelChangeMidSyncStopsCursorSaving(t *testing.T) {
	d, st, p := setup(t, srvRow(1, "old"))
	for i := 1; i <= 250; i++ {
		d.set("old", msg(i, "v"))
	}
	// Change the channel before the first page's cursor is saved.
	orig := p.store
	p.store = &hookStore{SuggestionStore: orig, beforeGetServer: func(n int) {
		if n == 2 { // 1st = start of sync, 2nd = pre-save check
			st.setChannel(1, "new")
		}
	}}
	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(st.saves[1]) != 0 {
		t.Errorf("cursor saved after channel change: %v", st.saves[1])
	}
	if st.cursor(1) != "" {
		t.Errorf("cursor = %q", st.cursor(1))
	}
	if n := len(d.reqs()); n != 1 {
		t.Errorf("requests after abort = %d, want 1", n)
	}
}

// hookStore runs a hook before each GetServer, simulating concurrent edits.
type hookStore struct {
	SuggestionStore
	n               int
	beforeGetServer func(n int)
}

func (h *hookStore) GetServer(ctx context.Context, id int64) (store.Server, error) {
	h.n++
	h.beforeGetServer(h.n)
	return h.SuggestionStore.GetServer(ctx, id)
}

func TestSyncSerializedPerServer(t *testing.T) {
	d, st, p := setup(t, srvRow(1, "chan"))
	d.set("chan", msg(1, "one"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.SyncServer(context.Background(), 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if st.cursor(1) != "1" {
		t.Errorf("cursor = %q", st.cursor(1))
	}
}

func TestSnowflakeGreater(t *testing.T) {
	if !snowflakeGreater("1000", "999") || snowflakeGreater("999", "1000") || snowflakeGreater("5", "5") {
		t.Error("numeric comparison wrong")
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	d, st, p := setup(t, srvRow(1, "chan"))
	d.set("chan", msg(1, "one"))
	p.interval = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	waitFor(t, func() bool { return st.count() == 1 })
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestSyncWithoutMessageContentKeepsCursor(t *testing.T) {
	d, st, p := setup(t, srvRow(1, "chan"))
	// Without the Message Content intent, user messages arrive blank.
	d.set("chan", msg(1, ""))
	d.set("chan", msg(2, ""))
	bot := msg(3, "bot messages keep their content")
	bot.Author.Bot = true
	d.set("chan", bot)

	if err := p.SyncServer(context.Background(), 1); !errors.Is(err, ErrMessageContentMissing) {
		t.Fatalf("err = %v, want ErrMessageContentMissing", err)
	}
	if st.cursor(1) != "" || st.count() != 0 {
		t.Fatalf("cursor = %q, suggestions = %d; want untouched", st.cursor(1), st.count())
	}

	// A page with at least one real message is processed normally.
	d.set("chan", msg(4, "https://youtu.be/abc"))
	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if st.cursor(1) != "4" || st.count() != 1 {
		t.Fatalf("cursor = %q, suggestions = %d; want 4 and 1", st.cursor(1), st.count())
	}
}

func TestAfterSyncRunsOnlyOnSuccess(t *testing.T) {
	d, _, p := setup(t, srvRow(1, "chan"), srvRow(2, "bad"))
	d.set("chan", msg(1, "https://youtu.be/abcdefghijk"))
	d.fail("bad")
	var calls []int64
	p.AfterSync = func(_ context.Context, id int64) { calls = append(calls, id) }

	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := p.SyncServer(context.Background(), 2); err == nil {
		t.Fatal("want error for failing channel")
	}
	if len(calls) != 1 || calls[0] != 1 {
		t.Fatalf("AfterSync calls = %v, want [1]", calls)
	}
}

func clipSrv(id int64, suggestions, clips string) store.Server {
	s := srvRow(id, suggestions)
	s.ClipsChannelID = clips
	s.GuildID = "guild9"
	return s
}

func TestClipsChannelHasOwnCursor(t *testing.T) {
	row := clipSrv(1, "sug", "clp")
	row.SuggestionsCursor = "50"
	d, st, p := setup(t, row)
	d.set("sug", msg(60, "https://youtu.be/abcdefghijk"))
	for i := 1; i <= 120; i++ {
		d.set("clp", msg(i, "https://clips.twitch.tv/Slug"+strconv.Itoa(i)))
	}
	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if st.cursor(1) != "60" || st.clipsCursor(1) != "120" {
		t.Errorf("cursors = %q / %q", st.cursor(1), st.clipsCursor(1))
	}
	if got := strings.Join(st.clipSaves[1], ","); got != "100,120" {
		t.Errorf("clip cursor saves = %s", got)
	}
	if st.clipCount() != 120 || st.count() != 1 {
		t.Errorf("clips = %d, suggestions = %d", st.clipCount(), st.count())
	}
	var clipFirst string
	for _, r := range d.reqs() {
		if strings.HasPrefix(r, "clp?") {
			clipFirst = r
			break
		}
	}
	if clipFirst != "clp?after=0&limit=100" {
		t.Errorf("first clips request = %q", clipFirst)
	}
	c, _ := st.clip(1, "7")
	if c.ChannelID != "clp" || c.ServerID != 1 || c.TwitchClipID != "Slug7" {
		t.Errorf("clip = %+v", c)
	}
}

func TestToClip(t *testing.T) {
	p := &Poller{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := store.Server{ID: 3, GuildID: "g1"}
	conv := func(m Message) (store.Clip, bool) { return p.toClip(srv, "ch", m) }

	c, ok := conv(msg(42, "look at this https://clips.twitch.tv/FunnySlug-abc, so   good"))
	if !ok || c.ClipURL != "https://clips.twitch.tv/FunnySlug-abc" || c.TwitchClipID != "FunnySlug-abc" {
		t.Fatalf("twitch clip = %+v ok=%v", c, ok)
	}
	if c.Title != "look at this so good" {
		t.Errorf("title = %q", c.Title)
	}
	if c.MessageURL != "https://discord.com/channels/g1/ch/42" {
		t.Errorf("message_url = %q", c.MessageURL)
	}
	if c.AuthorName != "User One" || c.AuthorUsername != "user" || c.AuthorID != "u1" || c.ServerID != 3 || c.ChannelID != "ch" || c.PostedAt.IsZero() {
		t.Errorf("clip = %+v", c)
	}

	c, ok = conv(msg(1, "clips.twitch.tv/NoScheme wow"))
	if !ok || c.ClipURL != "https://clips.twitch.tv/NoScheme" || c.TwitchClipID != "NoScheme" || c.Title != "wow" {
		t.Errorf("scheme-less = %+v ok=%v", c, ok)
	}

	c, ok = conv(msg(2, "see <https://example.com/v/1>. nice"))
	if !ok || c.ClipURL != "https://example.com/v/1" || c.TwitchClipID != "" || c.Title != "see nice" {
		t.Errorf("other link = %+v ok=%v", c, ok)
	}

	att := msg(3, "from a file")
	att.Attachments = []Attachment{{URL: "https://cdn.example/clip.mp4"}}
	c, ok = conv(att)
	if !ok || c.ClipURL != "https://cdn.example/clip.mp4" || c.TwitchClipID != "" || c.Title != "from a file" {
		t.Errorf("attachment = %+v ok=%v", c, ok)
	}

	if _, ok := conv(msg(4, "just chatting")); ok {
		t.Error("message without link should be skipped")
	}
	bot := msg(5, "https://clips.twitch.tv/Abc")
	bot.Author.Bot = true
	if _, ok := conv(bot); ok {
		t.Error("bot message should be skipped")
	}

	long := msg(6, "https://clips.twitch.tv/Abc "+strings.Repeat("é", 400))
	c, _ = conv(long)
	if n := len([]rune(c.Title)); n != 300 {
		t.Errorf("title runes = %d, want 300", n)
	}

	noGlobal := msg(7, "https://clips.twitch.tv/Abc")
	noGlobal.Author.GlobalName = ""
	if c, _ = conv(noGlobal); c.AuthorName != "user" {
		t.Errorf("author name = %q", c.AuthorName)
	}
}

func TestClipsChannelChangeMidSyncStopsCursorSaving(t *testing.T) {
	d, st, p := setup(t, clipSrv(1, "", "old"))
	for i := 1; i <= 250; i++ {
		d.set("old", msg(i, "https://clips.twitch.tv/S"+strconv.Itoa(i)))
	}
	p.store = &hookStore{SuggestionStore: p.store, beforeGetServer: func(n int) {
		if n == 2 {
			st.setClipsChannel(1, "new")
		}
	}}
	if err := p.SyncServer(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(st.clipSaves[1]) != 0 || st.clipsCursor(1) != "" {
		t.Errorf("clips cursor saved after channel change: %v / %q", st.clipSaves[1], st.clipsCursor(1))
	}
	if n := len(d.reqs()); n != 1 {
		t.Errorf("requests after abort = %d, want 1", n)
	}
}

func TestOneChannelFailingStillSyncsOther(t *testing.T) {
	d, st, p := setup(t, clipSrv(1, "sug", "clp"), clipSrv(2, "ok", "badclp"))
	d.fail("sug")
	d.set("sug", msg(1, "x"))
	d.set("clp", msg(5, "https://clips.twitch.tv/Abc"))
	d.set("ok", msg(3, "https://youtu.be/abcdefghijk"))
	d.fail("badclp")
	var calls []int64
	p.AfterSync = func(_ context.Context, id int64) { calls = append(calls, id) }

	err := p.SyncServer(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "suggestions") {
		t.Fatalf("err = %v, want suggestions failure", err)
	}
	if st.clipsCursor(1) != "5" || st.clipCount() != 1 {
		t.Errorf("clips not synced: cursor %q, clips %d", st.clipsCursor(1), st.clipCount())
	}
	if len(calls) != 1 || calls[0] != 1 {
		t.Errorf("AfterSync calls = %v, want [1]", calls)
	}

	err = p.SyncServer(context.Background(), 2)
	if err == nil || !strings.Contains(err.Error(), "clips") {
		t.Fatalf("err = %v, want clips failure", err)
	}
	if st.cursor(2) != "3" {
		t.Errorf("suggestions cursor = %q", st.cursor(2))
	}
	if len(calls) != 2 || calls[1] != 2 {
		t.Errorf("AfterSync calls = %v, want [1 2]", calls)
	}
}

func TestAfterSyncSkippedWhenAllChannelsFail(t *testing.T) {
	d, _, p := setup(t, clipSrv(1, "sug", "clp"))
	d.fail("sug")
	d.fail("clp")
	called := false
	p.AfterSync = func(context.Context, int64) { called = true }
	err := p.SyncServer(context.Background(), 1)
	if err == nil || !strings.Contains(err.Error(), "suggestions") || !strings.Contains(err.Error(), "clips") {
		t.Errorf("err = %v, want both failures", err)
	}
	if called {
		t.Error("AfterSync ran although nothing synced")
	}
}

func TestRunSyncsServerWithOnlyClipsChannel(t *testing.T) {
	d, st, p := setup(t, clipSrv(1, "", "clp"))
	d.set("clp", msg(9, "https://clips.twitch.tv/Abc"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	waitFor(t, func() bool { return st.clipsCursor(1) == "9" })
	cancel()
	<-done
	if st.clipCount() != 1 {
		t.Errorf("clips = %d", st.clipCount())
	}
}
