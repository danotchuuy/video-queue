package clips

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/danotchuuy/video-queue/internal/store"
	"github.com/danotchuuy/video-queue/internal/twitch"
)

type fakeStore struct {
	pending []store.Clip
	set     map[int64]store.ClipTwitch
}

func (f *fakeStore) PendingClipLookups(ctx context.Context, serverID int64, limit int) ([]store.Clip, error) {
	return f.pending, nil
}

func (f *fakeStore) SetClipTwitch(ctx context.Context, serverID, id int64, t store.ClipTwitch, at time.Time) error {
	if f.set == nil {
		f.set = map[int64]store.ClipTwitch{}
	}
	f.set[id] = t
	return nil
}

type fakeTwitch struct {
	clips []twitch.Clip
	err   error
	calls [][]string
}

func (f *fakeTwitch) Clips(ctx context.Context, slugs []string) ([]twitch.Clip, error) {
	f.calls = append(f.calls, slices.Clone(slugs))
	return f.clips, f.err
}

func pending(slugs ...string) []store.Clip {
	var out []store.Clip
	for i, s := range slugs {
		out = append(out, store.Clip{ID: int64(i + 1), TwitchClipID: s})
	}
	return out
}

func TestEnrichFoundNotFoundMixed(t *testing.T) {
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	st := &fakeStore{pending: pending("a", "gone", "b")}
	tw := &fakeTwitch{clips: []twitch.Clip{
		{ID: "a", Title: "Nice", CreatorName: "clipper", BroadcasterName: "vtuber",
			ThumbnailURL: "https://static-cdn.jtvnw.net/x.jpg", Duration: 29.6, CreatedAt: created},
		{ID: "b", Title: "Other", Duration: 12.4},
	}}
	if err := NewEnricher(st, tw, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(tw.calls) != 1 {
		t.Fatalf("Clips calls = %d, want 1", len(tw.calls))
	}
	a := st.set[1]
	if a.Title != "Nice" || a.ClipperName != "clipper" || a.BroadcasterName != "vtuber" ||
		a.ThumbnailURL != "https://static-cdn.jtvnw.net/x.jpg" || a.DurationSeconds != 30 ||
		a.CreatedAt == nil || !a.CreatedAt.Equal(created) {
		t.Errorf("clip a = %+v", a)
	}
	if got, ok := st.set[2]; !ok || got != (store.ClipTwitch{}) {
		t.Errorf("not-found clip = %+v, marked %v; want zero value marked", got, ok)
	}
	b := st.set[3]
	if b.DurationSeconds != 12 || b.CreatedAt != nil {
		t.Errorf("clip b = %+v", b)
	}
}

func TestEnrichThumbnailCheck(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://static-cdn.jtvnw.net/twitch-clips/x.jpg", "https://static-cdn.jtvnw.net/twitch-clips/x.jpg"},
		{"http://static-cdn.jtvnw.net/x.jpg", ""},
		{"https://clips-media-assets2.twitch.tv/x.jpg", ""},
		{"https://jtvnw.net.evil.example/x.jpg", ""},
		{"https://evil.example/.jtvnw.net/x.jpg", ""},
		{"https://notjtvnw.net/x.jpg", ""},
		{"https://evil.example@jtvnw.net.evil.example/x.jpg", ""},
		{"//static-cdn.jtvnw.net/x.jpg", ""},
		{"", ""},
		{"://bad", ""},
	}
	for _, tc := range tests {
		if got := safeThumbnail(tc.in); got != tc.want {
			t.Errorf("safeThumbnail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEnrichClipsErrorMarksNothing(t *testing.T) {
	st := &fakeStore{pending: pending("a")}
	boom := errors.New("boom")
	err := NewEnricher(st, &fakeTwitch{err: boom}, nil).Enrich(context.Background(), 1)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if len(st.set) != 0 {
		t.Errorf("marked %v, want nothing", st.set)
	}
}

func TestEnrichNothingPendingMakesNoCall(t *testing.T) {
	tw := &fakeTwitch{}
	if err := NewEnricher(&fakeStore{}, tw, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(tw.calls) != 0 {
		t.Errorf("Clips called %d times", len(tw.calls))
	}
}

func TestEnrichDeduplicatesSlugs(t *testing.T) {
	st := &fakeStore{pending: pending("a", "b", "a")}
	tw := &fakeTwitch{clips: []twitch.Clip{{ID: "a", Title: "A"}, {ID: "b", Title: "B"}}}
	if err := NewEnricher(st, tw, nil).Enrich(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(tw.calls) != 1 || !slices.Equal(tw.calls[0], []string{"a", "b"}) {
		t.Errorf("calls = %v, want one call with [a b]", tw.calls)
	}
	if st.set[1].Title != "A" || st.set[3].Title != "A" || st.set[2].Title != "B" {
		t.Errorf("set = %+v", st.set)
	}
}
