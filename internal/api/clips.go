package api

import (
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/danotchuuy/video-queue/internal/discord"
	"github.com/danotchuuy/video-queue/internal/store"
)

const maxNotesLen = 2000

// --- clips ---

func (a *API) listClips(w http.ResponseWriter, r *http.Request, srv store.Server) {
	q := r.URL.Query()
	f := store.ClipFilter{ServerID: srv.ID}
	if s := q.Get("status"); s != "" {
		for _, part := range strings.Split(s, ",") {
			st := store.ClipStatus(strings.TrimSpace(part))
			if !st.Valid() {
				writeError(w, http.StatusBadRequest, "invalid status")
				return
			}
			f.Statuses = append(f.Statuses, st)
		}
	}
	switch s := store.ClipSort(q.Get("sort")); s {
	case "":
		f.Sort = store.ClipSortNewest
	case store.ClipSortNewest, store.ClipSortOldest, store.ClipSortStatusAt:
		f.Sort = s
	default:
		writeError(w, http.StatusBadRequest, "invalid sort")
		return
	}
	if s := q.Get("missing"); s != "" {
		for _, part := range strings.Split(s, ",") {
			p := store.Platform(strings.TrimSpace(part))
			switch p {
			case store.PlatformYouTube, store.PlatformInstagram, store.PlatformX, store.PlatformTikTok:
				f.Missing = append(f.Missing, p)
			default:
				writeError(w, http.StatusBadRequest, "invalid missing platform")
				return
			}
		}
	}
	list, err := a.st.ListClips(r.Context(), f)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	if list == nil {
		list = []store.Clip{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (a *API) getClip(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	c, err := a.st.GetClip(r.Context(), srv.ID, id)
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) patchClip(w http.ResponseWriter, r *http.Request, srv store.Server) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	u, ok := a.user(w, r)
	if !ok {
		return
	}
	var p store.ClipPatch
	if !decodeJSON(w, r, &p) {
		return
	}
	if p.Status != nil && !p.Status.Valid() {
		writeError(w, http.StatusBadRequest, "invalid status")
		return
	}
	if p.EditedTitle != nil {
		t := strings.TrimSpace(*p.EditedTitle)
		if utf8.RuneCountInString(t) > maxTitleLen {
			writeError(w, http.StatusBadRequest, "edited_title is too long (max 300 characters)")
			return
		}
		p.EditedTitle = &t
	}
	if p.Notes != nil && utf8.RuneCountInString(*p.Notes) > maxNotesLen {
		writeError(w, http.StatusBadRequest, "notes is too long (max 2000 characters)")
		return
	}
	if msg := validLinks(map[string]*string{
		"edited_url": p.EditedURL, "youtube_url": p.YouTubeURL, "instagram_url": p.InstagramURL,
		"x_url": p.XURL, "tiktok_url": p.TikTokURL,
	}); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	c, err := a.st.UpdateClip(r.Context(), srv.ID, id, p, u.DiscordID, a.now().UTC())
	if err != nil {
		a.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *API) syncClips(w http.ResponseWriter, r *http.Request, srv store.Server) {
	if a.sync == nil {
		writeError(w, http.StatusServiceUnavailable, "clip sync is not configured")
		return
	}
	if srv.ClipsChannelID == "" {
		writeError(w, http.StatusBadRequest, "this server has no clips channel")
		return
	}
	if err := a.sync(r.Context(), srv.ID); err != nil {
		a.log.Error("clip sync failed", "server", srv.ID, "err", err)
		if errors.Is(err, discord.ErrMessageContentMissing) {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeError(w, http.StatusBadGateway, "clip sync failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
