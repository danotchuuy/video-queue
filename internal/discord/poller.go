package discord

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danotchuuy/video-queue/internal/store"
	"github.com/danotchuuy/video-queue/internal/twitch"
)

// SuggestionStore is the subset of *store.Store the poller needs.
type SuggestionStore interface {
	ListServers(ctx context.Context) ([]store.Server, error)
	GetServer(ctx context.Context, id int64) (store.Server, error)
	UpsertSuggestion(ctx context.Context, in store.Suggestion) error
	SetSuggestionsCursor(ctx context.Context, serverID int64, cursor string) error
	UpsertClip(ctx context.Context, in store.Clip) error
	SetClipsCursor(ctx context.Context, serverID int64, cursor string) error
}

// Poller periodically mirrors each server's suggestions channel into suggestions
// and its clips channel into clips.
type Poller struct {
	client   *Client
	store    SuggestionStore
	interval time.Duration
	log      *slog.Logger

	mu    sync.Mutex            // guards locks
	locks map[int64]*sync.Mutex // per-server, serializes SyncServer

	// AfterSync, if set, runs at the end of every successful SyncServer
	// (e.g. to look up YouTube titles). Set it before Run.
	AfterSync func(ctx context.Context, serverID int64)
}

func NewPoller(c *Client, st SuggestionStore, interval time.Duration, log *slog.Logger) *Poller {
	if log == nil {
		log = slog.Default()
	}
	return &Poller{client: c, store: st, interval: interval, log: log, locks: map[int64]*sync.Mutex{}}
}

// Run syncs all servers immediately and then every interval until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	p.syncAll(ctx)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			p.syncAll(ctx)
		}
	}
}

func (p *Poller) syncAll(ctx context.Context) {
	servers, err := p.store.ListServers(ctx)
	if err != nil {
		if ctx.Err() == nil {
			p.log.Error("list servers for suggestion sync failed", "err", err)
		}
		return
	}
	for _, srv := range servers {
		if ctx.Err() != nil {
			return
		}
		if !hasChannel(srv) {
			continue
		}
		if err := p.SyncServer(ctx, srv.ID); err != nil && ctx.Err() == nil {
			p.log.Error("suggestion sync failed", "server_id", srv.ID, "server", srv.Name, "err", err)
		}
	}
}

func (p *Poller) serverLock(id int64) *sync.Mutex {
	p.mu.Lock()
	defer p.mu.Unlock()
	l, ok := p.locks[id]
	if !ok {
		l = &sync.Mutex{}
		p.locks[id] = l
	}
	return l
}

// channelSpec describes one synced channel of a server: where its ID and
// cursor live and how its messages are stored.
type channelSpec struct {
	name       string
	channelID  func(store.Server) string
	cursor     func(store.Server) string
	saveCursor func(ctx context.Context, serverID int64, cursor string) error
	// changed reports whether the server's channel differs from channelID.
	changed func(cur store.Server, channelID string) bool
	upsert  func(ctx context.Context, srv store.Server, channelID string, m Message) error
}

func (p *Poller) channelSpecs() []channelSpec {
	return []channelSpec{
		{
			name:       "suggestions",
			channelID:  func(s store.Server) string { return s.SuggestionsChannelID },
			cursor:     func(s store.Server) string { return s.SuggestionsCursor },
			saveCursor: p.store.SetSuggestionsCursor,
			changed:    func(cur store.Server, id string) bool { return cur.SuggestionsChannelID != id },
			upsert: func(ctx context.Context, srv store.Server, channelID string, m Message) error {
				s, ok := p.toSuggestion(srv.ID, channelID, m)
				if !ok {
					return nil
				}
				if err := p.store.UpsertSuggestion(ctx, s); err != nil {
					return fmt.Errorf("upsert suggestion %s: %w", m.ID, err)
				}
				return nil
			},
		},
		{
			name:       "clips",
			channelID:  func(s store.Server) string { return s.ClipsChannelID },
			cursor:     func(s store.Server) string { return s.ClipsCursor },
			saveCursor: p.store.SetClipsCursor,
			changed:    func(cur store.Server, id string) bool { return cur.ClipsChannelID != id },
			upsert: func(ctx context.Context, srv store.Server, channelID string, m Message) error {
				c, ok := p.toClip(srv, channelID, m)
				if !ok {
					return nil
				}
				if err := p.store.UpsertClip(ctx, c); err != nil {
					return fmt.Errorf("upsert clip %s: %w", m.ID, err)
				}
				return nil
			},
		},
	}
}

// hasChannel reports whether the server has any channel to sync.
func hasChannel(srv store.Server) bool {
	return srv.SuggestionsChannelID != "" || srv.ClipsChannelID != ""
}

// SyncServer syncs the server's suggestions channel and then its clips
// channel: it backfills messages newer than each stored cursor, then
// refreshes the latest 100 so reaction counts stay current. Both channels are
// attempted even if one fails; failures are joined. It is a no-op if the
// server has neither channel.
func (p *Poller) SyncServer(ctx context.Context, serverID int64) error {
	l := p.serverLock(serverID)
	l.Lock()
	defer l.Unlock()

	srv, err := p.store.GetServer(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	var errs []error
	synced := 0
	for _, spec := range p.channelSpecs() {
		if spec.channelID(srv) == "" {
			continue
		}
		if err := p.syncChannel(ctx, srv, spec); err != nil {
			errs = append(errs, fmt.Errorf("%s channel: %w", spec.name, err))
			continue
		}
		synced++
	}
	if synced > 0 && p.AfterSync != nil {
		p.AfterSync(ctx, srv.ID)
	}
	return errors.Join(errs...)
}

func (p *Poller) syncChannel(ctx context.Context, srv store.Server, spec channelSpec) error {
	channelID := spec.channelID(srv)
	cursor := spec.cursor(srv)
	if cursor == "" {
		cursor = "0"
	}

	for {
		msgs, err := p.client.ChannelMessages(ctx, channelID, MessageQuery{After: cursor, Limit: 100})
		if err != nil {
			return err
		}
		// Bail out before advancing the cursor, so these messages are
		// read again once the intent is enabled.
		if contentMissing(msgs) {
			return ErrMessageContentMissing
		}
		if err := p.upsertAll(ctx, srv, channelID, msgs, spec); err != nil {
			return err
		}
		next := cursor
		for _, m := range msgs {
			if snowflakeGreater(m.ID, next) {
				next = m.ID
			}
		}
		if next != cursor {
			// The channel may have been changed (which resets the cursor)
			// while we were fetching; don't clobber the new cursor.
			cur, err := p.store.GetServer(ctx, srv.ID)
			if err != nil {
				return fmt.Errorf("get server: %w", err)
			}
			if spec.changed(cur, channelID) {
				return nil
			}
			cursor = next
			if err := spec.saveCursor(ctx, srv.ID, cursor); err != nil {
				return fmt.Errorf("save cursor: %w", err)
			}
		}
		if len(msgs) < 100 {
			break
		}
	}

	// Refresh reaction counts on recent messages.
	recent, err := p.client.ChannelMessages(ctx, channelID, MessageQuery{Limit: 100})
	if err != nil {
		return err
	}
	return p.upsertAll(ctx, srv, channelID, recent, spec)
}

// ErrMessageContentMissing means Discord sent user messages without their
// content, which happens when the bot lacks the Message Content intent.
var ErrMessageContentMissing = errors.New("messages arrive without content: enable the Message Content Intent for the bot in the Discord Developer Portal (Bot > Privileged Gateway Intents)")

// contentMissing reports whether msgs has user messages and every one of them
// lacks content, embeds and attachments, as Discord delivers them without the
// Message Content intent.
func contentMissing(msgs []Message) bool {
	users := 0
	for _, m := range msgs {
		if m.Author.Bot || (m.Type != 0 && m.Type != 19) {
			continue
		}
		if m.Content != "" || len(m.Embeds) > 0 || len(m.Attachments) > 0 {
			return false
		}
		users++
	}
	return users > 0
}

func (p *Poller) upsertAll(ctx context.Context, srv store.Server, channelID string, msgs []Message, spec channelSpec) error {
	for _, m := range msgs {
		if err := spec.upsert(ctx, srv, channelID, m); err != nil {
			return err
		}
	}
	return nil
}

// toSuggestion converts a message; ok is false if it should be skipped.
func (p *Poller) toSuggestion(serverID int64, channelID string, m Message) (store.Suggestion, bool) {
	if m.Author.Bot || (m.Type != 0 && m.Type != 19) {
		return store.Suggestion{}, false
	}
	content := m.Content
	if content == "" {
		switch {
		case len(m.Embeds) > 0 && m.Embeds[0].URL != "":
			content = m.Embeds[0].URL
		case len(m.Attachments) > 0:
			content = m.Attachments[0].URL
		}
	}
	if content == "" {
		return store.Suggestion{}, false
	}
	name := m.Author.GlobalName
	if name == "" {
		name = m.Author.Username
	}
	var reactions int64
	for _, r := range m.Reactions {
		reactions += int64(r.Count)
	}
	posted, err := time.Parse(time.RFC3339, m.Timestamp)
	if err != nil {
		p.log.Warn("bad message timestamp", "id", m.ID, "timestamp", m.Timestamp)
	}
	return store.Suggestion{
		DiscordMessageID: m.ID,
		ServerID:         serverID,
		ChannelID:        channelID,
		AuthorID:         m.Author.ID,
		AuthorName:       name,
		AuthorUsername:   m.Author.Username,
		Content:          content,
		ReactionCount:    reactions,
		PostedAt:         posted,
	}, true
}

// maxClipTitleRunes caps a clip's title.
const maxClipTitleRunes = 300

// toClip converts a clips-channel message; ok is false if it should be
// skipped (filtered author/type, or no link to a clip).
func (p *Poller) toClip(srv store.Server, channelID string, m Message) (store.Clip, bool) {
	if m.Author.Bot || (m.Type != 0 && m.Type != 19) {
		return store.Clip{}, false
	}
	clipURL, slug := twitch.FirstClip(m.Content)
	if clipURL == "" {
		clipURL = firstHTTPURL(m.Content)
	}
	if clipURL == "" && len(m.Attachments) > 0 {
		clipURL = m.Attachments[0].URL
	}
	if clipURL == "" {
		return store.Clip{}, false
	}
	var words []string
	for _, tok := range strings.Fields(m.Content) {
		if strings.Contains(tok, "://") {
			continue
		}
		if u, _ := twitch.FirstClip(tok); u != "" {
			continue
		}
		words = append(words, tok)
	}
	title := strings.Join(words, " ")
	if r := []rune(title); len(r) > maxClipTitleRunes {
		title = strings.TrimSpace(string(r[:maxClipTitleRunes]))
	}
	name := m.Author.GlobalName
	if name == "" {
		name = m.Author.Username
	}
	posted, err := time.Parse(time.RFC3339, m.Timestamp)
	if err != nil {
		p.log.Warn("bad message timestamp", "id", m.ID, "timestamp", m.Timestamp)
	}
	return store.Clip{
		DiscordMessageID: m.ID,
		ServerID:         srv.ID,
		ChannelID:        channelID,
		MessageURL:       "https://discord.com/channels/" + srv.GuildID + "/" + channelID + "/" + m.ID,
		AuthorID:         m.Author.ID,
		AuthorName:       name,
		AuthorUsername:   m.Author.Username,
		Content:          m.Content,
		PostedAt:         posted,
		ClipURL:          clipURL,
		TwitchClipID:     slug,
		Title:            title,
	}, true
}

// firstHTTPURL returns the first http(s) URL token in content, stripped of
// Discord/markdown decoration and trailing punctuation, or "".
func firstHTTPURL(content string) string {
	for _, tok := range strings.Fields(content) {
		if i := strings.Index(tok, "]("); i >= 0 {
			tok = tok[i+2:]
		}
		tok = strings.Trim(tok, "|<>(")
		tok = strings.TrimRight(tok, ").,!?;:>|")
		lower := strings.ToLower(tok)
		if (strings.HasPrefix(lower, "http://") && len(tok) > 7) || (strings.HasPrefix(lower, "https://") && len(tok) > 8) {
			return tok
		}
	}
	return ""
}

// snowflakeGreater reports whether a > b numerically; unparsable IDs never win.
func snowflakeGreater(a, b string) bool {
	x, err := strconv.ParseUint(a, 10, 64)
	if err != nil {
		return false
	}
	y, err := strconv.ParseUint(b, 10, 64)
	if err != nil {
		return true
	}
	return x > y
}
