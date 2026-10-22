import { useState } from 'react'
import type { Series, SeriesInput, Venue } from '../api'
import { VENUES, VENUE_LABELS, errMsg } from '../util'
import { ErrorBox } from './Status'

interface Props {
  initial?: Series
  submitLabel: string
  onSubmit: (input: SeriesInput) => Promise<void>
  onCancel: () => void
}

export default function SeriesForm({ initial, submitLabel, onSubmit, onCancel }: Props) {
  const [title, setTitle] = useState(initial?.title ?? '')
  const [creator, setCreator] = useState(initial?.creator ?? '')
  const [url, setUrl] = useState(initial?.url ?? '')
  const [venue, setVenue] = useState<Venue | ''>(initial?.default_venue ?? '')
  const [notes, setNotes] = useState(initial?.notes ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(e: React.SubmitEvent<HTMLFormElement>) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await onSubmit({
        title: title.trim(),
        creator: creator.trim(),
        url: url.trim(),
        default_venue: venue,
        notes: notes.trim(),
      })
    } catch (err) {
      setError(errMsg(err))
      setBusy(false)
    }
  }

  return (
    <form className="form" onSubmit={submit}>
      {error && <ErrorBox message={error} />}
      <label>
        Title
        <input value={title} required onChange={(e) => setTitle(e.target.value)} />
      </label>
      <div className="row">
        <label className="grow">
          Creator
          <input value={creator} onChange={(e) => setCreator(e.target.value)} />
        </label>
        <label className="grow">
          Default venue
          <select value={venue} onChange={(e) => setVenue(e.target.value as Venue | '')}>
            <option value="">(none)</option>
            {VENUES.map((x) => (
              <option key={x} value={x}>
                {VENUE_LABELS[x]}
              </option>
            ))}
          </select>
        </label>
      </div>
      <label>
        URL
        <input type="url" value={url} onChange={(e) => setUrl(e.target.value)} />
      </label>
      <label>
        Notes
        <textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} />
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
