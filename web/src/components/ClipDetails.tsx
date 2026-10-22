import { useState } from 'react'
import { PLATFORMS, updateClip, type Clip, type ClipPatch, type ClipStatus } from '../api'
import { useServer } from '../session'
import { errMsg, formatDate } from '../util'
import { ClipImage } from './ClipCard'
import { ErrorBox } from './Status'

interface Props {
  clip: Clip
  onChanged: () => void
  onClose: () => void
}

const STATUS_OPTIONS: { id: ClipStatus; label: string }[] = [
  { id: 'new', label: 'New (back to inbox)' },
  { id: 'pending', label: 'Pending' },
  { id: 'edited', label: 'Edited' },
  { id: 'posted', label: 'Posted' },
  { id: 'discarded', label: 'Discarded' },
]

type Fields = {
  edited_title: string
  edited_url: string
  youtube_url: string
  instagram_url: string
  x_url: string
  tiktok_url: string
  notes: string
}

/** Full details and editing for one clip; key it by clip id so the form resets when switching clips. */
export default function ClipDetails({ clip, onChanged, onClose }: Props) {
  const { id: sid } = useServer()
  const [form, setForm] = useState<Fields>({
    edited_title: clip.edited_title,
    edited_url: clip.edited_url,
    youtube_url: clip.youtube_url,
    instagram_url: clip.instagram_url,
    x_url: clip.x_url,
    tiktok_url: clip.tiktok_url,
    notes: clip.notes,
  })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const changed = (Object.keys(form) as (keyof Fields)[]).filter((k) => form[k].trim() !== clip[k])
  const set = (k: keyof Fields, v: string) => setForm((f) => ({ ...f, [k]: v }))

  async function run(p: ClipPatch, leaves: boolean) {
    setBusy(true)
    setError(null)
    try {
      await updateClip(sid, clip.id, p)
      onChanged()
      if (leaves) onClose()
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  const changeStatus = (status: ClipStatus) =>
    run({ status }, status === 'new' || status === 'discarded')
  const save = () => {
    const p: ClipPatch = {}
    for (const k of changed) p[k] = form[k].trim()
    return run(p, false)
  }

  const links = [
    { label: 'Edited video', url: clip.edited_url },
    ...PLATFORMS.map((p) => ({ label: p.label, url: clip[p.field] })),
  ].filter((l) => l.url)

  return (
    <aside className="details" aria-label="Clip details">
      <div className="details-head">
        <h2>Details</h2>
        <button type="button" className="details-close" aria-label="Close details" onClick={onClose}>
          ×
        </button>
      </div>
      <div className="details-body">
        <ClipImage clip={clip} />
        <div className="card-title">{clip.title || 'Untitled clip'}</div>
        <div className="links">
          <a href={clip.clip_url} target="_blank" rel="noreferrer noopener">
            Twitch clip
          </a>
          {clip.message_url && (
            <a href={clip.message_url} target="_blank" rel="noreferrer noopener">
              Discord message
            </a>
          )}
        </div>
        <div className="muted small">
          {clip.clipper_name && `clipped by ${clip.clipper_name}`}
          {clip.clipper_name && clip.author_name && ' · '}
          {clip.author_name && `shared by ${clip.author_name}`}
          {clip.broadcaster_name && ` · ${clip.broadcaster_name}`}
          {clip.posted_at && ` · ${formatDate(clip.posted_at)}`}
        </div>
        <label className="details-status">
          Status
          <select value={clip.status} disabled={busy} onChange={(e) => changeStatus(e.target.value as ClipStatus)}>
            {STATUS_OPTIONS.map((s) => (
              <option key={s.id} value={s.id}>
                {s.label}
              </option>
            ))}
          </select>
        </label>
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
            Edited title
            <input type="text" value={form.edited_title} onChange={(e) => set('edited_title', e.target.value)} />
          </label>
          <label>
            Edited video link
            <input
              type="url"
              value={form.edited_url}
              placeholder="Drive, Dropbox, …"
              onChange={(e) => set('edited_url', e.target.value)}
            />
          </label>
          {PLATFORMS.map((p) => (
            <label key={p.id}>
              {p.label} URL
              <input type="url" value={form[p.field]} onChange={(e) => set(p.field, e.target.value)} />
            </label>
          ))}
          <label>
            Notes
            <textarea className="clip-notes" value={form.notes} onChange={(e) => set('notes', e.target.value)} />
          </label>
        </div>
        {error && <ErrorBox message={error} />}
        {changed.length > 0 && (
          <div className="actions">
            <button type="button" className="primary" disabled={busy} onClick={save}>
              Save
            </button>
          </div>
        )}
      </div>
    </aside>
  )
}
