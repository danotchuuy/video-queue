package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danotchuuy/video-queue/internal/store"
)

type fakeStore struct {
	mu       sync.Mutex
	users    map[string]store.User
	sessions map[string]string // hash -> user id
	guilds   map[string][]store.UserGuild
}

func newFakeStore() *fakeStore {
	return &fakeStore{users: map[string]store.User{}, sessions: map[string]string{}, guilds: map[string][]store.UserGuild{}}
}

func (f *fakeStore) UpsertUser(_ context.Context, u store.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.DiscordID] = u
	return nil
}

func (f *fakeStore) CreateSession(_ context.Context, hash, uid string, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[hash] = uid
	return nil
}

func (f *fakeStore) UserBySession(_ context.Context, hash string, _ time.Time) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	uid, ok := f.sessions[hash]
	if !ok {
		return store.User{}, store.ErrNotFound
	}
	return f.users[uid], nil
}

func (f *fakeStore) DeleteSession(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, hash)
	return nil
}

func (f *fakeStore) ReplaceUserGuilds(_ context.Context, uid string, gs []store.UserGuild, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.guilds[uid] = gs
	return nil
}

type fakeDiscord struct {
	*httptest.Server
	userID     string
	guildCalls int
	guildPages [][]map[string]any // served by /users/@me/guilds, one page per call
	guildFail  bool
	guildAfter []string
}

func newFakeDiscord(t *testing.T, userID string) *fakeDiscord {
	d := &fakeDiscord{userID: userID}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.PostForm.Get("grant_type") != "authorization_code" || r.PostForm.Get("code") != "good" ||
			r.PostForm.Get("client_secret") != "secret" || r.PostForm.Get("client_id") != "cid" ||
			r.PostForm.Get("redirect_uri") != "http://app/cb" {
			http.Error(w, "bad", 400)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"access_token": "at"})
	})
	mux.HandleFunc("GET /users/@me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at" {
			http.Error(w, "unauth", 401)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"id": d.userID, "username": "u", "global_name": "User", "avatar": "av"})
	})
	mux.HandleFunc("GET /users/@me/guilds", func(w http.ResponseWriter, r *http.Request) {
		d.guildCalls++
		if r.Header.Get("Authorization") != "Bearer at" || r.URL.Query().Get("limit") != "200" || d.guildFail {
			http.Error(w, "bad", 500)
			return
		}
		d.guildAfter = append(d.guildAfter, r.URL.Query().Get("after"))
		page := []map[string]any{}
		if i := len(d.guildAfter) - 1; i < len(d.guildPages) {
			page = d.guildPages[i]
		}
		json.NewEncoder(w).Encode(page)
	})
	mux.HandleFunc("GET /users/@me/guilds/{gid}/member", func(w http.ResponseWriter, r *http.Request) {
		d.guildCalls++
		http.Error(w, "unexpected", 500)
	})
	d.Server = httptest.NewServer(mux)
	t.Cleanup(d.Close)
	return d
}

func newTestService(t *testing.T, d *fakeDiscord, st SessionStore) (*Service, *http.ServeMux) {
	s := New(Config{
		ClientID: "cid", ClientSecret: "secret", RedirectURL: "http://app/cb",
		CookieSecure: true,
	}, st)
	if d != nil {
		s.apiBase = d.URL
	}
	mux := http.NewServeMux()
	s.Register(mux)
	return s, mux
}

func do(mux http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func cookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// runCallback performs login then callback and returns the callback response.
func runCallback(t *testing.T, mux http.Handler, code string) *httptest.ResponseRecorder {
	t.Helper()
	login := do(mux, httptest.NewRequest("GET", "/api/auth/login", nil))
	sc := cookie(login, stateCookie)
	if sc == nil {
		t.Fatal("no state cookie")
	}
	req := httptest.NewRequest("GET", "/api/auth/callback?code="+code+"&state="+url.QueryEscape(sc.Value), nil)
	req.AddCookie(sc)
	return do(mux, req)
}

func TestLogin(t *testing.T) {
	_, mux := newTestService(t, nil, newFakeStore())
	rec := do(mux, httptest.NewRequest("GET", "/api/auth/login", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status %d", rec.Code)
	}
	sc := cookie(rec, stateCookie)
	if sc == nil || !sc.HttpOnly || sc.MaxAge != 600 || sc.SameSite != http.SameSiteLaxMode {
		t.Fatalf("bad state cookie: %+v", sc)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme+"://"+loc.Host+loc.Path != "https://discord.com/oauth2/authorize" {
		t.Errorf("location %s", loc)
	}
	q := loc.Query()
	want := map[string]string{
		"response_type": "code", "client_id": "cid", "scope": "identify guilds",
		"state": sc.Value, "redirect_uri": "http://app/cb",
	}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
}

func TestCallbackStateMismatch(t *testing.T) {
	d := newFakeDiscord(t, "u1")
	_, mux := newTestService(t, d, newFakeStore())
	req := httptest.NewRequest("GET", "/api/auth/callback?code=good&state=bad", nil)
	req.AddCookie(&http.Cookie{Name: stateCookie, Value: "other"})
	if rec := do(mux, req); rec.Code != http.StatusBadRequest {
		t.Errorf("mismatch: status %d", rec.Code)
	}
	req = httptest.NewRequest("GET", "/api/auth/callback?code=good&state=bad", nil)
	if rec := do(mux, req); rec.Code != http.StatusBadRequest {
		t.Errorf("no cookie: status %d", rec.Code)
	}
}

func TestCallbackCreatesSession(t *testing.T) {
	d := newFakeDiscord(t, "u1")
	st := newFakeStore()
	_, mux := newTestService(t, d, st)
	rec := runCallback(t, mux, "good")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/" {
		t.Fatalf("status %d loc %q", rec.Code, rec.Header().Get("Location"))
	}
	sc := cookie(rec, sessionCookie)
	if sc == nil {
		t.Fatal("no session cookie")
	}
	if !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteLaxMode || sc.Path != "/" || sc.MaxAge != 7*24*3600 {
		t.Errorf("bad cookie flags: %+v", sc)
	}
	if len(st.sessions) != 1 {
		t.Fatalf("sessions: %v", st.sessions)
	}
	for hash, uid := range st.sessions {
		if hash == sc.Value || hash != hashToken(sc.Value) || uid != "u1" {
			t.Errorf("hash %q cookie %q uid %q", hash, sc.Value, uid)
		}
	}
	if st.users["u1"].GlobalName != "User" {
		t.Errorf("user not upserted: %+v", st.users)
	}
	if c := cookie(rec, stateCookie); c == nil || c.MaxAge >= 0 {
		t.Error("state cookie not cleared")
	}
	if d.guildCalls != 1 {
		t.Errorf("guild endpoint called %d times", d.guildCalls)
	}
}

func TestCallbackStoresGuilds(t *testing.T) {
	d := newFakeDiscord(t, "u1")
	d.guildPages = [][]map[string]any{{
		{"id": "1", "name": "Owned", "icon": "i1", "owner": true, "permissions": "0"},
		{"id": "2", "name": "Admin", "icon": "", "owner": false, "permissions": "8"},
		{"id": "3", "name": "Manage", "icon": "", "owner": false, "permissions": "32"},
		{"id": "4", "name": "Plain", "icon": "", "owner": false, "permissions": "1024"},
	}}
	st := newFakeStore()
	_, mux := newTestService(t, d, st)
	if rec := runCallback(t, mux, "good"); rec.Header().Get("Location") != "/" {
		t.Fatalf("loc %q", rec.Header().Get("Location"))
	}
	want := []store.UserGuild{
		{GuildID: "1", Name: "Owned", Icon: "i1", CanManage: true},
		{GuildID: "2", Name: "Admin", CanManage: true},
		{GuildID: "3", Name: "Manage", CanManage: true},
		{GuildID: "4", Name: "Plain", CanManage: false},
	}
	got := st.guilds["u1"]
	if len(got) != len(want) {
		t.Fatalf("guilds %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("guild %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestCallbackGuildsPagination(t *testing.T) {
	d := newFakeDiscord(t, "u1")
	var p1 []map[string]any
	for i := 0; i < 200; i++ {
		p1 = append(p1, map[string]any{"id": strconv.Itoa(1000 + i), "name": "g", "permissions": "0"})
	}
	d.guildPages = [][]map[string]any{p1, {{"id": "5000", "name": "last", "owner": true, "permissions": "0"}}}
	st := newFakeStore()
	_, mux := newTestService(t, d, st)
	runCallback(t, mux, "good")
	if len(d.guildAfter) != 2 || d.guildAfter[0] != "" || d.guildAfter[1] != "1199" {
		t.Errorf("after params %q", d.guildAfter)
	}
	got := st.guilds["u1"]
	if len(got) != 201 || got[200].GuildID != "5000" || !got[200].CanManage {
		t.Errorf("guilds: %d", len(got))
	}
}

func TestCallbackGuildsFailureStillLogsIn(t *testing.T) {
	d := newFakeDiscord(t, "u1")
	d.guildFail = true
	st := newFakeStore()
	_, mux := newTestService(t, d, st)
	rec := runCallback(t, mux, "good")
	if rec.Header().Get("Location") != "/" || cookie(rec, sessionCookie) == nil {
		t.Fatalf("login blocked: %q", rec.Header().Get("Location"))
	}
	if len(st.sessions) != 1 || len(st.guilds) != 0 {
		t.Errorf("sessions %v guilds %v", st.sessions, st.guilds)
	}
}

func TestCallbackUpstreamAndCancel(t *testing.T) {
	d := newFakeDiscord(t, "u1")
	_, mux := newTestService(t, d, newFakeStore())
	if loc := runCallback(t, mux, "badcode").Header().Get("Location"); loc != "/login?error=discord" {
		t.Errorf("upstream loc %q", loc)
	}
	login := do(mux, httptest.NewRequest("GET", "/api/auth/login", nil))
	sc := cookie(login, stateCookie)
	req := httptest.NewRequest("GET", "/api/auth/callback?error=access_denied&state="+sc.Value, nil)
	req.AddCookie(sc)
	if loc := do(mux, req).Header().Get("Location"); loc != "/login?error=cancelled" {
		t.Errorf("cancel loc %q", loc)
	}
}

func TestRequire(t *testing.T) {
	st := newFakeStore()
	st.users["u1"] = store.User{DiscordID: "u1", Username: "u"}
	st.sessions[hashToken("tok")] = "u1"
	s, _ := newTestService(t, nil, st)
	h := s.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := UserFromContext(r.Context())
		if !ok {
			t.Error("no user in context")
		}
		w.Write([]byte(u.DiscordID))
	}))
	withCookie := func(req *http.Request, v string) *http.Request {
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: v})
		return req
	}

	rec := do(h, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 401 || rec.Header().Get("Content-Type") != "application/json" ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"not signed in"}` {
		t.Errorf("no cookie: %d %q", rec.Code, rec.Body)
	}
	if rec := do(h, withCookie(httptest.NewRequest("GET", "/x", nil), "bogus")); rec.Code != 401 {
		t.Errorf("bad session: %d", rec.Code)
	}
	rec = do(h, withCookie(httptest.NewRequest("GET", "/x", nil), "tok"))
	if rec.Code != 200 || rec.Body.String() != "u1" {
		t.Errorf("valid: %d %q", rec.Code, rec.Body)
	}

	// Mutating request with a body needs JSON.
	req := withCookie(httptest.NewRequest("POST", "/x", strings.NewReader("a=b")), "tok")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if rec := do(h, req); rec.Code != 415 {
		t.Errorf("form post: %d", rec.Code)
	}
	req = withCookie(httptest.NewRequest("POST", "/x", strings.NewReader("{}")), "tok")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if rec := do(h, req); rec.Code != 200 {
		t.Errorf("json post: %d", rec.Code)
	}
	req = withCookie(httptest.NewRequest("DELETE", "/x", nil), "tok")
	if rec := do(h, req); rec.Code != 200 {
		t.Errorf("empty-body delete: %d", rec.Code)
	}
}

func TestLogout(t *testing.T) {
	st := newFakeStore()
	st.sessions[hashToken("tok")] = "u1"
	_, mux := newTestService(t, nil, st)
	req := httptest.NewRequest("POST", "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "tok"})
	rec := do(mux, req)
	if rec.Code != 204 {
		t.Errorf("status %d", rec.Code)
	}
	if len(st.sessions) != 0 {
		t.Error("session not deleted")
	}
	if c := cookie(rec, sessionCookie); c == nil || c.MaxAge >= 0 {
		t.Errorf("cookie not cleared: %+v", c)
	}
	// Without a session it still succeeds and clears the cookie.
	rec = do(mux, httptest.NewRequest("POST", "/api/auth/logout", nil))
	if rec.Code != 204 || cookie(rec, sessionCookie) == nil {
		t.Errorf("anonymous logout: %d", rec.Code)
	}
}
