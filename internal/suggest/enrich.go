// Package suggest enriches stored suggestions with YouTube video details.
package suggest

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/danotchuuy/video-queue/internal/store"
	"github.com/danotchuuy/video-queue/internal/youtube"
)

// batchSize is how many pending suggestions one Enrich call handles.
const batchSize = 200

// Store is the part of *store.Store the enricher needs.
type Store interface {
	PendingVideoLookups(ctx context.Context, serverID int64, limit int) ([]store.Suggestion, error)
	SetSuggestionVideo(ctx context.Context, serverID, id int64, v store.SuggestionVideo, at time.Time) error
	GetServer(ctx context.Context, id int64) (store.Server, error)
	DiscardSelfSuggestions(ctx context.Context, serverID int64, now time.Time) (int64, error)
}

// Videos is the part of *youtube.Client the enricher needs.
type Videos interface {
	Videos(ctx context.Context, ids []string) ([]youtube.Video, error)
	ChannelHandles(ctx context.Context, channelIDs []string) (map[string]string, error)
}

type Enricher struct {
	st  Store
	yt  Videos
	log *slog.Logger
}

// NewEnricher returns an Enricher; a nil log means slog.Default().
func NewEnricher(st Store, yt Videos, log *slog.Logger) *Enricher {
	if log == nil {
		log = slog.Default()
	}
	return &Enricher{st: st, yt: yt, log: log}
}

// Enrich looks up pending suggestions of one server (up to 200 per call).
// If the YouTube lookup fails nothing is marked, so the next call retries.
// Finally, if the server blocks self-suggestions, pending self-flagged ones
// are discarded (even when nothing was pending).
func (e *Enricher) Enrich(ctx context.Context, serverID int64) error {
	pending, err := e.st.PendingVideoLookups(ctx, serverID, batchSize)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		if err := e.lookup(ctx, serverID, pending); err != nil {
			return err
		}
	}
	srv, err := e.st.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	if srv.BlockSelfSuggestions {
		n, err := e.st.DiscardSelfSuggestions(ctx, serverID, time.Now())
		if err != nil {
			return err
		}
		if n > 0 {
			e.log.Info("discarded self-suggestions", "server_id", serverID, "count", n)
		}
	}
	return nil
}

// lookup fetches video details for pending suggestions and stores them.
func (e *Enricher) lookup(ctx context.Context, serverID int64, pending []store.Suggestion) error {
	ids := make([]string, len(pending))
	var unique []string
	seen := map[string]bool{}
	for i, p := range pending {
		id := FirstVideoID(p.Content)
		ids[i] = id
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	found := map[string]youtube.Video{}
	handles := map[string]string{}
	if len(unique) > 0 {
		vids, err := e.yt.Videos(ctx, unique)
		if err != nil {
			return err
		}
		for _, v := range vids {
			found[v.ID] = v
		}
		var channelIDs []string
		for _, v := range vids {
			if v.ChannelID != "" {
				channelIDs = append(channelIDs, v.ChannelID)
			}
		}
		if h, err := e.yt.ChannelHandles(ctx, channelIDs); err != nil {
			e.log.Warn("channel handle lookup failed", "server_id", serverID, "err", err)
		} else if h != nil {
			handles = h
		}
	}
	now := time.Now()
	for i, p := range pending {
		var sv store.SuggestionVideo
		if ids[i] != "" {
			sv.YouTubeVideoID = ids[i]
			if v, ok := found[ids[i]]; ok {
				sv.Title = v.Title
				sv.ChannelTitle = v.ChannelTitle
				sv.DurationSeconds = v.DurationSeconds
				sv.ChannelHandle = handles[v.ChannelID]
				sv.SelfSuggestion = IsSelfSuggestion(
					[]string{p.AuthorName, p.AuthorUsername},
					[]string{v.ChannelTitle, sv.ChannelHandle})
			}
		}
		if err := e.st.SetSuggestionVideo(ctx, serverID, p.ID, sv, now); err != nil {
			return err
		}
	}
	e.log.Debug("enriched suggestions", "server_id", serverID, "count", len(pending), "videos", len(found))
	return nil
}

// FirstVideoID returns the first YouTube video ID linked in Discord message
// text, or "".
func FirstVideoID(content string) string {
	for _, tok := range strings.Fields(content) {
		tok = cleanToken(tok)
		if !strings.Contains(tok, "://") {
			// People often paste links without the scheme. A bare 11-letter
			// word must not count as an ID, so require a YouTube-looking host.
			lower := strings.ToLower(tok)
			if !strings.HasPrefix(lower, "youtu") && !strings.HasPrefix(lower, "www.youtu") && !strings.HasPrefix(lower, "m.youtu") {
				continue
			}
			tok = "https://" + tok
		}
		if id, ok := youtube.ParseVideoID(tok); ok {
			return id
		}
	}
	return ""
}

// cleanToken strips Discord decorations around a link: <...> embed
// suppression, || spoilers, [text](url) markdown and trailing punctuation.
func cleanToken(tok string) string {
	const trailing = ").,!?;:>"
	tok = strings.Trim(tok, "|")
	if i := strings.Index(tok, "]("); i >= 0 {
		tok = tok[i+2:]
	}
	for {
		next := strings.TrimRight(strings.Trim(tok, "|"), trailing)
		next = strings.TrimLeft(next, "<(")
		if next == tok {
			return tok
		}
		tok = next
	}
}
