// Package auth implements Discord OAuth login (identify and guilds scopes) and cookie-based
// sessions. Access to individual servers is checked by package access.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/danotchuuy/video-queue/internal/discord"
	"github.com/danotchuuy/video-queue/internal/store"
)

type Config struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string // e.g. http://localhost:5173/api/auth/callback
	CookieSecure bool   // set the Secure flag on cookies (true behind HTTPS)
	SessionTTL   time.Duration
}

// SessionStore is the subset of *store.Store that auth needs.
type SessionStore interface {
	UpsertUser(ctx context.Context, u store.User) error
	CreateSession(ctx context.Context, tokenHash, userID string, expiresAt time.Time) error
	UserBySession(ctx context.Context, tokenHash string, now time.Time) (store.User, error)
	DeleteSession(ctx context.Context, tokenHash string) error
	ReplaceUserGuilds(ctx context.Context, userID string, gs []store.UserGuild, now time.Time) error
}

const (
	sessionCookie = "vq_session"
	stateCookie   = "vq_oauth_state"
	stateTTL      = 10 * time.Minute

	defaultAuthorizeURL = "https://discord.com/oauth2/authorize"
	defaultAPIBase      = "https://discord.com/api/v10"
)

type Service struct {
	cfg   Config
	store SessionStore

	// Overridable in tests.
	authorizeURL string
	apiBase      string
	client       *http.Client
	log          *slog.Logger
}

func New(cfg Config, st SessionStore) *Service {
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = 7 * 24 * time.Hour
	}
	return &Service{
		cfg:          cfg,
		store:        st,
		authorizeURL: defaultAuthorizeURL,
		apiBase:      defaultAPIBase,
		client:       &http.Client{Timeout: 10 * time.Second},
		log:          slog.Default(),
	}
}

// Register adds GET /api/auth/login, GET /api/auth/callback and POST /api/auth/logout.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth/login", s.login)
	mux.HandleFunc("GET /api/auth/callback", s.callback)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
}

func (s *Service) login(w http.ResponseWriter, r *http.Request) {
	state, err := randomToken()
	if err != nil {
		s.log.Error("auth: generate state", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     stateCookie,
		Value:    state,
		Path:     "/",
		MaxAge:   int(stateTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {s.cfg.ClientID},
		"scope":         {"identify guilds"},
		"state":         {state},
		"redirect_uri":  {s.cfg.RedirectURL},
	}
	http.Redirect(w, r, s.authorizeURL+"?"+q.Encode(), http.StatusFound)
}

func (s *Service) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	c, err := r.Cookie(stateCookie)
	state := q.Get("state")
	if err != nil || c.Value == "" || state == "" ||
		subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		writeError(w, http.StatusBadRequest, "invalid state")
		return
	}
	// The state is single-use.
	http.SetCookie(w, &http.Cookie{
		Name: stateCookie, Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.cfg.CookieSecure, SameSite: http.SameSiteLaxMode,
	})

	if q.Get("error") != "" {
		http.Redirect(w, r, "/login?error=cancelled", http.StatusFound)
		return
	}
	code := q.Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "missing code")
		return
	}

	ctx := r.Context()
	fail := func(msg string, err error) {
		s.log.Error("auth: "+msg, "err", err)
		http.Redirect(w, r, "/login?error=discord", http.StatusFound)
	}

	accessToken, err := s.exchangeCode(ctx, code)
	if err != nil {
		fail("token exchange", err)
		return
	}
	du, err := s.fetchUser(ctx, accessToken)
	if err != nil {
		fail("fetch user", err)
		return
	}

	now := time.Now()
	user := store.User{
		DiscordID:   du.ID,
		Username:    du.Username,
		GlobalName:  du.GlobalName,
		Avatar:      du.Avatar,
		CreatedAt:   now,
		LastLoginAt: now,
	}
	if err := s.store.UpsertUser(ctx, user); err != nil {
		fail("upsert user", err)
		return
	}
	if gs, err := s.fetchGuilds(ctx, accessToken); err != nil {
		s.log.Warn("auth: fetch user guilds", "err", err)
	} else if err := s.store.ReplaceUserGuilds(ctx, du.ID, gs, now); err != nil {
		s.log.Warn("auth: store user guilds", "err", err)
	}
	token, err := randomToken()
	if err != nil {
		fail("generate session token", err)
		return
	}
	if err := s.store.CreateSession(ctx, hashToken(token), du.ID, now.Add(s.cfg.SessionTTL)); err != nil {
		fail("create session", err)
		return
	}
	http.SetCookie(w, s.sessionCookie(token, int(s.cfg.SessionTTL.Seconds())))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.store.DeleteSession(r.Context(), hashToken(c.Value)); err != nil {
			s.log.Error("auth: delete session", "err", err)
		}
	}
	http.SetCookie(w, s.sessionCookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

// Require rejects requests without a valid session (401 JSON {"error": "..."})
// and otherwise puts the user in the request context.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || c.Value == "" {
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		u, err := s.store.UserBySession(r.Context(), hashToken(c.Value), time.Now())
		if err != nil {
			if !errors.Is(err, store.ErrNotFound) {
				s.log.Error("auth: lookup session", "err", err)
			}
			writeError(w, http.StatusUnauthorized, "not signed in")
			return
		}
		switch r.Method {
		case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
			if r.ContentLength != 0 {
				mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil || mt != "application/json" {
					writeError(w, http.StatusUnsupportedMediaType, "content type must be application/json")
					return
				}
			}
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	})
}

type ctxKey struct{}

// UserFromContext returns the signed-in user set by Require.
func UserFromContext(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(ctxKey{}).(store.User)
	return u, ok
}

// WithUser returns ctx carrying u; used by Require and by tests of other packages.
func WithUser(ctx context.Context, u store.User) context.Context {
	return context.WithValue(ctx, ctxKey{}, u)
}

// --- Discord API ---

type discordUser struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Avatar     string `json:"avatar"`
}

func (s *Service) exchangeCode(ctx context.Context, code string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {s.cfg.RedirectURL},
		"client_id":     {s.cfg.ClientID},
		"client_secret": {s.cfg.ClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.apiBase+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if _, err := s.doJSON(req, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", errors.New("empty access token")
	}
	return out.AccessToken, nil
}

func (s *Service) fetchUser(ctx context.Context, token string) (discordUser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.apiBase+"/users/@me", nil)
	if err != nil {
		return discordUser{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var u discordUser
	if _, err := s.doJSON(req, &u); err != nil {
		return discordUser{}, err
	}
	if u.ID == "" {
		return discordUser{}, errors.New("user has no id")
	}
	return u, nil
}

const guildPageSize = 200

// fetchGuilds lists the user's guilds (paginated) as a snapshot for ReplaceUserGuilds.
func (s *Service) fetchGuilds(ctx context.Context, token string) ([]store.UserGuild, error) {
	var all []store.UserGuild
	after := ""
	for {
		path := s.apiBase + "/users/@me/guilds?limit=" + strconv.Itoa(guildPageSize)
		if after != "" {
			path += "&after=" + url.QueryEscape(after)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		var page []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Icon        string `json:"icon"`
			Owner       bool   `json:"owner"`
			Permissions string `json:"permissions"`
		}
		if _, err := s.doJSON(req, &page); err != nil {
			return nil, err
		}
		for _, g := range page {
			all = append(all, store.UserGuild{
				GuildID:   g.ID,
				Name:      g.Name,
				Icon:      g.Icon,
				CanManage: g.Owner || discord.HasManagePermission(g.Permissions),
			})
		}
		if len(page) < guildPageSize {
			return all, nil
		}
		after = page[len(page)-1].ID
	}
}

// doJSON performs req and decodes a 2xx JSON body into out. It returns the
// HTTP status (0 on transport errors) so callers can special-case statuses.
func (s *Service) doJSON(req *http.Request, out any) (int, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("%s %s: unexpected status %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

// --- helpers ---

func (s *Service) sessionCookie(value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     sessionCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cfg.CookieSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

// randomToken returns 32 random bytes, base64url-encoded.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken is the session key stored in the database.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
