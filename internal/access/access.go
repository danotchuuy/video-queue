// Package access decides what a user may do on a server: app admins can do
// everything; others may use it if they are allowlisted, hold a mod role or
// can manage the Discord guild, and may manage it only if they can manage the
// guild. Lookups are cached in the store.
package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/danotchuuy/video-queue/internal/discord"
	"github.com/danotchuuy/video-queue/internal/store"
)

// maxStale is how old a cached entry may be and still be used when Discord
// can't be reached.
const maxStale = 24 * time.Hour

// fetchTimeout bounds a shared member lookup.
const fetchTimeout = 15 * time.Second

// Store is the subset of *store.Store that access needs.
type Store interface {
	GetAccess(ctx context.Context, serverID int64, userID string) (e store.AccessEntry, found bool, err error)
	SetAccess(ctx context.Context, serverID int64, userID string, e store.AccessEntry) error
}

// Discord looks up guilds and their members through the bot.
type Discord interface {
	GuildMember(ctx context.Context, guildID, userID string) (discord.Member, error) // discord.ErrNotFound if not a member
	Guild(ctx context.Context, guildID string) (discord.Guild, error)                // includes OwnerID and Roles
}

// Perms is what a user may do on a server.
type Perms struct {
	Access bool // any section: Full || Videos || Clips
	Full   bool // every section (mod / allowed user / manager / admin)
	Manage bool // may configure or delete the server
	Videos bool // video pages: Full || video editor role
	Clips  bool // clip pages: Full || clip editor role
}

// fromEntry maps a cached entry to Perms.
func fromEntry(e store.AccessEntry) Perms {
	return Perms{
		Access: e.Allowed || e.CanVideos || e.CanClips,
		Full:   e.Allowed,
		Manage: e.CanManage,
		Videos: e.Allowed || e.CanVideos,
		Clips:  e.Allowed || e.CanClips,
	}
}

// allPerms is what app admins and allowlisted users get (admins also manage).
func allPerms(manage bool) Perms {
	return Perms{Access: true, Full: true, Manage: manage, Videos: true, Clips: true}
}

type flightKey struct {
	serverID int64
	userID   string
}

// flight is one in-progress check shared by concurrent callers.
type flight struct {
	done  chan struct{}
	perms Perms
	err   error
}

type cachedGuild struct {
	g  discord.Guild
	at time.Time
}

type Checker struct {
	st     Store
	d      Discord
	admins []string
	ttl    time.Duration
	log    *slog.Logger

	mu      sync.Mutex
	flights map[flightKey]*flight
	guilds  map[string]cachedGuild

	now func() time.Time // overridable in tests
}

func New(st Store, d Discord, adminIDs []string, ttl time.Duration, log *slog.Logger) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{
		st:      st,
		d:       d,
		admins:  adminIDs,
		ttl:     ttl,
		log:     log,
		flights: map[flightKey]*flight{},
		guilds:  map[string]cachedGuild{},
		now:     time.Now,
	}
}

// IsAdmin reports whether the user is an app admin.
func (c *Checker) IsAdmin(userID string) bool {
	return slices.Contains(c.admins, userID)
}

// Check reports what the user may do on the server.
func (c *Checker) Check(ctx context.Context, userID string, srv store.Server) (Perms, error) {
	if c.IsAdmin(userID) {
		return allPerms(true), nil
	}

	cached, found, err := c.st.GetAccess(ctx, srv.ID, userID)
	if err != nil {
		c.log.Error("access: read cache", "server", srv.ID, "user", userID, "err", err)
		found = false
	}
	age := c.now().Sub(cached.CheckedAt)
	if found && age < c.ttl {
		return fromEntry(cached), nil
	}

	perms, err := c.check(ctx, userID, srv)
	if err != nil {
		if found && age < maxStale {
			c.log.Warn("access: member check failed, using cached value",
				"server", srv.ID, "user", userID, "cached", cached, "err", err)
			return fromEntry(cached), nil
		}
		if slices.Contains(srv.AllowedUserIDs, userID) {
			c.log.Warn("access: member check failed, allowing allowlisted user",
				"server", srv.ID, "user", userID, "err", err)
			return allPerms(false), nil
		}
		return Perms{}, err
	}
	return perms, nil
}

// CanManageGuild reports whether the user may manage the Discord guild. It
// asks Discord every time.
func (c *Checker) CanManageGuild(ctx context.Context, userID, guildID string) (bool, error) {
	if c.IsAdmin(userID) {
		return true, nil
	}
	m, err := c.d.GuildMember(ctx, guildID, userID)
	if errors.Is(err, discord.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("fetch member: %w", err)
	}
	g, err := c.d.Guild(ctx, guildID)
	if errors.Is(err, discord.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("fetch guild: %w", err)
	}
	return discord.CanManage(g, m, userID), nil
}

// check runs fetch once per (server, user) at a time; concurrent callers wait
// for and share that result.
func (c *Checker) check(ctx context.Context, userID string, srv store.Server) (Perms, error) {
	key := flightKey{srv.ID, userID}
	c.mu.Lock()
	if f, ok := c.flights[key]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.perms, f.err
		case <-ctx.Done():
			return Perms{}, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	c.flights[key] = f
	c.mu.Unlock()

	// The fetch is shared by every waiter, so it must not be cancelled just
	// because the first caller's request went away.
	fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fetchTimeout)
	f.perms, f.err = c.fetch(fetchCtx, userID, srv)
	cancel()

	c.mu.Lock()
	delete(c.flights, key)
	c.mu.Unlock()
	close(f.done)
	return f.perms, f.err
}

// fetch asks Discord about the member and guild and caches the answer.
func (c *Checker) fetch(ctx context.Context, userID string, srv store.Server) (Perms, error) {
	allowlisted := slices.Contains(srv.AllowedUserIDs, userID)
	perms := Perms{Access: allowlisted, Full: allowlisted, Videos: allowlisted, Clips: allowlisted}
	m, err := c.d.GuildMember(ctx, srv.GuildID, userID)
	switch {
	case errors.Is(err, discord.ErrNotFound):
	case err != nil:
		return Perms{}, fmt.Errorf("fetch member: %w", err)
	default:
		g, err := c.guild(ctx, srv.GuildID)
		if err != nil {
			return Perms{}, fmt.Errorf("fetch guild: %w", err)
		}
		hasRole := func(ids []string) bool {
			return slices.ContainsFunc(m.Roles, func(r string) bool { return slices.Contains(ids, r) })
		}
		perms.Manage = discord.CanManage(g, m, userID)
		perms.Full = allowlisted || perms.Manage || hasRole(srv.ModRoleIDs)
		perms.Videos = perms.Full || hasRole(srv.VideoEditorRoleIDs)
		perms.Clips = perms.Full || hasRole(srv.ClipEditorRoleIDs)
		perms.Access = perms.Full || perms.Videos || perms.Clips
	}
	e := store.AccessEntry{Allowed: perms.Full, CanManage: perms.Manage, CanVideos: perms.Videos, CanClips: perms.Clips, CheckedAt: c.now()}
	if err := c.st.SetAccess(ctx, srv.ID, userID, e); err != nil {
		c.log.Error("access: write cache", "server", srv.ID, "user", userID, "err", err)
	}
	return perms, nil
}

// guild returns the guild, from memory if fetched within ttl.
func (c *Checker) guild(ctx context.Context, guildID string) (discord.Guild, error) {
	c.mu.Lock()
	cg, ok := c.guilds[guildID]
	c.mu.Unlock()
	if ok && c.now().Sub(cg.at) < c.ttl {
		return cg.g, nil
	}
	g, err := c.d.Guild(ctx, guildID)
	if err != nil {
		return discord.Guild{}, err
	}
	c.mu.Lock()
	c.guilds[guildID] = cachedGuild{g, c.now()}
	c.mu.Unlock()
	return g, nil
}
