package discord

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(srv *httptest.Server) *Client {
	c := NewClient("tok")
	c.baseURL = srv.URL
	return c
}

func TestChannelMessagesRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/channels/42/messages" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bot tok" {
			t.Errorf("auth = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != userAgent {
			t.Errorf("user-agent = %q", got)
		}
		q := r.URL.Query()
		if q.Get("after") != "7" || q.Get("limit") != "100" {
			t.Errorf("query = %v", q)
		}
		w.Write([]byte(`[{"id":"9","type":0,"content":"hi","timestamp":"2024-01-02T03:04:05.123000+00:00",
			"author":{"id":"1","username":"u","global_name":"G","bot":false},
			"embeds":[{"url":"http://e"}],"attachments":[{"url":"http://a"}],"reactions":[{"count":3}]}]`))
	}))
	defer srv.Close()

	msgs, err := newTestClient(srv).ChannelMessages(context.Background(), "42", MessageQuery{After: "7", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Author.GlobalName != "G" || msgs[0].Embeds[0].URL != "http://e" ||
		msgs[0].Attachments[0].URL != "http://a" || msgs[0].Reactions[0].Count != 3 {
		t.Errorf("msgs = %+v", msgs)
	}
}

func TestChannelMessagesNoParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q", r.URL.RawQuery)
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	if _, err := newTestClient(srv).ChannelMessages(context.Background(), "1", MessageQuery{}); err != nil {
		t.Fatal(err)
	}
}

func TestChannelMessagesRetry429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"message":"slow down","retry_after":0.05}`))
			return
		}
		w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	start := time.Now()
	if _, err := newTestClient(srv).ChannelMessages(context.Background(), "1", MessageQuery{}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d", calls.Load())
	}
	if time.Since(start) < 50*time.Millisecond {
		t.Error("did not wait retry_after")
	}
}

func TestChannelMessagesRetryOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"retry_after":0.01}`))
	}))
	defer srv.Close()
	_, err := newTestClient(srv).ChannelMessages(context.Background(), "1", MessageQuery{})
	if err == nil || !strings.Contains(err.Error(), "429") {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d", calls.Load())
	}
}

func TestChannelMessages429Cancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"retry_after":30}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := newTestClient(srv).ChannelMessages(ctx, "1", MessageQuery{}); err == nil {
		t.Fatal("expected error")
	}
}

func TestChannelMessagesError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(strings.Repeat("x", 2000)))
	}))
	defer srv.Close()
	_, err := newTestClient(srv).ChannelMessages(context.Background(), "1", MessageQuery{})
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v", err)
	}
	if len(err.Error()) > 1000 {
		t.Errorf("error body not truncated: %d bytes", len(err.Error()))
	}
}

func jsonServer(t *testing.T, wantPath string, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != wantPath {
			t.Errorf("path = %q, want %q", r.URL.EscapedPath(), wantPath)
		}
		if r.Header.Get("Authorization") != "Bot tok" || r.Header.Get("User-Agent") != userAgent {
			t.Errorf("headers = %v", r.Header)
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestBotGuildsPagination(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/users/@me/guilds" {
			t.Errorf("path = %q", r.URL.Path)
		}
		queries = append(queries, r.URL.RawQuery)
		var sb strings.Builder
		sb.WriteString("[")
		n := 200
		start := 1
		if r.URL.Query().Get("after") != "" {
			n, start = 3, 201
		}
		for i := 0; i < n; i++ {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, `{"id":"%d","name":"g%d","icon":"i"}`, start+i, start+i)
		}
		sb.WriteString("]")
		w.Write([]byte(sb.String()))
	}))
	defer srv.Close()
	gs, err := newTestClient(srv).BotGuilds(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(gs) != 203 || gs[0].ID != "1" || gs[202].ID != "203" || gs[0].Icon != "i" {
		t.Errorf("guilds = %d first=%+v", len(gs), gs[0])
	}
	if len(queries) != 2 || queries[0] != "limit=200" || queries[1] != "limit=200&after=200" {
		t.Errorf("queries = %v", queries)
	}
}

func TestGuild(t *testing.T) {
	srv := jsonServer(t, "/guilds/g%2F1", `{"id":"g/1","name":"N","icon":null}`)
	g, err := newTestClient(srv).Guild(context.Background(), "g/1")
	if err != nil {
		t.Fatal(err)
	}
	if g.ID != "g/1" || g.Name != "N" || g.Icon != "" {
		t.Errorf("guild = %+v", g)
	}
}

func TestGuildRolesFilterAndSort(t *testing.T) {
	srv := jsonServer(t, "/guilds/10/roles", `[
		{"id":"10","name":"@everyone","position":0},
		{"id":"1","name":"low","position":1,"color":5},
		{"id":"2","name":"bot","position":9,"managed":true},
		{"id":"3","name":"high","position":7},
		{"id":"4","name":"mid","position":3}]`)
	rs, err := newTestClient(srv).GuildRoles(context.Background(), "10")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "3,4,1" || rs[2].Color != 5 {
		t.Errorf("roles = %+v", rs)
	}
}

func TestGuildTextChannelsFilterAndSort(t *testing.T) {
	srv := jsonServer(t, "/guilds/10/channels", `[
		{"id":"7","name":"voice","type":2,"position":0},
		{"id":"5","name":"b","type":0,"position":2,"parent_id":"99"},
		{"id":"3","name":"news","type":5,"position":2},
		{"id":"4","name":"cat","type":4,"position":0},
		{"id":"9","name":"a","type":0,"position":1}]`)
	cs, err := newTestClient(srv).GuildTextChannels(context.Background(), "10")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, c := range cs {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "9,3,5" || cs[2].ParentID != "99" {
		t.Errorf("channels = %+v", cs)
	}
}

func TestGuildMember(t *testing.T) {
	srv := jsonServer(t, "/guilds/10/members/20", `{"roles":["1","2"],"user":{"id":"20"}}`)
	m, err := newTestClient(srv).GuildMember(context.Background(), "10", "20")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Roles) != 2 || m.Roles[1] != "2" {
		t.Errorf("member = %+v", m)
	}
}

func TestNotFoundMapping(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		notFound bool
	}{
		{"404", 404, `{"code":10004,"message":"Unknown Guild"}`, true},
		{"404 plain", 404, ``, true},
		{"403 missing access", 403, `{"code":50001,"message":"Missing Access"}`, true},
		{"403 other code", 403, `{"code":50013,"message":"Missing Permissions"}`, false},
		{"403 not json", 403, `forbidden`, false},
		{"500", 500, `{"code":50001}`, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := newTestClient(srv)
			ctx := context.Background()
			_, e1 := c.Guild(ctx, "1")
			_, e2 := c.GuildMember(ctx, "1", "2")
			_, e3 := c.GuildRoles(ctx, "1")
			_, e4 := c.GuildTextChannels(ctx, "1")
			_, e5 := c.BotGuilds(ctx)
			for i, err := range []error{e1, e2, e3, e4, e5} {
				if err == nil {
					t.Fatalf("call %d: expected error", i)
				}
				if got := errors.Is(err, ErrNotFound); got != tc.notFound {
					t.Errorf("call %d: errors.Is(ErrNotFound) = %v, want %v (%v)", i, got, tc.notFound, err)
				}
				if !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
					t.Errorf("call %d: error lacks status: %v", i, err)
				}
			}
		})
	}
}
