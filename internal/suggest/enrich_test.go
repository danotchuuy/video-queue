package suggest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/danotchuuy/video-queue/internal/store"
	"github.com/danotchuuy/video-queue/internal/youtube"
)

func TestFirstVideoID(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"watch", "check https://www.youtube.com/watch?v=dQw4w9WgXcQ out", "dQw4w9WgXcQ"},
		{"youtu.be", "https://youtu.be/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"shorts", "https://youtube.com/shorts/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"suppressed", "<https://youtu.be/dQw4w9WgXcQ>", "dQw4w9WgXcQ"},
		{"spoiler", "||https://youtu.be/dQw4w9WgXcQ||", "dQw4w9WgXcQ"},
		{"spoiler suppressed", "||<https://youtu.be/dQw4w9WgXcQ>||", "dQw4w9WgXcQ"},
		{"markdown", "[cool video](https://youtu.be/dQw4w9WgXcQ)", "dQw4w9WgXcQ"},
		{"markdown spaced", "see [cool](https://youtu.be/dQw4w9WgXcQ) ok", "dQw4w9WgXcQ"},
		{"trailing punctuation", "wow https://youtu.be/dQw4w9WgXcQ.", "dQw4w9WgXcQ"},
		{"trailing mixed", "https://youtu.be/dQw4w9WgXcQ)?!", "dQw4w9WgXcQ"},
		{"first of several", "https://youtu.be/aaaaaaaaaaa and https://youtu.be/bbbbbbbbbbb", "aaaaaaaaaaa"},
		{"non-youtube ignored", "https://example.com/watch?v=dQw4w9WgXcQ https://youtu.be/bbbbbbbbbbb", "bbbbbbbbbbb"},
		{"non-youtube only", "https://example.com/foo", ""},
		{"parenthesized", "(https://youtu.be/dQw4w9WgXcQ)", "dQw4w9WgXcQ"},
		{"no scheme youtu.be", "pls react youtu.be/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"no scheme www", "www.youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"no scheme bare host", "youtube.com/watch?v=dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"no scheme uppercase", "YouTu.be/dQw4w9WgXcQ", "dQw4w9WgXcQ"},
		{"no scheme lookalike", "youtube.com.evil.example/watch?v=dQw4w9WgXcQ", ""},
		{"word starting youtu", "youtuber", ""},
		{"bare word", "reactionnn", ""},
		{"bare 11 letters", "abcdefghijk", ""},
		{"empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FirstVideoID(tt.in); got != tt.want {
				t.Errorf("FirstVideoID(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

type setCall struct {
	serverID, id int64
	v            store.SuggestionVideo
}

type fakeStore struct {
	pending []store.Suggestion
	calls   []setCall

	block        bool
	serverErr    error
	discardErr   error
	serverCalls  int
	discardCalls int
}

func (f *fakeStore) GetServer(_ context.Context, id int64) (store.Server, error) {
	f.serverCalls++
	return store.Server{ID: id, BlockSelfSuggestions: f.block}, f.serverErr
}

func (f *fakeStore) DiscardSelfSuggestions(_ context.Context, _ int64, _ time.Time) (int64, error) {
	f.discardCalls++
	return 0, f.discardErr
}

func (f *fakeStore) PendingVideoLookups(_ context.Context, _ int64, limit int) ([]store.Suggestion, error) {
	return f.pending, nil
}

func (f *fakeStore) SetSuggestionVideo(_ context.Context, serverID, id int64, v store.SuggestionVideo, _ time.Time) error {
	f.calls = append(f.calls, setCall{serverID, id, v})
	return nil
}

type fakeVideos struct {
	videos []youtube.Video
	err    error
	calls  [][]string

	handles     map[string]string
	handlesErr  error
	handleCalls [][]string
}

func (f *fakeVideos) ChannelHandles(_ context.Context, ids []string) (map[string]string, error) {
	f.handleCalls = append(f.handleCalls, slices.Clone(ids))
	return f.handles, f.handlesErr
}

func (f *fakeVideos) Videos(_ context.Context, ids []string) ([]youtube.Video, error) {
	f.calls = append(f.calls, slices.Clone(ids))
	return f.videos, f.err
}

func TestEnrichMixed(t *testing.T) {
	st := &fakeStore{pending: []store.Suggestion{
		{ID: 1, Content: "no link here"},
		{ID: 2, Content: "https://youtu.be/aaaaaaaaaaa"},
		{ID: 3, Content: "gone https://youtu.be/bbbbbbbbbbb"},
	}}
	yt := &fakeVideos{videos: []youtube.Video{{ID: "aaaaaaaaaaa", Title: "A", ChannelTitle: "Chan", DurationSeconds: 90}}}
	if err := NewEnricher(st, yt, nil).Enrich(context.Background(), 7); err != nil {
		t.Fatal(err)
	}
	want := []setCall{
		{7, 1, store.SuggestionVideo{}},
		{7, 2, store.SuggestionVideo{YouTubeVideoID: "aaaaaaaaaaa", Title: "A", ChannelTitle: "Chan", DurationSeconds: 90}},
		{7, 3, store.SuggestionVideo{YouTubeVideoID: "bbbbbbbbbbb"}},
	}
	if !slices.Equal(st.calls, want) {
		t.Errorf("calls = %+v, want %+v", st.calls, want)
	}
	if len(yt.calls) != 1 || !slices.Equal(yt.calls[0], []string{"aaaaaaaaaaa", "bbbbbbbbbbb"}) {
		t.Errorf("Videos calls = %v", yt.calls)
	}
}

func TestEnrichDuplicateIDs(t *testing.T) {
	st := &fakeStore{pending: []store.Suggestion{
		{ID: 1, Content: "https://youtu.be/aaaaaaaaaaa"},
		{ID: 2, Content: "<https://www.youtube.com/watch?v=aaaaaaaaaaa>"},
	}}
	yt := &fakeVideos{videos: []youtube.Video{{ID: "aaaaaaaaaaa", Title: "A"}}}
	if err := NewEnricher(st, yt, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(yt.calls) != 1 || !slices.Equal(yt.calls[0], []string{"aaaaaaaaaaa"}) {
		t.Errorf("Videos calls = %v", yt.calls)
	}
	if len(st.calls) != 2 || st.calls[0].v.Title != "A" || st.calls[1].v.Title != "A" {
		t.Errorf("calls = %+v", st.calls)
	}
}

func TestEnrichVideosError(t *testing.T) {
	st := &fakeStore{pending: []store.Suggestion{
		{ID: 1, Content: "no link"},
		{ID: 2, Content: "https://youtu.be/aaaaaaaaaaa"},
	}}
	boom := errors.New("quota")
	yt := &fakeVideos{err: boom}
	if err := NewEnricher(st, yt, nil).Enrich(context.Background(), 1); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
	if len(st.calls) != 0 {
		t.Errorf("marked despite error: %+v", st.calls)
	}
}

func TestEnrichNothingPending(t *testing.T) {
	st := &fakeStore{}
	yt := &fakeVideos{}
	if err := NewEnricher(st, yt, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(yt.calls) != 0 || len(yt.handleCalls) != 0 || len(st.calls) != 0 {
		t.Errorf("unexpected calls: %v %v", yt.calls, st.calls)
	}
}

func TestEnrichNoLinksSkipsVideos(t *testing.T) {
	st := &fakeStore{pending: []store.Suggestion{{ID: 1, Content: "hello"}}}
	yt := &fakeVideos{}
	if err := NewEnricher(st, yt, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(yt.calls) != 0 || len(st.calls) != 1 || st.calls[0].v != (store.SuggestionVideo{}) {
		t.Errorf("videos %v, sets %+v", yt.calls, st.calls)
	}
}

func TestEnrichSelfSuggestion(t *testing.T) {
	st := &fakeStore{pending: []store.Suggestion{
		{ID: 1, Content: "https://youtu.be/aaaaaaaaaaa", AuthorName: "Druid Grove", AuthorUsername: "someone"},
		{ID: 2, Content: "https://youtu.be/bbbbbbbbbbb", AuthorName: "Display", AuthorUsername: "creator_x"},
		{ID: 3, Content: "https://youtu.be/aaaaaaaaaaa", AuthorName: "Other", AuthorUsername: "other"},
	}}
	yt := &fakeVideos{
		videos: []youtube.Video{
			{ID: "aaaaaaaaaaa", ChannelID: "UC1", ChannelTitle: "DruidGrove"},
			{ID: "bbbbbbbbbbb", ChannelID: "UC2", ChannelTitle: "Totally Different"},
		},
		handles: map[string]string{"UC2": "@CreatorX"},
	}
	if err := NewEnricher(st, yt, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(yt.handleCalls) != 1 || !slices.Equal(yt.handleCalls[0], []string{"UC1", "UC2"}) {
		t.Errorf("ChannelHandles calls = %v", yt.handleCalls)
	}
	want := []bool{true, true, false}
	wantHandle := []string{"", "@CreatorX", ""}
	for i, c := range st.calls {
		if c.v.SelfSuggestion != want[i] || c.v.ChannelHandle != wantHandle[i] {
			t.Errorf("call %d = %+v; want self=%v handle=%q", i, c.v, want[i], wantHandle[i])
		}
	}
	if len(st.calls) != 3 {
		t.Errorf("calls = %d", len(st.calls))
	}
}

func TestEnrichNotSelfWhenNamesDiffer(t *testing.T) {
	st := &fakeStore{pending: []store.Suggestion{
		{ID: 1, Content: "https://youtu.be/aaaaaaaaaaa", AuthorName: "Druid Grove Fan", AuthorUsername: "fan123"},
	}}
	yt := &fakeVideos{
		videos:  []youtube.Video{{ID: "aaaaaaaaaaa", ChannelID: "UC1", ChannelTitle: "Druid Grove"}},
		handles: map[string]string{"UC1": "@druidgrove"},
	}
	if err := NewEnricher(st, yt, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) != 1 || st.calls[0].v.SelfSuggestion || st.calls[0].v.ChannelHandle != "@druidgrove" {
		t.Errorf("calls = %+v", st.calls)
	}
}

func TestEnrichHandlesErrorStillStores(t *testing.T) {
	st := &fakeStore{pending: []store.Suggestion{
		{ID: 1, Content: "https://youtu.be/aaaaaaaaaaa", AuthorName: "Chan"},
	}}
	yt := &fakeVideos{
		videos:     []youtube.Video{{ID: "aaaaaaaaaaa", ChannelID: "UC1", ChannelTitle: "Chan", Title: "A"}},
		handlesErr: errors.New("boom"),
	}
	if err := NewEnricher(st, yt, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) != 1 || st.calls[0].v.Title != "A" || st.calls[0].v.ChannelHandle != "" || !st.calls[0].v.SelfSuggestion {
		t.Errorf("calls = %+v", st.calls)
	}
}

func TestEnrichDiscardStep(t *testing.T) {
	for _, block := range []bool{true, false} {
		st := &fakeStore{block: block}
		if err := NewEnricher(st, &fakeVideos{}, nil).Enrich(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		want := 0
		if block {
			want = 1
		}
		if st.serverCalls != 1 || st.discardCalls != want {
			t.Errorf("block=%v: GetServer %d, Discard %d calls", block, st.serverCalls, st.discardCalls)
		}
	}
}

func TestEnrichDiscardAfterStoring(t *testing.T) {
	st := &fakeStore{block: true, pending: []store.Suggestion{{ID: 1, Content: "hello"}}}
	if err := NewEnricher(st, &fakeVideos{}, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(st.calls) != 1 || st.discardCalls != 1 {
		t.Errorf("sets %d, discards %d", len(st.calls), st.discardCalls)
	}
}

func TestEnrichGetServerError(t *testing.T) {
	boom := errors.New("db")
	st := &fakeStore{serverErr: boom, block: true}
	if err := NewEnricher(st, &fakeVideos{}, nil).Enrich(context.Background(), 1); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	if st.discardCalls != 0 {
		t.Errorf("discard ran despite GetServer error")
	}
}

func TestEnrichDiscardError(t *testing.T) {
	boom := errors.New("db")
	st := &fakeStore{block: true, discardErr: boom}
	if err := NewEnricher(st, &fakeVideos{}, nil).Enrich(context.Background(), 1); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
}
