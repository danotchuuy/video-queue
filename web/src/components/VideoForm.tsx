import { useRef, useState } from 'react'
import { lookupVideo, type Series, type Venue } from '../api'
import { useSession } from '../session'
import { VENUES, VENUE_LABELS, errMsg, parseYouTubeVideoId } from '../util'
import { seriesDefaultVenue, type VideoValues } from '../videoValues'
import { ErrorBox } from './Status'
import Thumbnail from './Thumbnail'

interface Props {
  series: Series[]
  initial: VideoValues
  submitLabel: string
  onSubmit: (values: VideoValues) => Promise<void>
  onCancel: () => void
}

/** Shared by add, edit and approve. */
export default function VideoForm({ series, initial, submitLabel, onSubmit, onCancel }: Props) {
  const [v, setV] = useState<VideoValues>(initial)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const { me } = useSession()
  const [fetching, setFetching] = useState(false)
  const [fetchError, setFetchError] = useState<string | null>(null)
  const autoTried = useRef('')
  const ytId = parseYouTubeVideoId(v.url)

  async function fetchDetails(url: string, auto: boolean) {
    const id = parseYouTubeVideoId(url)
    if (!me.youtube_enabled || !id) return
    if (auto) {
      if (v.title.trim() || autoTried.current === id) return
      autoTried.current = id
    }
    setFetching(true)
    setFetchError(null)
    try {
      const info = await lookupVideo(url.trim())
      const titleSet = v.title.trim() !== ''
      const replace =
        !titleSet || (info.title !== v.title && window.confirm(`Replace the title with "${info.title}"?`))
      setV((prev) => ({
        ...prev,
        title: replace ? info.title.slice(0, 300) : prev.title,
        channel_title: info.channel_title,
        duration_seconds: info.duration_seconds,
      }))
    } catch (err) {
      setFetchError(errMsg(err))
    } finally {
      setFetching(false)
    }
  }

  const set = <K extends keyof VideoValues>(k: K, val: VideoValues[K]) =>
    setV((prev) => ({ ...prev, [k]: val }))

  function pickSeries(raw: string) {
    const id = raw ? Number(raw) : null
    const venue = seriesDefaultVenue(series, id)
    setV((prev) => ({ ...prev, series_id: id, venue: venue ?? prev.venue }))
  }

  async function submit(e: React.SubmitEvent<HTMLFormElement>) {
    e.preventDefault()
    if (!v.title.trim()) {
      setError('Title is required')
      return
    }
    setBusy(true)
    setError(null)
    try {
      await onSubmit(v)
    } catch (err) {
      setError(errMsg(err))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit}>
      {error && <ErrorBox message={error} />}
      <label>
        Series
        <select value={v.series_id ?? ''} onChange={(e) => pickSeries(e.target.value)}>
          <option value="">(none)</option>
          {series.map((s) => (
            <option key={s.id} value={s.id}>
              {s.title}
            </option>
          ))}
        </select>
      </label>
      <div className="row">
        <label className="grow">
          Title
          <input
            value={v.title}
            maxLength={300}
            required
            onChange={(e) => set('title', e.target.value)}
          />
        </label>
        <label className="narrow">
          Episode
          <input value={v.episode} onChange={(e) => set('episode', e.target.value)} />
        </label>
      </div>
      <label>
        URL
        <input
          type="url"
          value={v.url}
          onChange={(e) => setV((prev) => ({ ...prev, url: e.target.value, channel_title: '', duration_seconds: 0 }))}
          onPaste={(e) => {
            const text = e.clipboardData.getData('text')
            if (text) void fetchDetails(text, true)
          }}
          onBlur={(e) => void fetchDetails(e.target.value, true)}
        />
      </label>
      {ytId && (
        <div className="media">
          <Thumbnail videoId={ytId} title={v.title || 'YouTube video'} seconds={v.duration_seconds} />
          <div className="grow">
            {v.channel_title && <div className="small muted">{v.channel_title}</div>}
            {me.youtube_enabled && (
              <button type="button" disabled={fetching} onClick={() => void fetchDetails(v.url, false)}>
                {fetching ? 'Fetching…' : 'Fetch details'}
              </button>
            )}
          </div>
        </div>
      )}
      {fetchError && <ErrorBox message={fetchError} />}
      <div className="row">
        <label className="grow">
          Venue
          <select value={v.venue} onChange={(e) => set('venue', e.target.value as Venue)}>
            {VENUES.map((x) => (
              <option key={x} value={x}>
                {VENUE_LABELS[x]}
              </option>
            ))}
          </select>
        </label>
        <label className="grow">
          Scheduled for
          <input
            type="date"
            value={v.scheduled_for}
            onChange={(e) => set('scheduled_for', e.target.value)}
          />
        </label>
      </div>
      <label>
        Notes
        <textarea rows={2} value={v.notes} onChange={(e) => set('notes', e.target.value)} />
      </label>
      <div className="actions">
        <button type="submit" className="primary" disabled={busy}>
          {busy ? 'Saving…' : submitLabel}
        </button>
        <button type="button" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
      </div>
    </form>
  )
}
