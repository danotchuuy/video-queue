// Package store persists video-queue data in SQLite.
package store

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the SQLite-backed data store. It is safe for concurrent use.
type Store struct {
	db *sql.DB
}

// timeLayout is RFC 3339 with fixed-width nanoseconds, so stored timestamps
// sort lexicographically and parse with time.RFC3339Nano.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func fmtTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return fmtTime(*t)
}

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: parse time %q: %w", s, err)
	}
	return t.UTC(), nil
}

func parseTimePtr(ns sql.NullString) (*time.Time, error) {
	if !ns.Valid {
		return nil, nil
	}
	t, err := parseTime(ns.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// querier is implemented by *sql.DB and *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type scanner interface{ Scan(dest ...any) error }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// inTx runs fn in a transaction, committing if fn returns nil.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Open opens (creating if needed) the SQLite database at path, enables foreign
// keys and WAL, and applies any pending migrations. path may be ":memory:".
func Open(ctx context.Context, path string) (*Store, error) {
	memory := path == ":memory:"
	dsn := path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	if !memory {
		dsn += "&_pragma=journal_mode(WAL)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if memory {
		db.SetMaxOpenConns(1)
	}
	s := &Store{db: db}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error { return s.migrateUpTo(ctx, 0) }

// migrateUpTo applies pending migrations with version <= maxVersion (0 means all).
func (s *Store) migrateUpTo(ctx context.Context, maxVersion int) error {
	if _, err := s.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}
	entries, err := fs.ReadDir(migrations, "migrations")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || path.Ext(name) != ".sql" {
			continue
		}
		prefix, _, _ := strings.Cut(name, "_")
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return fmt.Errorf("store: migration %s: bad version prefix", name)
		}
		if maxVersion > 0 && version > maxVersion {
			continue
		}
		var applied bool
		if err := s.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = ?)`, version).Scan(&applied); err != nil {
			return err
		}
		if applied {
			continue
		}
		script, err := fs.ReadFile(migrations, "migrations/"+name)
		if err != nil {
			return err
		}
		err = s.inTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, string(script)); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, fmtTime(time.Now()))
			return err
		})
		if err != nil {
			return fmt.Errorf("store: apply migration %s: %w", name, err)
		}
	}
	return nil
}

// --- users & sessions ---

// UpsertUser inserts the user or updates username/global_name/avatar/last_login_at.
func (s *Store) UpsertUser(ctx context.Context, u User) error {
	now := fmtTime(time.Now())
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users (discord_id, username, global_name, avatar, created_at, last_login_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(discord_id) DO UPDATE SET
    username = excluded.username,
    global_name = excluded.global_name,
    avatar = excluded.avatar,
    last_login_at = excluded.last_login_at`,
		u.DiscordID, u.Username, u.GlobalName, u.Avatar, now, now)
	return err
}

// ListUsers returns all users ordered by display name (global name, else
// username; case-insensitive), then Discord ID. It is never nil.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT discord_id, username, global_name, avatar, created_at, last_login_at FROM users
ORDER BY LOWER(CASE WHEN global_name <> '' THEN global_name ELSE username END), discord_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		var u User
		var created, login string
		if err := rows.Scan(&u.DiscordID, &u.Username, &u.GlobalName, &u.Avatar, &created, &login); err != nil {
			return nil, err
		}
		if u.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		if u.LastLoginAt, err = parseTime(login); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser returns the user, or ErrNotFound.
func (s *Store) GetUser(ctx context.Context, discordID string) (User, error) {
	var u User
	var created, login string
	err := s.db.QueryRowContext(ctx, `
SELECT discord_id, username, global_name, avatar, created_at, last_login_at FROM users WHERE discord_id = ?`, discordID).
		Scan(&u.DiscordID, &u.Username, &u.GlobalName, &u.Avatar, &created, &login)
	if err != nil {
		return User{}, notFound(err)
	}
	if u.CreatedAt, err = parseTime(created); err != nil {
		return User{}, err
	}
	if u.LastLoginAt, err = parseTime(login); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *Store) CreateSession(ctx context.Context, tokenHash, userID string, expiresAt time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
		tokenHash, userID, fmtTime(time.Now()), fmtTime(expiresAt))
	return err
}

// UserBySession returns the session's user if the session exists and expires after now.
func (s *Store) UserBySession(ctx context.Context, tokenHash string, now time.Time) (User, error) {
	var u User
	var created, login string
	err := s.db.QueryRowContext(ctx, `
SELECT u.discord_id, u.username, u.global_name, u.avatar, u.created_at, u.last_login_at
FROM sessions s JOIN users u ON u.discord_id = s.user_id
WHERE s.token_hash = ? AND s.expires_at > ?`, tokenHash, fmtTime(now)).
		Scan(&u.DiscordID, &u.Username, &u.GlobalName, &u.Avatar, &created, &login)
	if err != nil {
		return User{}, notFound(err)
	}
	if u.CreatedAt, err = parseTime(created); err != nil {
		return User{}, err
	}
	if u.LastLoginAt, err = parseTime(login); err != nil {
		return User{}, err
	}
	return u, nil
}

func (s *Store) DeleteSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, fmtTime(now))
	return err
}

// --- servers ---

const serverCols = `id, guild_id, name, guild_name, icon, mod_role_ids, allowed_user_ids,
clip_editor_role_ids, video_editor_role_ids,
suggestions_channel_id, suggestions_cursor, block_self_suggestions, clips_channel_id, clips_cursor,
created_by, created_at, updated_at`

func marshalIDs(ids []string) (string, error) {
	if ids == nil {
		ids = []string{}
	}
	b, err := json.Marshal(ids)
	return string(b), err
}

func unmarshalIDs(s string) ([]string, error) {
	ids := []string{}
	if err := json.Unmarshal([]byte(s), &ids); err != nil {
		return nil, fmt.Errorf("store: parse id list %q: %w", s, err)
	}
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}

func scanServer(r scanner) (Server, error) {
	var x Server
	var roles, users, clipRoles, videoRoles, created, updated string
	if err := r.Scan(&x.ID, &x.GuildID, &x.Name, &x.GuildName, &x.Icon, &roles, &users, &clipRoles, &videoRoles,
		&x.SuggestionsChannelID, &x.SuggestionsCursor, &x.BlockSelfSuggestions,
		&x.ClipsChannelID, &x.ClipsCursor, &x.CreatedBy, &created, &updated); err != nil {
		return Server{}, err
	}
	var err error
	if x.ModRoleIDs, err = unmarshalIDs(roles); err != nil {
		return Server{}, err
	}
	if x.AllowedUserIDs, err = unmarshalIDs(users); err != nil {
		return Server{}, err
	}
	if x.ClipEditorRoleIDs, err = unmarshalIDs(clipRoles); err != nil {
		return Server{}, err
	}
	if x.VideoEditorRoleIDs, err = unmarshalIDs(videoRoles); err != nil {
		return Server{}, err
	}
	if x.CreatedAt, err = parseTime(created); err != nil {
		return Server{}, err
	}
	if x.UpdatedAt, err = parseTime(updated); err != nil {
		return Server{}, err
	}
	return x, nil
}

// ListServers returns all servers ordered by name (case-insensitive), then id.
func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverCols+` FROM servers ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Server{}
	for rows.Next() {
		x, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GetServer(ctx context.Context, id int64) (Server, error) {
	return getServer(ctx, s.db, id)
}

func getServer(ctx context.Context, q querier, id int64) (Server, error) {
	x, err := scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE id = ?`, id))
	return x, notFound(err)
}

func (s *Store) GetServerByGuild(ctx context.Context, guildID string) (Server, error) {
	return getServerByGuild(ctx, s.db, guildID)
}

func getServerByGuild(ctx context.Context, q querier, guildID string) (Server, error) {
	x, err := scanServer(q.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers WHERE guild_id = ?`, guildID))
	return x, notFound(err)
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// CreateServer registers a server. It returns ErrConflict if the guild is already registered.
func (s *Store) CreateServer(ctx context.Context, in Server) (Server, error) {
	return createServer(ctx, s.db, in)
}

func createServer(ctx context.Context, q querier, in Server) (Server, error) {
	roles, err := marshalIDs(in.ModRoleIDs)
	if err != nil {
		return Server{}, err
	}
	users, err := marshalIDs(in.AllowedUserIDs)
	if err != nil {
		return Server{}, err
	}
	clipRoles, err := marshalIDs(in.ClipEditorRoleIDs)
	if err != nil {
		return Server{}, err
	}
	videoRoles, err := marshalIDs(in.VideoEditorRoleIDs)
	if err != nil {
		return Server{}, err
	}
	now := fmtTime(time.Now())
	res, err := q.ExecContext(ctx, `
INSERT INTO servers (guild_id, name, guild_name, icon, mod_role_ids, allowed_user_ids,
    clip_editor_role_ids, video_editor_role_ids,
    suggestions_channel_id, suggestions_cursor, block_self_suggestions, clips_channel_id, clips_cursor,
    created_by, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.GuildID, in.Name, in.GuildName, in.Icon, roles, users, clipRoles, videoRoles,
		in.SuggestionsChannelID, in.SuggestionsCursor, in.BlockSelfSuggestions, in.ClipsChannelID, in.ClipsCursor,
		in.CreatedBy, now, now)
	if err != nil {
		if isUnique(err) {
			return Server{}, ErrConflict
		}
		return Server{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Server{}, err
	}
	return getServer(ctx, q, id)
}

// UpdateServer applies the patch. Changing the suggestions or clips channel resets
// its cursor; changing mod roles, editor roles or allowed users clears the server's access cache.
func (s *Store) UpdateServer(ctx context.Context, id int64, pt ServerPatch) (Server, error) {
	var out Server
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := getServer(ctx, tx, id)
		if err != nil {
			return err
		}
		var p patch
		setPtr(&p, "name", pt.Name)
		if pt.SuggestionsChannelID != nil {
			p.set("suggestions_channel_id", *pt.SuggestionsChannelID)
			if *pt.SuggestionsChannelID != cur.SuggestionsChannelID {
				p.set("suggestions_cursor", "")
			}
		}
		setPtr(&p, "block_self_suggestions", pt.BlockSelfSuggestions)
		if pt.ClipsChannelID != nil {
			p.set("clips_channel_id", *pt.ClipsChannelID)
			if *pt.ClipsChannelID != cur.ClipsChannelID {
				p.set("clips_cursor", "")
			}
		}
		if pt.ModRoleIDs != nil {
			b, err := marshalIDs(*pt.ModRoleIDs)
			if err != nil {
				return err
			}
			p.set("mod_role_ids", b)
		}
		if pt.AllowedUserIDs != nil {
			b, err := marshalIDs(*pt.AllowedUserIDs)
			if err != nil {
				return err
			}
			p.set("allowed_user_ids", b)
		}
		if pt.ClipEditorRoleIDs != nil {
			b, err := marshalIDs(*pt.ClipEditorRoleIDs)
			if err != nil {
				return err
			}
			p.set("clip_editor_role_ids", b)
		}
		if pt.VideoEditorRoleIDs != nil {
			b, err := marshalIDs(*pt.VideoEditorRoleIDs)
			if err != nil {
				return err
			}
			p.set("video_editor_role_ids", b)
		}
		if len(p.sets) == 0 {
			out = cur
			return nil
		}
		if err := p.exec(ctx, tx, "servers", "id = ?", id); err != nil {
			return err
		}
		if pt.ModRoleIDs != nil || pt.AllowedUserIDs != nil || pt.ClipEditorRoleIDs != nil || pt.VideoEditorRoleIDs != nil {
			if _, err := tx.ExecContext(ctx, `DELETE FROM server_access WHERE server_id = ?`, id); err != nil {
				return err
			}
		}
		out, err = getServer(ctx, tx, id)
		return err
	})
	if err != nil {
		return Server{}, err
	}
	return out, nil
}

// DeleteServer deletes the server and, by cascade, all its content.
func (s *Store) DeleteServer(ctx context.Context, id int64) error {
	return deleteScoped(ctx, s.db, "servers", "id = ?", id)
}

func (s *Store) SetSuggestionsCursor(ctx context.Context, serverID int64, cursor string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE servers SET suggestions_cursor = ? WHERE id = ?`, cursor, serverID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

const legacyCursorKey = "suggestions_last_message_id"

// ClaimUnassigned assigns all rows with a NULL server_id to the server with
// in.GuildID (created from in if needed), and moves the legacy kv cursor into
// the server if it has none. It returns the server and the number of rows claimed.
func (s *Store) ClaimUnassigned(ctx context.Context, in Server) (Server, int64, error) {
	var srv Server
	var claimed int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		srv, err = getServerByGuild(ctx, tx, in.GuildID)
		if errors.Is(err, ErrNotFound) {
			srv, err = createServer(ctx, tx, in)
		}
		if err != nil {
			return err
		}
		for _, table := range []string{"series", "videos", "suggestions"} {
			res, err := tx.ExecContext(ctx, `UPDATE `+table+` SET server_id = ? WHERE server_id IS NULL`, srv.ID)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			claimed += n
		}
		if srv.SuggestionsCursor == "" {
			var cursor string
			err := tx.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, legacyCursorKey).Scan(&cursor)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if cursor != "" {
				if _, err := tx.ExecContext(ctx,
					`UPDATE servers SET suggestions_cursor = ? WHERE id = ?`, cursor, srv.ID); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `DELETE FROM kv WHERE key = ?`, legacyCursorKey); err != nil {
					return err
				}
			}
		}
		srv, err = getServer(ctx, tx, srv.ID)
		return err
	})
	if err != nil {
		return Server{}, 0, err
	}
	return srv, claimed, nil
}

// --- access cache ---

// GetAccess returns the cached permission check for the user, if any.
func (s *Store) GetAccess(ctx context.Context, serverID int64, userID string) (AccessEntry, bool, error) {
	var allowed, manage, videos, clips int64
	var checked string
	err := s.db.QueryRowContext(ctx,
		`SELECT allowed, can_manage, can_videos, can_clips, checked_at FROM server_access WHERE server_id = ? AND user_id = ?`, serverID, userID).
		Scan(&allowed, &manage, &videos, &clips, &checked)
	if errors.Is(err, sql.ErrNoRows) {
		return AccessEntry{}, false, nil
	}
	if err != nil {
		return AccessEntry{}, false, err
	}
	checkedAt, err := parseTime(checked)
	if err != nil {
		return AccessEntry{}, false, err
	}
	return AccessEntry{Allowed: allowed != 0, CanManage: manage != 0, CanVideos: videos != 0, CanClips: clips != 0,
		CheckedAt: checkedAt}, true, nil
}

// SetAccess upserts the cached permission check.
func (s *Store) SetAccess(ctx context.Context, serverID int64, userID string, e AccessEntry) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO server_access (server_id, user_id, allowed, can_manage, can_videos, can_clips, checked_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(server_id, user_id) DO UPDATE SET allowed = excluded.allowed, can_manage = excluded.can_manage,
    can_videos = excluded.can_videos, can_clips = excluded.can_clips, checked_at = excluded.checked_at`,
		serverID, userID, e.Allowed, e.CanManage, e.CanVideos, e.CanClips, fmtTime(e.CheckedAt))
	return err
}

// --- user guilds ---

// ReplaceUserGuilds replaces the user's guild snapshot with gs.
func (s *Store) ReplaceUserGuilds(ctx context.Context, userID string, gs []UserGuild, now time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM user_guilds WHERE user_id = ?`, userID); err != nil {
			return err
		}
		for _, g := range gs {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO user_guilds (user_id, guild_id, name, icon, can_manage, updated_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(user_id, guild_id) DO UPDATE SET name = excluded.name, icon = excluded.icon,
    can_manage = excluded.can_manage, updated_at = excluded.updated_at`,
				userID, g.GuildID, g.Name, g.Icon, g.CanManage, fmtTime(now)); err != nil {
				return err
			}
		}
		return nil
	})
}

// ListUserGuilds returns the user's guild snapshot ordered by name.
func (s *Store) ListUserGuilds(ctx context.Context, userID string) ([]UserGuild, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT guild_id, name, icon, can_manage FROM user_guilds WHERE user_id = ?
ORDER BY name COLLATE NOCASE, guild_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserGuild{}
	for rows.Next() {
		var g UserGuild
		var manage int64
		if err := rows.Scan(&g.GuildID, &g.Name, &g.Icon, &manage); err != nil {
			return nil, err
		}
		g.CanManage = manage != 0
		out = append(out, g)
	}
	return out, rows.Err()
}

// --- series ---

const seriesCols = `id, server_id, title, creator, url, default_venue, notes,
youtube_playlist_id, thumbnail_video_id, created_at, updated_at`

func scanSeries(r scanner) (Series, error) {
	var x Series
	var server sql.NullInt64
	var created, updated string
	if err := r.Scan(&x.ID, &server, &x.Title, &x.Creator, &x.URL, &x.DefaultVenue, &x.Notes,
		&x.YouTubePlaylistID, &x.ThumbnailVideoID, &created, &updated); err != nil {
		return Series{}, err
	}
	x.ServerID = server.Int64
	var err error
	if x.CreatedAt, err = parseTime(created); err != nil {
		return Series{}, err
	}
	if x.UpdatedAt, err = parseTime(updated); err != nil {
		return Series{}, err
	}
	return x, nil
}

func (s *Store) ListSeries(ctx context.Context, serverID int64) ([]Series, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+seriesCols+` FROM series WHERE server_id = ? ORDER BY title COLLATE NOCASE, id`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Series{}
	for rows.Next() {
		x, err := scanSeries(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GetSeries(ctx context.Context, serverID, id int64) (Series, error) {
	return getSeries(ctx, s.db, serverID, id)
}

func getSeries(ctx context.Context, q querier, serverID, id int64) (Series, error) {
	x, err := scanSeries(q.QueryRowContext(ctx,
		`SELECT `+seriesCols+` FROM series WHERE id = ? AND server_id = ?`, id, serverID))
	return x, notFound(err)
}

var errNoServer = errors.New("store: server id required")

func (s *Store) CreateSeries(ctx context.Context, in Series) (Series, error) {
	return createSeries(ctx, s.db, in)
}

func createSeries(ctx context.Context, q querier, in Series) (Series, error) {
	if in.ServerID == 0 {
		return Series{}, errNoServer
	}
	if in.DefaultVenue == "" {
		in.DefaultVenue = VenueWatchParty
	}
	now := fmtTime(time.Now())
	res, err := q.ExecContext(ctx, `
INSERT INTO series (server_id, title, creator, url, default_venue, notes, youtube_playlist_id, thumbnail_video_id,
    created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ServerID, in.Title, in.Creator, in.URL, in.DefaultVenue, in.Notes,
		in.YouTubePlaylistID, in.ThumbnailVideoID, now, now)
	if err != nil {
		return Series{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Series{}, err
	}
	return getSeries(ctx, q, in.ServerID, id)
}

// patch accumulates the SET clauses of a partial UPDATE.
type patch struct {
	sets []string
	args []any
}

func (p *patch) set(col string, v any) {
	p.sets = append(p.sets, col+" = ?")
	p.args = append(p.args, v)
}

func setPtr[T any](p *patch, col string, v *T) {
	if v != nil {
		p.set(col, *v)
	}
}

func setTime(p *patch, col string, t *time.Time) {
	if t != nil {
		p.set(col, fmtTime(*t))
	}
}

// exec runs the UPDATE for rows matching where (a condition with placeholders
// for whereArgs), returning ErrNotFound if no row matched.
func (p *patch) exec(ctx context.Context, q querier, table, where string, whereArgs ...any) error {
	p.set("updated_at", fmtTime(time.Now()))
	args := append(p.args, whereArgs...)
	res, err := q.ExecContext(ctx, `UPDATE `+table+` SET `+strings.Join(p.sets, ", ")+` WHERE `+where, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateSeries(ctx context.Context, serverID, id int64, pt SeriesPatch) (Series, error) {
	var p patch
	setPtr(&p, "title", pt.Title)
	setPtr(&p, "creator", pt.Creator)
	setPtr(&p, "url", pt.URL)
	setPtr(&p, "default_venue", pt.DefaultVenue)
	setPtr(&p, "notes", pt.Notes)
	setPtr(&p, "youtube_playlist_id", pt.YouTubePlaylistID)
	setPtr(&p, "thumbnail_video_id", pt.ThumbnailVideoID)
	if len(p.sets) == 0 {
		return s.GetSeries(ctx, serverID, id)
	}
	if err := p.exec(ctx, s.db, "series", "id = ? AND server_id = ?", id, serverID); err != nil {
		return Series{}, err
	}
	return s.GetSeries(ctx, serverID, id)
}

// DeleteSeries deletes the series; its videos remain with series_id cleared.
// DeleteSeries deletes the series. With deleteQueued, its videos still in the
// queue are deleted too (in the same transaction); every other video of the
// series (reacted, edited, posted, skipped) is kept with series_id cleared.
func (s *Store) DeleteSeries(ctx context.Context, serverID, id int64, deleteQueued bool) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if deleteQueued {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM videos WHERE server_id = ? AND series_id = ? AND status = ?`,
				serverID, id, StatusQueued); err != nil {
				return err
			}
		}
		return deleteScoped(ctx, tx, "series", "id = ? AND server_id = ?", id, serverID)
	})
}

func deleteScoped(ctx context.Context, q querier, table, where string, args ...any) error {
	res, err := q.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+where, args...)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- videos ---

const videoCols = `id, server_id, series_id, episode, title, url, venue, status, position, scheduled_for, reacted_at,
vod_url, patreon_url, patreon_at, youtube_url, youtube_at, notes, created_by,
youtube_video_id, channel_title, duration_seconds, edit_url, suggested_by_id, suggested_by_name, editor_id, editor_name, created_at, updated_at`

func scanVideo(r scanner) (Video, error) {
	var v Video
	var server, series sql.NullInt64
	var sched, reacted, patreon, youtube sql.NullString
	var created, updated string
	err := r.Scan(&v.ID, &server, &series, &v.Episode, &v.Title, &v.URL, &v.Venue, &v.Status, &v.Position,
		&sched, &reacted, &v.VODURL, &v.PatreonURL, &patreon, &v.YouTubeURL, &youtube,
		&v.Notes, &v.CreatedBy, &v.YouTubeVideoID, &v.ChannelTitle, &v.DurationSeconds, &v.EditURL, &v.SuggestedByID, &v.SuggestedByName, &v.EditorID, &v.EditorName, &created, &updated)
	if err != nil {
		return Video{}, err
	}
	v.ServerID = server.Int64
	if series.Valid {
		v.SeriesID = &series.Int64
	}
	for _, f := range []struct {
		src sql.NullString
		dst **time.Time
	}{{sched, &v.ScheduledFor}, {reacted, &v.ReactedAt}, {patreon, &v.PatreonAt}, {youtube, &v.YouTubeAt}} {
		if *f.dst, err = parseTimePtr(f.src); err != nil {
			return Video{}, err
		}
	}
	if v.CreatedAt, err = parseTime(created); err != nil {
		return Video{}, err
	}
	if v.UpdatedAt, err = parseTime(updated); err != nil {
		return Video{}, err
	}
	return v, nil
}

// ListVideos returns the server's matching videos ordered by position, then id.
func (s *Store) ListVideos(ctx context.Context, f VideoFilter) ([]Video, error) {
	where := []string{"server_id = ?"}
	args := []any{f.ServerID}
	if len(f.Status) > 0 {
		marks := make([]string, len(f.Status))
		for i, st := range f.Status {
			marks[i] = "?"
			args = append(args, st)
		}
		where = append(where, "status IN ("+strings.Join(marks, ", ")+")")
	}
	if f.Venue != "" {
		where = append(where, "venue = ?")
		args = append(args, f.Venue)
	}
	if f.SeriesID != 0 {
		where = append(where, "series_id = ?")
		args = append(args, f.SeriesID)
	}
	q := `SELECT ` + videoCols + ` FROM videos WHERE ` + strings.Join(where, " AND ") + " ORDER BY position, id"
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetVideo(ctx context.Context, serverID, id int64) (Video, error) {
	return getVideo(ctx, s.db, serverID, id)
}

func getVideo(ctx context.Context, q querier, serverID, id int64) (Video, error) {
	v, err := scanVideo(q.QueryRowContext(ctx,
		`SELECT `+videoCols+` FROM videos WHERE id = ? AND server_id = ?`, id, serverID))
	return v, notFound(err)
}

// CreateVideo inserts a video at the end of the server's queue (position = max+1).
// Empty Status defaults to queued.
func (s *Store) CreateVideo(ctx context.Context, in Video) (Video, error) {
	return createVideo(ctx, s.db, in)
}

func createVideo(ctx context.Context, q querier, in Video) (Video, error) {
	if in.ServerID == 0 {
		return Video{}, errNoServer
	}
	if in.Status == "" {
		in.Status = StatusQueued
	}
	var series any
	if in.SeriesID != nil {
		series = *in.SeriesID
	}
	now := fmtTime(time.Now())
	res, err := q.ExecContext(ctx, `
INSERT INTO videos (server_id, series_id, episode, title, url, venue, status, position, scheduled_for, reacted_at,
    vod_url, patreon_url, patreon_at, youtube_url, youtube_at, notes, created_by,
    youtube_video_id, channel_title, duration_seconds, edit_url, suggested_by_id, suggested_by_name, editor_id, editor_name, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(position), 0) + 1 FROM videos WHERE server_id = ?),
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ServerID, series, in.Episode, in.Title, in.URL, in.Venue, in.Status, in.ServerID,
		fmtTimePtr(in.ScheduledFor), fmtTimePtr(in.ReactedAt),
		in.VODURL, in.PatreonURL, fmtTimePtr(in.PatreonAt), in.YouTubeURL, fmtTimePtr(in.YouTubeAt),
		in.Notes, in.CreatedBy, in.YouTubeVideoID, in.ChannelTitle, in.DurationSeconds, in.EditURL,
		in.SuggestedByID, in.SuggestedByName, in.EditorID, in.EditorName, now, now)
	if err != nil {
		return Video{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Video{}, err
	}
	return getVideo(ctx, q, in.ServerID, id)
}

// CreateVideos inserts vs in one transaction, appending positions after the
// server's current max in input order. All videos must share one non-zero ServerID.
func (s *Store) CreateVideos(ctx context.Context, vs []Video) ([]Video, error) {
	out := []Video{}
	if len(vs) == 0 {
		return out, nil
	}
	if vs[0].ServerID == 0 {
		return nil, errNoServer
	}
	for _, v := range vs {
		if v.ServerID != vs[0].ServerID {
			return nil, errServerMismatch
		}
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		return createVideosTx(ctx, tx, vs, &out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

var errServerMismatch = errors.New("store: videos must share one server")

func createVideosTx(ctx context.Context, q querier, vs []Video, out *[]Video) error {
	for _, v := range vs {
		c, err := createVideo(ctx, q, v)
		if err != nil {
			return err
		}
		*out = append(*out, c)
	}
	return nil
}

// CreateSeriesWithVideos creates the series, then vs with SeriesID set to it, in
// one transaction. Each video's ServerID must equal the series' ServerID.
func (s *Store) CreateSeriesWithVideos(ctx context.Context, in Series, vs []Video) (Series, []Video, error) {
	if in.ServerID == 0 {
		return Series{}, nil, errNoServer
	}
	for _, v := range vs {
		if v.ServerID != in.ServerID {
			return Series{}, nil, errServerMismatch
		}
	}
	var ser Series
	out := []Video{}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		if ser, err = createSeries(ctx, tx, in); err != nil {
			return err
		}
		items := make([]Video, len(vs))
		for i, v := range vs {
			v.SeriesID = &ser.ID
			items[i] = v
		}
		return createVideosTx(ctx, tx, items, &out)
	})
	if err != nil {
		return Series{}, nil, err
	}
	return ser, out, nil
}

func (s *Store) UpdateVideo(ctx context.Context, serverID, id int64, pt VideoPatch) (Video, error) {
	var p patch
	switch {
	case pt.SeriesID != nil:
		p.set("series_id", *pt.SeriesID)
	case pt.ClearSeries:
		p.set("series_id", nil)
	}
	setPtr(&p, "episode", pt.Episode)
	setPtr(&p, "title", pt.Title)
	setPtr(&p, "url", pt.URL)
	setPtr(&p, "venue", pt.Venue)
	setPtr(&p, "status", pt.Status)
	switch {
	case pt.ScheduledFor != nil:
		setTime(&p, "scheduled_for", pt.ScheduledFor)
	case pt.ClearSchedule:
		p.set("scheduled_for", nil)
	}
	setTime(&p, "reacted_at", pt.ReactedAt)
	setPtr(&p, "vod_url", pt.VODURL)
	setPtr(&p, "edit_url", pt.EditURL)
	setPtr(&p, "patreon_url", pt.PatreonURL)
	setTime(&p, "patreon_at", pt.PatreonAt)
	setPtr(&p, "youtube_url", pt.YouTubeURL)
	setTime(&p, "youtube_at", pt.YouTubeAt)
	setPtr(&p, "notes", pt.Notes)
	setPtr(&p, "youtube_video_id", pt.YouTubeVideoID)
	setPtr(&p, "channel_title", pt.ChannelTitle)
	setPtr(&p, "duration_seconds", pt.DurationSeconds)
	setPtr(&p, "editor_id", pt.EditorID)
	setPtr(&p, "editor_name", pt.EditorName)
	if len(p.sets) == 0 {
		return s.GetVideo(ctx, serverID, id)
	}
	if err := p.exec(ctx, s.db, "videos", "id = ? AND server_id = ?", id, serverID); err != nil {
		return Video{}, err
	}
	return s.GetVideo(ctx, serverID, id)
}

func (s *Store) DeleteVideo(ctx context.Context, serverID, id int64) error {
	return deleteScoped(ctx, s.db, "videos", "id = ? AND server_id = ?", id, serverID)
}

// ReorderVideos sets position = index for each id, in one transaction. If any
// id is not a video of the server it returns ErrNotFound and changes nothing.
func (s *Store) ReorderVideos(ctx context.Context, serverID int64, ids []int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for i, id := range ids {
			res, err := tx.ExecContext(ctx,
				`UPDATE videos SET position = ? WHERE id = ? AND server_id = ?`, i, id, serverID)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err == nil && n == 0 {
				return ErrNotFound
			}
		}
		return nil
	})
}

// --- suggestions ---

const suggestionCols = `id, server_id, discord_message_id, channel_id, author_id, author_name, content, reaction_count,
posted_at, status, reviewed_by, reviewed_at, video_id, youtube_video_id, video_title, channel_title,
duration_seconds, video_lookup_at, author_username, channel_handle, self_suggestion, discard_reason, self_override,
created_at, updated_at, removed_at`

func scanSuggestion(r scanner) (Suggestion, error) {
	var x Suggestion
	var server, video sql.NullInt64
	var reviewed, lookup, removed sql.NullString
	var posted, created, updated string
	err := r.Scan(&x.ID, &server, &x.DiscordMessageID, &x.ChannelID, &x.AuthorID, &x.AuthorName, &x.Content,
		&x.ReactionCount, &posted, &x.Status, &x.ReviewedBy, &reviewed, &video, &x.YouTubeVideoID, &x.VideoTitle,
		&x.ChannelTitle, &x.DurationSeconds, &lookup, &x.AuthorUsername, &x.ChannelHandle, &x.SelfSuggestion,
		&x.DiscardReason, &x.SelfOverride, &created, &updated, &removed)
	if err != nil {
		return Suggestion{}, err
	}
	x.ServerID = server.Int64
	if video.Valid {
		x.VideoID = &video.Int64
	}
	if x.PostedAt, err = parseTime(posted); err != nil {
		return Suggestion{}, err
	}
	if x.ReviewedAt, err = parseTimePtr(reviewed); err != nil {
		return Suggestion{}, err
	}
	if x.VideoLookupAt, err = parseTimePtr(lookup); err != nil {
		return Suggestion{}, err
	}
	if x.CreatedAt, err = parseTime(created); err != nil {
		return Suggestion{}, err
	}
	if x.UpdatedAt, err = parseTime(updated); err != nil {
		return Suggestion{}, err
	}
	if x.RemovedAt, err = parseTimePtr(removed); err != nil {
		return Suggestion{}, err
	}
	return x, nil
}

// UpsertSuggestion inserts a suggestion keyed by DiscordMessageID into
// in.ServerID. If it already exists in that server, only content, author_name,
// author_username and reaction_count are updated (review state and removed_at are never overwritten); if the
// content changed, the YouTube lookup (including channel handle and
// self-suggestion flag) is reset so it runs again. A message
// ID already used by another server is left untouched.
func (s *Store) UpsertSuggestion(ctx context.Context, in Suggestion) error {
	if in.ServerID == 0 {
		return errNoServer
	}
	now := fmtTime(time.Now())
	_, err := s.db.ExecContext(ctx, `
INSERT INTO suggestions (server_id, discord_message_id, channel_id, author_id, author_name, author_username, content,
    reaction_count, posted_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(discord_message_id) DO UPDATE SET
    youtube_video_id = CASE WHEN suggestions.content <> excluded.content THEN '' ELSE suggestions.youtube_video_id END,
    video_title = CASE WHEN suggestions.content <> excluded.content THEN '' ELSE suggestions.video_title END,
    channel_title = CASE WHEN suggestions.content <> excluded.content THEN '' ELSE suggestions.channel_title END,
    duration_seconds = CASE WHEN suggestions.content <> excluded.content THEN 0 ELSE suggestions.duration_seconds END,
    channel_handle = CASE WHEN suggestions.content <> excluded.content THEN '' ELSE suggestions.channel_handle END,
    self_suggestion = CASE WHEN suggestions.content <> excluded.content THEN 0 ELSE suggestions.self_suggestion END,
    video_lookup_at = CASE WHEN suggestions.content <> excluded.content THEN NULL ELSE suggestions.video_lookup_at END,
    content = excluded.content,
    author_name = excluded.author_name,
    author_username = excluded.author_username,
    reaction_count = excluded.reaction_count,
    updated_at = excluded.updated_at
WHERE suggestions.server_id = excluded.server_id`,
		in.ServerID, in.DiscordMessageID, in.ChannelID, in.AuthorID, in.AuthorName, in.AuthorUsername, in.Content,
		in.ReactionCount, fmtTime(in.PostedAt), now, now)
	return err
}

// PendingVideoLookups returns up to limit suggestions of the server that have
// not had their YouTube link looked up yet, oldest posted first.
func (s *Store) PendingVideoLookups(ctx context.Context, serverID int64, limit int) ([]Suggestion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+suggestionCols+
		` FROM suggestions WHERE server_id = ? AND removed_at IS NULL AND video_lookup_at IS NULL ORDER BY posted_at ASC, id ASC LIMIT ?`,
		serverID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suggestion{}
	for rows.Next() {
		x, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// SetSuggestionVideo stores the lookup result v and marks the suggestion as
// looked up at the given time. It does not touch updated_at.
func (s *Store) SetSuggestionVideo(ctx context.Context, serverID, id int64, v SuggestionVideo, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE suggestions SET youtube_video_id = ?, video_title = ?, channel_title = ?, duration_seconds = ?, video_lookup_at = ?,
    channel_handle = ?, self_suggestion = ?
WHERE id = ? AND server_id = ?`,
		v.YouTubeVideoID, v.Title, v.ChannelTitle, v.DurationSeconds, fmtTime(at),
		v.ChannelHandle, v.SelfSuggestion, id, serverID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListSuggestions(ctx context.Context, f SuggestionFilter) ([]Suggestion, error) {
	q := `SELECT ` + suggestionCols + ` FROM suggestions WHERE server_id = ? AND removed_at IS NULL`
	args := []any{f.ServerID}
	if f.Status != "" {
		q += " AND status = ?"
		args = append(args, f.Status)
	}
	if f.AuthorID != "" {
		q += " AND author_id = ?"
		args = append(args, f.AuthorID)
	}
	switch f.Sort {
	case SortOldest:
		q += " ORDER BY posted_at ASC, id ASC"
	case SortVotes:
		q += " ORDER BY reaction_count DESC, posted_at DESC, id DESC"
	default:
		q += " ORDER BY posted_at DESC, id DESC"
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suggestion{}
	for rows.Next() {
		x, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GetSuggestion(ctx context.Context, serverID, id int64) (Suggestion, error) {
	return getSuggestion(ctx, s.db, serverID, id)
}

func getSuggestion(ctx context.Context, q querier, serverID, id int64) (Suggestion, error) {
	x, err := scanSuggestion(q.QueryRowContext(ctx,
		`SELECT `+suggestionCols+` FROM suggestions WHERE id = ? AND server_id = ?`, id, serverID))
	return x, notFound(err)
}

// SetSuggestionStatus sets status and review fields (pending clears them).
// Discarding or approving clears discard_reason (a manual action); restoring
// an automatic self-suggestion discard sets self_override so it isn't redone.
func (s *Store) SetSuggestionStatus(ctx context.Context, serverID, id int64, status SuggestionStatus, reviewerID string, now time.Time) (Suggestion, error) {
	var res sql.Result
	var err error
	if status == SuggestionPending {
		res, err = s.db.ExecContext(ctx,
			`UPDATE suggestions SET status = ?, reviewed_by = '', reviewed_at = NULL, updated_at = ?,
    self_override = CASE WHEN discard_reason = ? THEN 1 ELSE self_override END, discard_reason = '' WHERE id = ? AND server_id = ? AND removed_at IS NULL`,
			status, fmtTime(time.Now()), DiscardSelfSuggestion, id, serverID)
	} else {
		res, err = s.db.ExecContext(ctx,
			`UPDATE suggestions SET status = ?, reviewed_by = ?, reviewed_at = ?, updated_at = ?, discard_reason = '' WHERE id = ? AND server_id = ? AND removed_at IS NULL`,
			status, reviewerID, fmtTime(now), fmtTime(time.Now()), id, serverID)
	}
	if err != nil {
		return Suggestion{}, err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return Suggestion{}, ErrNotFound
	}
	return s.GetSuggestion(ctx, serverID, id)
}

// ApproveSuggestion creates v in the server (as CreateVideo would) and marks the
// suggestion approved and linked to it, in one transaction.
func (s *Store) ApproveSuggestion(ctx context.Context, serverID, id int64, v Video, reviewerID string, now time.Time) (Suggestion, Video, error) {
	var sug Suggestion
	var vid Video
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cur, err := getSuggestion(ctx, tx, serverID, id)
		if err != nil {
			return err
		}
		if cur.RemovedAt != nil {
			return ErrNotFound
		}
		v.ServerID = serverID
		v.SuggestedByID = cur.AuthorID
		v.SuggestedByName = cur.AuthorName
		if vid, err = createVideo(ctx, tx, v); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `
UPDATE suggestions SET status = ?, reviewed_by = ?, reviewed_at = ?, video_id = ?, updated_at = ?, discard_reason = ''
WHERE id = ? AND server_id = ?`,
			SuggestionApproved, reviewerID, fmtTime(now), vid.ID, fmtTime(time.Now()), id, serverID); err != nil {
			return err
		}
		sug, err = getSuggestion(ctx, tx, serverID, id)
		return err
	})
	if err != nil {
		return Suggestion{}, Video{}, err
	}
	return sug, vid, nil
}

// DiscardSelfSuggestions marks the server's pending, self-flagged, non-overridden
// suggestions as discarded with discard_reason 'self_suggestion' (reviewed_by
// is empty, reviewed_at is now). It returns the number of suggestions discarded.
func (s *Store) DiscardSelfSuggestions(ctx context.Context, serverID int64, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE suggestions SET status = ?, discard_reason = ?, reviewed_by = '', reviewed_at = ?, updated_at = ?
WHERE server_id = ? AND removed_at IS NULL AND status = ? AND self_suggestion = 1 AND self_override = 0`,
		SuggestionDiscarded, DiscardSelfSuggestion, fmtTime(now), fmtTime(time.Now()), serverID, SuggestionPending)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RemoveSuggestions sets removed_at = now on the given ids of the server that
// aren't removed yet, in one statement; ids from other servers are ignored.
// It returns the number of rows changed.
func (s *Store) RemoveSuggestions(ctx context.Context, serverID int64, ids []int64, now time.Time) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(ids)+3)
	args = append(args, fmtTime(now), fmtTime(time.Now()), serverID)
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE suggestions SET removed_at = ?, updated_at = ? WHERE server_id = ? AND removed_at IS NULL AND id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`)`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// suggestersChunk is the most video IDs SuggestersByVideo puts in one query.
const suggestersChunk = 500

// SuggestersByVideo returns, for each of the given YouTube video IDs (empty ones ignored, duplicates
// collapsed), the distinct authors of the server's non-removed suggestions linking it, in any status,
// ordered by their earliest suggestion (FirstAt). Video IDs with no suggestions are absent from the map.
// AuthorName is the name on that author's most recent suggestion of the video.
func (s *Store) SuggestersByVideo(ctx context.Context, serverID int64, videoIDs []string) (map[string][]Suggester, error) {
	out := map[string][]Suggester{}
	seen := make(map[string]bool, len(videoIDs))
	ids := make([]string, 0, len(videoIDs))
	for _, id := range videoIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for len(ids) > 0 {
		n := min(len(ids), suggestersChunk)
		if err := s.suggestersChunk(ctx, serverID, ids[:n], out); err != nil {
			return nil, err
		}
		ids = ids[n:]
	}
	return out, nil
}

// suggestersChunk adds the suggesters of one chunk of video IDs to out. Rows come oldest first, so an
// author's first row sets FirstAt and each later row updates the name to the latest one.
func (s *Store) suggestersChunk(ctx context.Context, serverID int64, ids []string, out map[string][]Suggester) error {
	args := make([]any, 0, len(ids)+1)
	args = append(args, serverID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT youtube_video_id, author_id, author_name, posted_at FROM suggestions
WHERE server_id = ? AND removed_at IS NULL AND youtube_video_id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+`) ORDER BY posted_at ASC, id ASC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var vid, author, name, posted string
		if err := rows.Scan(&vid, &author, &name, &posted); err != nil {
			return err
		}
		at, err := parseTime(posted)
		if err != nil {
			return err
		}
		list := out[vid]
		i := slices.IndexFunc(list, func(x Suggester) bool { return x.AuthorID == author })
		if i < 0 {
			out[vid] = append(list, Suggester{AuthorID: author, AuthorName: name, FirstAt: at})
		} else {
			list[i].AuthorName = name
		}
	}
	return rows.Err()
}

// UnremoveSuggestion clears removed_at (ErrNotFound if the id isn't in the server).
func (s *Store) UnremoveSuggestion(ctx context.Context, serverID, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE suggestions SET removed_at = NULL, updated_at = ? WHERE id = ? AND server_id = ?`,
		fmtTime(time.Now()), id, serverID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- key/value ---

// GetKV returns "" with no error when the key is absent.
func (s *Store) GetKV(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM kv WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetKV(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO kv (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
