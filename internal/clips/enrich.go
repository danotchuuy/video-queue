// Package clips enriches stored Twitch clips with details from the Helix API.
package clips

import (
	"context"
	"log/slog"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/danotchuuy/video-queue/internal/store"
	"github.com/danotchuuy/video-queue/internal/twitch"
)

// batchSize is how many pending clips one Enrich call handles.
const batchSize = 200

// Store is the part of *store.Store the enricher needs.
type Store interface {
	PendingClipLookups(ctx context.Context, serverID int64, limit int) ([]store.Clip, error)
	SetClipTwitch(ctx context.Context, serverID, id int64, t store.ClipTwitch, at time.Time) error
}

// Twitch is the part of *twitch.Client the enricher needs.
type Twitch interface {
	Clips(ctx context.Context, slugs []string) ([]twitch.Clip, error)
}

type Enricher struct {
	st  Store
	tw  Twitch
	log *slog.Logger
}

// NewEnricher returns an Enricher; a nil log means slog.Default().
func NewEnricher(st Store, tw Twitch, log *slog.Logger) *Enricher {
	if log == nil {
		log = slog.Default()
	}
	return &Enricher{st: st, tw: tw, log: log}
}

// Enrich looks up pending clips of one server (up to 200 per call) with a
// single Twitch call. Clips Twitch doesn't know are marked as looked up with
// empty details. If the Twitch call fails nothing is marked, so the next call
// retries.
func (e *Enricher) Enrich(ctx context.Context, serverID int64) error {
	pending, err := e.st.PendingClipLookups(ctx, serverID, batchSize)
	if err != nil || len(pending) == 0 {
		return err
	}
	var slugs []string
	seen := map[string]bool{}
	for _, p := range pending {
		if !seen[p.TwitchClipID] {
			seen[p.TwitchClipID] = true
			slugs = append(slugs, p.TwitchClipID)
		}
	}
	res, err := e.tw.Clips(ctx, slugs)
	if err != nil {
		return err
	}
	found := make(map[string]twitch.Clip, len(res))
	for _, c := range res {
		found[c.ID] = c
	}
	now := time.Now()
	for _, p := range pending {
		var t store.ClipTwitch
		if c, ok := found[p.TwitchClipID]; ok {
			t = store.ClipTwitch{
				Title:           c.Title,
				ClipperName:     c.CreatorName,
				BroadcasterName: c.BroadcasterName,
				ThumbnailURL:    safeThumbnail(c.ThumbnailURL),
				DurationSeconds: int64(math.Round(c.Duration)),
			}
			if !c.CreatedAt.IsZero() {
				created := c.CreatedAt
				t.CreatedAt = &created
			}
		}
		if err := e.st.SetClipTwitch(ctx, serverID, p.ID, t, now); err != nil {
			return err
		}
	}
	e.log.Debug("enriched clips", "server_id", serverID, "count", len(pending), "found", len(found))
	return nil
}

// safeThumbnail returns raw if it is an https URL on Twitch's CDN
// (*.jtvnw.net), else "".
func safeThumbnail(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || !strings.HasSuffix(u.Hostname(), ".jtvnw.net") {
		return ""
	}
	return raw
}
