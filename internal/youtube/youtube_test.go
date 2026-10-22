package youtube

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func TestParseVideoID(t *testing.T) {
	const id = "dQw4w9WgXcQ"
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{id, id, true},
		{"  " + id + "\n", id, true},
		{"https://www.youtube.com/watch?v=" + id, id, true},
		{"http://youtube.com/watch?v=" + id, id, true},
		{"https://www.youtube.com/watch?v=" + id + "&list=PLabc&t=10s", id, true},
		{"https://www.youtube.com/watch?feature=share&v=" + id, id, true},
		{"https://m.youtube.com/watch?v=" + id, id, true},
		{"https://music.youtube.com/watch?v=" + id, id, true},
		{"https://WWW.YOUTUBE.COM/watch?v=" + id, id, true},
		{"https://youtu.be/" + id, id, true},
		{"https://youtu.be/" + id + "?t=5", id, true},
		{"https://www.youtube.com/shorts/" + id, id, true},
		{"https://www.youtube.com/embed/" + id, id, true},
		{"https://www.youtube.com/live/" + id + "?feature=share", id, true},
		{"https://www.youtube.com/v/" + id, id, true},
		{"", "", false},
		{"https://youtube.com.evil.com/watch?v=" + id, "", false},
		{"https://notyoutube.com/watch?v=" + id, "", false},
		{"https://evil.com/?u=youtube.com/watch?v=" + id, "", false},
		{"https://user@www.youtube.com/watch?v=" + id, "", false},
		{"https://www.youtube.com:8080/watch?v=" + id, "", false},
		{"https://www.youtube.com:443/watch?v=" + id, "", false},
		{"javascript:alert(1)//youtube.com/watch?v=" + id, "", false},
		{"ftp://www.youtube.com/watch?v=" + id, "", false},
		{"//www.youtube.com/watch?v=" + id, "", false},
		{"https://www.youtube.com/watch?v=" + id[:10], "", false},
		{"https://www.youtube.com/watch?v=" + id + "x", "", false},
		{"https://www.youtube.com/watch?v=dQw4w9WgX!Q", "", false},
		{"https://www.youtube.com/watch", "", false},
		{"https://www.youtube.com/playlist?list=PLabc", "", false},
		{"https://www.youtube.com/shorts/" + id + "/extra", "", false},
		{"https://youtu.be/", "", false},
		{id[:10], "", false},
		{id + "x", "", false},
		{"dQw4w9WgX!Q", "", false},
		{"dQw4w9WgXc:", "", false},
	}
	for _, tt := range tests {
		got, ok := ParseVideoID(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseVideoID(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParsePlaylistID(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{"PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf", "PLrAXtmErZgOeiKm4sgNOknGvNjby9efdf", true},
		{"UUabc", "UUabc", true},
		{"OLAK5uy_x", "OLAK5uy_x", true},
		{"FLabc", "FLabc", true},
		{"RDabc", "RDabc", true},
		{"LL", "LL", true},
		{"https://www.youtube.com/playlist?list=PLabc123", "PLabc123", true},
		{"https://youtube.com/watch?v=dQw4w9WgXcQ&list=PLabc123", "PLabc123", true},
		{"https://m.youtube.com/playlist?list=PLabc123", "PLabc123", true},
		{"https://music.youtube.com/playlist?list=OLAK5uy_x", "OLAK5uy_x", true},
		{"https://WWW.YouTube.com/playlist?list=PLabc123", "PLabc123", true},
		{"https://youtu.be/dQw4w9WgXcQ?list=PLabc123", "PLabc123", true},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=RDdQw4w9WgXcQ", "RDdQw4w9WgXcQ", true},
		{"https://www.youtube.com/show/VLPLHovnlOusNLgvAbnxluXCVB3KLj8e4QB-?sbp=KgtId0FQTGtfc1Ezd0AB", "PLHovnlOusNLgvAbnxluXCVB3KLj8e4QB-", true},
		{"https://youtube.com/show/VLPLabc123/", "PLabc123", true},
		{"https://music.youtube.com/browse/VLOLAK5uy_x", "OLAK5uy_x", true},
		{"https://www.youtube.com/show/VLPLabc123?list=PLother", "PLother", true}, // explicit list= wins
		{"https://www.youtube.com/show/SCq6f7v1hJ1T_nUHgv_JZvRA", "", false},      // not a playlist-backed show
		{"https://www.youtube.com/show/VL", "", false},
		{"https://www.youtube.com/show/VLabcdef", "", false}, // no playlist prefix
		{"https://www.youtube.com/show/VLPL<x>", "", false},
		{"https://www.youtube.com/show/VLPLabc/extra", "", false},
		{"https://www.youtube.com/channel/VLPLabc123", "", false},
		{"https://evil.com/show/VLPLabc123", "", false},
		{"", "", false},
		{"abcdef", "", false},
		{"P", "", false},
		{"PL" + strings.Repeat("a", 63), "", false},
		{"PLab!", "", false},
		{"https://www.youtube.com/playlist", "", false},
		{"https://www.youtube.com/playlist?list=", "", false},
		{"https://www.youtube.com/playlist?list=a", "", false},
		{"https://www.youtube.com/playlist?list=PL<x>", "", false},
		{"https://evil.com/playlist?list=PLabc", "", false},
		{"https://youtube.com.evil.com/playlist?list=PLabc", "", false},
		{"https://www.youtube.com:81/playlist?list=PLabc", "", false},
		{"javascript:alert(1)", "", false},
		{"ftp://www.youtube.com/playlist?list=PLabc", "", false},
	}
	for _, tt := range tests {
		got, ok := ParsePlaylistID(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParsePlaylistID(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"PT1H2M3S", 3723},
		{"PT45S", 45},
		{"PT5M", 300},
		{"PT2H", 7200},
		{"P1DT2H", 93600},
		{"P1D", 86400},
		{"P0D", 0},
		{"PT0S", 0},
		{"PT1M30.5S", 90},
		{"PT0.9S", 0},
		{"", 0},
		{"P", 0},
		{"PT", 0},
		{"1H", 0},
		{"PT1X", 0},
		{"PTxS", 0},
		{"P1W", 0},
		{"PT99999999999999999999S", 0},
	}
	for _, tt := range tests {
		if got := parseDuration(tt.in); got != tt.want {
			t.Errorf("parseDuration(%q) = %d; want %d", tt.in, got, tt.want)
		}
	}
}

const testKey = "SECRET-KEY-123"

// fakeAPI is an httptest server recording requests.
type fakeAPI struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newFake(t *testing.T, h http.HandlerFunc) (*fakeAPI, *Client) {
	t.Helper()
	f := &fakeAPI{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.reqs = append(f.reqs, r)
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(f.Close)
	c := NewClient(testKey)
	c.baseURL = f.URL
	return f, c
}

func videoJSON(id string) string {
	return fmt.Sprintf(`{"id":%q,"snippet":{"title":"T-%s","channelTitle":"Chan","publishedAt":"2020-01-02T03:04:05Z"},"contentDetails":{"duration":"PT1M5S"}}`, id, id)
}

// videosHandler answers /videos for every requested id except those in missing.
func videosHandler(missing ...string) http.HandlerFunc {
	skip := map[string]bool{}
	for _, m := range missing {
		skip[m] = true
	}
	return func(w http.ResponseWriter, r *http.Request) {
		var items []string
		for _, id := range strings.Split(r.URL.Query().Get("id"), ",") {
			if !skip[id] {
				items = append(items, videoJSON(id))
			}
		}
		fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
	}
}

func TestVideosBatching(t *testing.T) {
	f, c := newFake(t, videosHandler())
	var ids []string
	for i := 0; i < 120; i++ {
		ids = append(ids, fmt.Sprintf("id%09d", i))
	}
	vs, err := c.Videos(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 3 {
		t.Fatalf("requests = %d; want 3", len(f.reqs))
	}
	for i, r := range f.reqs {
		q := r.URL.Query()
		if r.URL.Path != "/videos" || q.Get("key") != testKey || q.Get("part") != "snippet,contentDetails" {
			t.Errorf("request %d: bad path/params %s", i, r.URL)
		}
		lo, hi := i*50, min(i*50+50, 120)
		if got, want := q.Get("id"), strings.Join(ids[lo:hi], ","); got != want {
			t.Errorf("request %d id = %q; want %q", i, got, want)
		}
	}
	if len(vs) != 120 {
		t.Fatalf("got %d videos", len(vs))
	}
	for i, v := range vs {
		if v.ID != ids[i] {
			t.Fatalf("video %d = %s; want %s", i, v.ID, ids[i])
		}
	}
	v := vs[0]
	if v.Title != "T-"+ids[0] || v.ChannelTitle != "Chan" || v.DurationSeconds != 65 ||
		v.URL != "https://www.youtube.com/watch?v="+ids[0] || v.ThumbnailURL != ThumbnailURL(ids[0]) ||
		v.PublishedAt.Year() != 2020 {
		t.Errorf("unexpected video %+v", v)
	}
}

func TestVideosOrderAndMissing(t *testing.T) {
	_, c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		// Respond in a different order than requested and omit "gone".
		fmt.Fprintf(w, `{"items":[%s,%s]}`, videoJSON("ccc"), videoJSON("aaa"))
	})
	vs, err := c.Videos(context.Background(), []string{"aaa", "gone", "ccc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 2 || vs[0].ID != "aaa" || vs[1].ID != "ccc" {
		t.Errorf("got %+v", vs)
	}
}

func TestVideosEmpty(t *testing.T) {
	f, c := newFake(t, videosHandler())
	vs, err := c.Videos(context.Background(), nil)
	if err != nil || len(vs) != 0 || len(f.reqs) != 0 {
		t.Errorf("vs=%v err=%v reqs=%d", vs, err, len(f.reqs))
	}
}

func TestVideosChannelID(t *testing.T) {
	_, c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"items":[{"id":"aaa","snippet":{"title":"T","channelId":"UC123","channelTitle":"Chan"},"contentDetails":{"duration":"PT1S"}}]}`)
	})
	vs, err := c.Videos(context.Background(), []string{"aaa"})
	if err != nil || len(vs) != 1 || vs[0].ChannelID != "UC123" {
		t.Errorf("vs=%+v err=%v", vs, err)
	}
}

// channelsHandler answers /channels with a handle per id from handles; ids
// missing from the map get no customUrl.
func channelsHandler(handles map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var items []string
		for _, id := range strings.Split(r.URL.Query().Get("id"), ",") {
			items = append(items, fmt.Sprintf(`{"id":%q,"snippet":{"customUrl":%q}}`, id, handles[id]))
		}
		fmt.Fprintf(w, `{"items":[%s]}`, strings.Join(items, ","))
	}
}

func TestChannelHandlesBatching(t *testing.T) {
	handles := map[string]string{}
	var ids []string
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("UC%09d", i)
		ids = append(ids, id)
		handles[id] = "@h" + id
	}
	f, c := newFake(t, channelsHandler(handles))
	got, err := c.ChannelHandles(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 3 {
		t.Fatalf("requests = %d; want 3", len(f.reqs))
	}
	for i, r := range f.reqs {
		q := r.URL.Query()
		if r.URL.Path != "/channels" || q.Get("key") != testKey || q.Get("part") != "snippet" {
			t.Errorf("request %d: bad path/params %s", i, r.URL)
		}
		lo, hi := i*50, min(i*50+50, 120)
		if want := strings.Join(ids[lo:hi], ","); q.Get("id") != want {
			t.Errorf("request %d id = %q; want %q", i, q.Get("id"), want)
		}
	}
	if len(got) != 120 || got[ids[7]] != "@h"+ids[7] {
		t.Errorf("got %d handles, [7]=%q", len(got), got[ids[7]])
	}
}

func TestChannelHandlesDedupeNormalizeOmit(t *testing.T) {
	f, c := newFake(t, channelsHandler(map[string]string{"a": "creator", "b": "@Already", "c": ""}))
	got, err := c.ChannelHandles(context.Background(), []string{"a", "b", "a", "", "c", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.reqs) != 1 || f.reqs[0].URL.Query().Get("id") != "a,b,c" {
		t.Errorf("requests = %v", f.reqs)
	}
	want := map[string]string{"a": "@creator", "b": "@Already"}
	if len(got) != len(want) || got["a"] != want["a"] || got["b"] != want["b"] {
		t.Errorf("got %v; want %v", got, want)
	}
}

func TestChannelHandlesEmpty(t *testing.T) {
	f, c := newFake(t, channelsHandler(nil))
	for _, in := range [][]string{nil, {}, {""}} {
		got, err := c.ChannelHandles(context.Background(), in)
		if err != nil || len(got) != 0 {
			t.Errorf("got=%v err=%v", got, err)
		}
	}
	if len(f.reqs) != 0 {
		t.Errorf("requests = %d; want 0", len(f.reqs))
	}
}

func TestChannelHandlesErrorDoesNotLeakKey(t *testing.T) {
	_, c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":"quota exceeded for %s"}`, r.URL.String())
	})
	_, err := c.ChannelHandles(context.Background(), []string{"a"})
	if err == nil || strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "403") {
		t.Errorf("err = %v", err)
	}
}

func TestVideoNotFound(t *testing.T) {
	_, c := newFake(t, videosHandler("nope"))
	if _, err := c.Video(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v; want ErrNotFound", err)
	}
	v, err := c.Video(context.Background(), "yes")
	if err != nil || v.ID != "yes" {
		t.Errorf("v=%+v err=%v", v, err)
	}
}

func TestHTTP404IsNotFound(t *testing.T) {
	_, c := newFake(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	if _, err := c.Video(context.Background(), "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestErrorsDoNotLeakKey(t *testing.T) {
	_, c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		// Echo the request URL back, as some error pages do.
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprintf(w, `{"error":"quota exceeded for %s"}`, r.URL.String())
	})
	_, err := c.Video(context.Background(), "abc")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), testKey) {
		t.Errorf("error leaks key: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error lacks status: %v", err)
	}

	// Transport errors carry the URL; the key must be stripped.
	c2 := NewClient(testKey)
	c2.baseURL = "http://127.0.0.1:1"
	_, err = c2.Video(context.Background(), "abc")
	if err == nil || strings.Contains(err.Error(), testKey) {
		t.Errorf("transport error = %v", err)
	}
}

func TestContextCanceled(t *testing.T) {
	_, c := newFake(t, videosHandler())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Video(ctx, "abc"); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

type plItem struct {
	title, vid string
	pos        int
}

func plItemJSON(it plItem) string {
	return fmt.Sprintf(`{"snippet":{"title":%q,"position":%d},"contentDetails":{"videoId":%q}}`, it.title, it.pos, it.vid)
}

func TestPlaylistPaging(t *testing.T) {
	// 3 pages: 50 + 50 + 3 entries, with a few unplayable ones.
	var all []plItem
	for i := 0; i < 103; i++ {
		all = append(all, plItem{fmt.Sprintf("title %d", i), fmt.Sprintf("v%010d", i), i})
	}
	all[5].title = "Deleted video"
	all[6].title = "Private video"
	all[7].vid = ""
	f, c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.URL.Path {
		case "/playlists":
			fmt.Fprint(w, `{"items":[{"snippet":{"title":"My List","channelTitle":"Chan"}}]}`)
		case "/playlistItems":
			start := 0
			next := ""
			switch q.Get("pageToken") {
			case "":
				next = "p2"
			case "p2":
				start, next = 50, "p3"
			case "p3":
				start = 100
			}
			end := min(start+50, len(all))
			var items []string
			// Return the page reversed to check that output is sorted by position.
			for i := end - 1; i >= start; i-- {
				items = append(items, plItemJSON(all[i]))
			}
			tok := ""
			if next != "" && end < len(all) {
				tok = fmt.Sprintf(`,"nextPageToken":%q`, next)
			}
			fmt.Fprintf(w, `{"items":[%s]%s}`, strings.Join(items, ","), tok)
		case "/videos":
			videosHandler("v0000000010")(w, r)
		}
	})
	p, items, err := c.Playlist(context.Background(), "PLxyz")
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != "PLxyz" || p.Title != "My List" || p.ChannelTitle != "Chan" ||
		p.URL != "https://www.youtube.com/playlist?list=PLxyz" || p.ThumbnailURL != ThumbnailURL(all[0].vid) {
		t.Errorf("playlist = %+v", p)
	}
	// 103 minus 3 unplayable minus 1 not returned by /videos.
	if len(items) != 99 {
		t.Fatalf("items = %d; want 99", len(items))
	}
	last := 0
	for _, it := range items {
		if it.Position <= last {
			t.Fatalf("not ordered: %d after %d", it.Position, last)
		}
		last = it.Position
		if it.DurationSeconds != 65 {
			t.Errorf("duration not merged for %s", it.ID)
		}
		if it.ID == all[5].vid || it.ID == all[6].vid || it.ID == "v0000000010" {
			t.Errorf("should have been skipped: %s", it.ID)
		}
	}
	if items[0].Position != 1 || items[0].ID != all[0].vid {
		t.Errorf("first = %+v", items[0])
	}
	if items[len(items)-1].Position != 103 {
		t.Errorf("last position = %d", items[len(items)-1].Position)
	}

	var tokens []string
	for _, r := range f.reqs {
		if r.URL.Query().Get("key") != testKey {
			t.Errorf("missing key on %s", r.URL.Path)
		}
		if r.URL.Path == "/playlistItems" {
			if r.URL.Query().Get("playlistId") != "PLxyz" {
				t.Errorf("playlistId = %q", r.URL.Query().Get("playlistId"))
			}
			tokens = append(tokens, r.URL.Query().Get("pageToken"))
		}
	}
	if got := strings.Join(tokens, ","); got != ",p2,p3" {
		t.Errorf("page tokens = %q", got)
	}
}

func TestPlaylistCap(t *testing.T) {
	var pages int
	_, c := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/playlists":
			fmt.Fprint(w, `{"items":[{"snippet":{"title":"Big"}}]}`)
		case "/playlistItems":
			pages++
			n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Query().Get("pageToken"), "t"))
			var items []string
			for i := 0; i < 50; i++ {
				pos := n*50 + i
				items = append(items, plItemJSON(plItem{"t", fmt.Sprintf("v%010d", pos), pos}))
			}
			// Always claim there is another page.
			fmt.Fprintf(w, `{"items":[%s],"nextPageToken":"t%d"}`, strings.Join(items, ","), n+1)
		case "/videos":
			videosHandler()(w, r)
		}
	})
	_, items, err := c.Playlist(context.Background(), "PLbig")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != MaxPlaylistItems || pages != MaxPlaylistItems/50 {
		t.Errorf("items=%d pages=%d", len(items), pages)
	}
}

func TestPlaylistNotFound(t *testing.T) {
	_, c := newFake(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"items":[]}`) })
	if _, _, err := c.Playlist(context.Background(), "PLnone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v", err)
	}
}
