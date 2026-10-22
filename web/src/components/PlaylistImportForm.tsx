import { useState } from 'react'
import { importSeries, lookupPlaylist, type PlaylistPreview, type Series, type Venue } from '../api'
import { useServer } from '../session'
import { VENUES, VENUE_LABELS, errMsg } from '../util'
import PlaylistPicker from './PlaylistPicker'
import { ErrorBox } from './Status'

interface Props {
  onImported: (series: Series) => void
  onCancel: () => void
}

/** Create a series from a YouTube playlist: load a preview, pick videos, import. */
export default function PlaylistImportForm({ onImported, onCancel }: Props) {
  const { id: sid } = useServer()
  const [url, setUrl] = useState('')
  const [preview, setPreview] = useState<PlaylistPreview | null>(null)
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [title, setTitle] = useState('')
  const [venue, setVenue] = useState<Venue>('watch_party')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function load() {
    if (!url.trim()) return
    setBusy(true)
    setError(null)
    try {
      const p = await lookupPlaylist(url.trim())
      setPreview(p)
      setSelected(new Set(p.items.map((i) => i.id)))
      setTitle(p.playlist.title)
    } catch (err) {
      setPreview(null)
      setError(errMsg(err))
    } finally {
      setBusy(false)
    }
  }

  async function doImport() {
    if (!preview) return
    setBusy(true)
    setError(null)
    try {
      const res = await importSeries(sid, {
        playlist_url: url.trim(),
        title: title.trim(),
        default_venue: venue,
        video_ids: preview.items.filter((i) => selected.has(i.id)).map((i) => i.id),
      })
      onImported(res.series)
    } catch (err) {
      setError(errMsg(err))
      setBusy(false)
    }
  }

  return (
    <div className="form">
      {error && <ErrorBox message={error} />}
      <div className="row">
        <label className="grow">
          Playlist or show URL
          <input
            type="url"
            value={url}
            placeholder="https://www.youtube.com/playlist?list=… or /show/…"
            disabled={busy}
            onChange={(e) => setUrl(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') {
                e.preventDefault()
                void load()
              }
            }}
          />
        </label>
      </div>
      <div className="actions">
        <button type="button" disabled={busy || !url.trim()} onClick={load}>
          {busy && !preview ? 'Loading…' : 'Load preview'}
        </button>
      </div>
      {preview && (
        <>
          <div className="muted small">
            {preview.playlist.title}
            {preview.playlist.channel_title && ` · ${preview.playlist.channel_title}`}
          </div>
          {preview.items.length === 0 ? (
            <p className="muted">This playlist has no importable videos.</p>
          ) : (
            <PlaylistPicker items={preview.items} selected={selected} onChange={setSelected} disabled={busy} />
          )}
          <div className="row">
            <label className="grow">
              Title
              <input value={title} maxLength={300} onChange={(e) => setTitle(e.target.value)} />
            </label>
            <label className="grow">
              Default venue
              <select value={venue} onChange={(e) => setVenue(e.target.value as Venue)}>
                {VENUES.map((x) => (
                  <option key={x} value={x}>
                    {VENUE_LABELS[x]}
                  </option>
                ))}
              </select>
            </label>
          </div>
        </>
      )}
      <div className="actions">
        {preview && (
          <button
            type="button"
            className="primary"
            disabled={busy || selected.size === 0 || !title.trim()}
            onClick={doImport}
          >
            {busy ? 'Importing…' : `Import ${selected.size} video${selected.size === 1 ? '' : 's'}`}
          </button>
        )}
        <button type="button" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
      </div>
    </div>
  )
}
