package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// SetClipsCursor stores the last Discord message ID read from the server's clips channel.
func (s *Store) SetClipsCursor(ctx context.Context, serverID int64, cursor string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET clips_cursor = ? WHERE id = ?`, cursor, serverID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

const clipCols = `id, server_id, discord_message_id, channel_id, message_url, author_id, author_name, author_username,
content, posted_at, clip_url, twitch_clip_id, title, clipper_name, broadcaster_name, thumbnail_url, duration_seconds,
clip_created_at, twitch_lookup_at, status, status_at, reviewed_by, reviewed_at, edited_title, edited_url,
youtube_url, instagram_url, x_url, tiktok_url, notes, first_posted_at, created_at, updated_at`

func scanClip(r scanner) (Clip, error) {
	var x Clip
	var posted, statusAt, created, updated string
	var clipCreated, lookup, reviewed, firstPosted sql.NullString
	if err := r.Scan(&x.ID, &x.ServerID, &x.DiscordMessageID, &x.ChannelID, &x.MessageURL, &x.AuthorID,
		&x.AuthorName, &x.AuthorUsername, &x.Content, &posted, &x.ClipURL, &x.TwitchClipID, &x.Title,
		&x.ClipperName, &x.BroadcasterName, &x.ThumbnailURL, &x.DurationSeconds, &clipCreated, &lookup,
		&x.Status, &statusAt, &x.ReviewedBy, &reviewed, &x.EditedTitle, &x.EditedURL,
		&x.YouTubeURL, &x.InstagramURL, &x.XURL, &x.TikTokURL, &x.Notes, &firstPosted, &created, &updated); err != nil {
		return Clip{}, err
	}
	var err error
	if x.PostedAt, err = parseTime(posted); err != nil {
		return Clip{}, err
	}
	if x.StatusAt, err = parseTime(statusAt); err != nil {
		return Clip{}, err
	}
	if x.CreatedAt, err = parseTime(created); err != nil {
		return Clip{}, err
	}
	if x.UpdatedAt, err = parseTime(updated); err != nil {
		return Clip{}, err
	}
	if x.ClipCreatedAt, err = parseTimePtr(clipCreated); err != nil {
		return Clip{}, err
	}
	if x.TwitchLookupAt, err = parseTimePtr(lookup); err != nil {
		return Clip{}, err
	}
	if x.ReviewedAt, err = parseTimePtr(reviewed); err != nil {
		return Clip{}, err
	}
	if x.FirstPostedAt, err = parseTimePtr(firstPosted); err != nil {
		return Clip{}, err
	}
	return x, nil
}

func scanClips(rows *sql.Rows) ([]Clip, error) {
	defer rows.Close()
	out := []Clip{}
	for rows.Next() {
		x, err := scanClip(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// UpsertClip inserts a clip keyed by DiscordMessageID into in.ServerID with
// status new. If it already exists in that server, only author_name,
// author_username and content are updated; if the content changed while the
// clip is still new, clip_url, twitch_clip_id and title are taken from in and
// the Twitch lookup is reset. A message ID already used by another server is
// left untouched.
func (s *Store) UpsertClip(ctx context.Context, in Clip) error {
	if in.ServerID == 0 {
		return errNoServer
	}
	now := fmtTime(time.Now())
	const changed = `(clips.content <> excluded.content AND clips.status = 'new')`
	_, err := s.db.ExecContext(ctx, `
INSERT INTO clips (server_id, discord_message_id, channel_id, message_url, author_id, author_name, author_username,
    content, posted_at, clip_url, twitch_clip_id, title, status, status_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'new', ?, ?, ?)
ON CONFLICT(discord_message_id) DO UPDATE SET
    clip_url = CASE WHEN `+changed+` THEN excluded.clip_url ELSE clips.clip_url END,
    twitch_clip_id = CASE WHEN `+changed+` THEN excluded.twitch_clip_id ELSE clips.twitch_clip_id END,
    title = CASE WHEN `+changed+` THEN excluded.title ELSE clips.title END,
    clipper_name = CASE WHEN `+changed+` THEN '' ELSE clips.clipper_name END,
    broadcaster_name = CASE WHEN `+changed+` THEN '' ELSE clips.broadcaster_name END,
    thumbnail_url = CASE WHEN `+changed+` THEN '' ELSE clips.thumbnail_url END,
    duration_seconds = CASE WHEN `+changed+` THEN 0 ELSE clips.duration_seconds END,
    clip_created_at = CASE WHEN `+changed+` THEN NULL ELSE clips.clip_created_at END,
    twitch_lookup_at = CASE WHEN `+changed+` THEN NULL ELSE clips.twitch_lookup_at END,
    content = excluded.content,
    author_name = excluded.author_name,
    author_username = excluded.author_username,
    updated_at = excluded.updated_at
WHERE clips.server_id = excluded.server_id`,
		in.ServerID, in.DiscordMessageID, in.ChannelID, in.MessageURL, in.AuthorID, in.AuthorName, in.AuthorUsername,
		in.Content, fmtTime(in.PostedAt), in.ClipURL, in.TwitchClipID, in.Title, now, now, now)
	return err
}

var platformCols = map[Platform]string{
	PlatformYouTube:   "youtube_url",
	PlatformInstagram: "instagram_url",
	PlatformX:         "x_url",
	PlatformTikTok:    "tiktok_url",
}

// ListClips returns the server's clips matching the filter; never nil.
func (s *Store) ListClips(ctx context.Context, f ClipFilter) ([]Clip, error) {
	q := `SELECT ` + clipCols + ` FROM clips WHERE server_id = ?`
	args := []any{f.ServerID}
	if len(f.Statuses) > 0 {
		q += " AND status IN (?" + strings.Repeat(", ?", len(f.Statuses)-1) + ")"
		for _, st := range f.Statuses {
			args = append(args, string(st))
		}
	}
	var missing []string
	for _, p := range f.Missing {
		if col, ok := platformCols[p]; ok {
			missing = append(missing, col+" = ''")
		}
	}
	if len(missing) > 0 {
		q += " AND (" + strings.Join(missing, " OR ") + ")"
	}
	switch f.Sort {
	case ClipSortOldest:
		q += " ORDER BY posted_at ASC, id ASC"
	case ClipSortStatusAt:
		q += " ORDER BY status_at ASC, id ASC"
	default:
		q += " ORDER BY posted_at DESC, id DESC"
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return scanClips(rows)
}

func (s *Store) GetClip(ctx context.Context, serverID, id int64) (Clip, error) {
	return getClip(ctx, s.db, serverID, id)
}

func getClip(ctx context.Context, q querier, serverID, id int64) (Clip, error) {
	x, err := scanClip(q.QueryRowContext(ctx,
		`SELECT `+clipCols+` FROM clips WHERE id = ? AND server_id = ?`, id, serverID))
	return x, notFound(err)
}

// UpdateClip applies the patch. A status change sets status_at; the first move
// out of the inbox (new/discarded) to pending, edited or posted records the
// reviewer; the first entry into posted sets first_posted_at.
func (s *Store) UpdateClip(ctx context.Context, serverID, id int64, pt ClipPatch, reviewerID string, now time.Time) (Clip, error) {
	var out Clip
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := getClip(ctx, tx, serverID, id)
		if err != nil {
			return err
		}
		var p patch
		if pt.Status != nil && *pt.Status != cur.Status {
			st := *pt.Status
			p.set("status", string(st))
			p.set("status_at", fmtTime(now))
			if (cur.Status == ClipNew || cur.Status == ClipDiscarded) &&
				(st == ClipPending || st == ClipEdited || st == ClipPosted) && cur.ReviewedAt == nil {
				p.set("reviewed_by", reviewerID)
				p.set("reviewed_at", fmtTime(now))
			}
			if st == ClipPosted && cur.FirstPostedAt == nil {
				p.set("first_posted_at", fmtTime(now))
			}
		}
		setPtr(&p, "edited_title", pt.EditedTitle)
		setPtr(&p, "edited_url", pt.EditedURL)
		setPtr(&p, "youtube_url", pt.YouTubeURL)
		setPtr(&p, "instagram_url", pt.InstagramURL)
		setPtr(&p, "x_url", pt.XURL)
		setPtr(&p, "tiktok_url", pt.TikTokURL)
		setPtr(&p, "notes", pt.Notes)
		if err := p.exec(ctx, tx, "clips", "id = ? AND server_id = ?", id, serverID); err != nil {
			return err
		}
		out, err = getClip(ctx, tx, serverID, id)
		return err
	})
	if err != nil {
		return Clip{}, err
	}
	return out, nil
}

// PendingClipLookups returns up to limit Twitch clips of the server that have
// not been looked up yet, oldest posted first.
func (s *Store) PendingClipLookups(ctx context.Context, serverID int64, limit int) ([]Clip, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+clipCols+
		` FROM clips WHERE server_id = ? AND twitch_clip_id <> '' AND twitch_lookup_at IS NULL ORDER BY posted_at ASC, id ASC LIMIT ?`,
		serverID, limit)
	if err != nil {
		return nil, err
	}
	return scanClips(rows)
}

// SetClipTwitch stores the Twitch lookup result t and marks the clip as looked
// up at the given time. The title is replaced only when t.Title is non-empty.
// It does not touch updated_at.
func (s *Store) SetClipTwitch(ctx context.Context, serverID, id int64, t ClipTwitch, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE clips SET title = CASE WHEN ? <> '' THEN ? ELSE title END, clipper_name = ?, broadcaster_name = ?,
    thumbnail_url = ?, duration_seconds = ?, clip_created_at = ?, twitch_lookup_at = ?
WHERE id = ? AND server_id = ?`,
		t.Title, t.Title, t.ClipperName, t.BroadcasterName, t.ThumbnailURL, t.DurationSeconds,
		fmtTimePtr(t.CreatedAt), fmtTime(at), id, serverID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}
