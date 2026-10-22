import { useEffect, useState } from 'react'
import { listMembers, updateVideo, type Member, type Series, type Video, type VideoStatus } from '../api'
import { STATUSES, STATUS_LABELS, VENUE_LABELS, errMsg, formatDate } from '../util'
import { useServer, useSession } from '../session'
import { ErrorBox } from './Status'
import Thumbnail from './Thumbnail'

interface Props {
  video: Video
  series?: Series
  onChanged: () => void
  onClose: () => void
}

/** Full details and editing for one video; key it by video id so the form resets when switching videos. */
export default function VideoDetails({ video, series, onChanged, onClose }: Props) {
  const { id: sid } = useServer()
  const { me } = useSession()
  const [members, setMembers] = useState<Member[]>([])
  const [otherMode, setOtherMode] = useState(false)
  const [otherName, setOtherName] = useState(video.editor_id ? '' : video.editor_name)
  const [vod, setVod] = useState(video.vod_url)
  const [edit, setEdit] = useState(video.edit_url)
  const [patreon, setPatreon] = useState(video.patreon_url)
  const [youtube, setYoutube] = useState(video.youtube_url)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    listMembers(sid)
      .then((m) => {
        if (!cancelled) setMembers(m)
      })
      .catch((e) => {
        if (!cancelled) setError(errMsg(e))
      })
    return () => {
      cancelled = true
    }
  }, [sid])

  const dirty =
    vod !== video.vod_url ||
    edit !== video.edit_url ||
    patreon !== video.patreon_url ||
    youtube !== video.youtube_url

  async function run(fn: () => Promise<unknown>) {
    setBusy(true)
    setError(null)
    try {
      await fn()
      onChanged()
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  const changeStatus = (status: VideoStatus) => run(() => updateVideo(sid, video.id, { status }))
  const OTHER = '__other'
  const freeText = !video.editor_id && video.editor_name !== ''
  const editorValue = otherMode || freeText ? OTHER : video.editor_id
  const orphan = video.editor_id !== '' && !members.some((m) => m.discord_id === video.editor_id)
  const changeEditor = (v: string) => {
    if (v === OTHER) {
      setOtherMode(true)
      return
    }
    setOtherMode(false)
    if (v === video.editor_id) return
    run(() => updateVideo(sid, video.id, v ? { editor_id: v } : { editor_id: '', editor_name: '' }))
  }
  const saveOther = () => run(() => updateVideo(sid, video.id, { editor_id: '', editor_name: otherName.trim() }))
  const saveUrls = () =>
    run(() =>
      updateVideo(sid, video.id, {
        vod_url: vod.trim(),
        edit_url: edit.trim(),
        patreon_url: patreon.trim(),
        youtube_url: youtube.trim(),
      }),
    )

  const links = [
    { label: 'VOD', url: video.vod_url },
    { label: 'Edited file', url: video.edit_url },
    { label: 'Patreon', url: video.patreon_url },
    { label: 'YouTube', url: video.youtube_url },
  ].filter((l) => l.url)

  return (
    <aside className="details" aria-label="Video details">
      <div className="details-head">
        <h2>Details</h2>
        <button type="button" className="details-close" aria-label="Close details" onClick={onClose}>
          ×
        </button>
      </div>
      <div className="details-body">
        <Thumbnail
          videoId={video.youtube_video_id}
          title={video.title}
          seconds={video.duration_seconds}
          size="fill"
        />
        <div className="card-title">{video.title}</div>
        {(series || video.episode) && (
          <div className="muted">
            {series?.title}
            {video.episode && ` #${video.episode}`}
          </div>
        )}
        <div className="muted small">
          {VENUE_LABELS[video.venue]}
          {video.reacted_at && ` · reacted ${formatDate(video.reacted_at)}`}
          {video.patreon_at && ` · Patreon ${formatDate(video.patreon_at)}`}
          {video.youtube_at && ` · YouTube ${formatDate(video.youtube_at)}`}
        </div>
        {video.suggested_by_name && <div className="muted small">Suggested by {video.suggested_by_name}</div>}
        <label className="details-status">
          Status
          <select
            value={video.status}
            disabled={busy}
            onChange={(e) => changeStatus(e.target.value as VideoStatus)}
          >
            {STATUSES.map((s) => (
              <option key={s} value={s}>
                {STATUS_LABELS[s]}
              </option>
            ))}
          </select>
        </label>
        <label className="details-status">
          Editor
          <select value={editorValue} disabled={busy} onChange={(e) => changeEditor(e.target.value)}>
            <option value="">Unassigned</option>
            {orphan && <option value={video.editor_id}>{video.editor_name || video.editor_id}</option>}
            {members.map((m) => (
              <option key={m.discord_id} value={m.discord_id}>
                {m.name}
                {m.discord_id === me.user.discord_id && ' (you)'}
              </option>
            ))}
            <option value={OTHER}>Other…</option>
          </select>
        </label>
        {editorValue === OTHER && (
          <div className="editor-other">
            <input
              type="text"
              value={otherName}
              maxLength={100}
              placeholder="Editor name"
              aria-label="Editor name"
              disabled={busy}
              onChange={(e) => setOtherName(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  saveOther()
                }
              }}
            />
            <button type="button" className="primary" disabled={busy} onClick={saveOther}>
              Save
            </button>
          </div>
        )}
        {links.length > 0 && (
          <div className="links">
            {links.map((l) => (
              <a key={l.label} href={l.url} target="_blank" rel="noreferrer noopener">
                {l.label}
              </a>
            ))}
          </div>
        )}
        <div className="url-grid">
          <label>
            VOD URL
            <input type="url" value={vod} onChange={(e) => setVod(e.target.value)} />
          </label>
          <label>
            Edited file URL
            <input
              type="url"
              value={edit}
              placeholder="Drive, Dropbox, …"
              onChange={(e) => setEdit(e.target.value)}
            />
          </label>
          <label>
            Patreon URL
            <input type="url" value={patreon} onChange={(e) => setPatreon(e.target.value)} />
          </label>
          <label>
            YouTube URL
            <input type="url" value={youtube} onChange={(e) => setYoutube(e.target.value)} />
          </label>
        </div>
        {error && <ErrorBox message={error} />}
        {dirty && (
          <div className="actions">
            <button type="button" className="primary" disabled={busy} onClick={saveUrls}>
              Save URLs
            </button>
          </div>
        )}
      </div>
    </aside>
  )
}
