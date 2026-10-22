import { useState } from 'react'
import { Link, useNavigate } from 'react-router'
import { createSeries, deleteSeries, listSeries, listVideos, updateSeries, type Series } from '../api'
import PlaylistImportForm from '../components/PlaylistImportForm'
import SeriesForm from '../components/SeriesForm'
import { ErrorBox, Loading } from '../components/Status'
import Thumbnail from '../components/Thumbnail'
import { useServer, useSession } from '../session'
import { useFetch } from '../useFetch'
import { VENUE_LABELS, errMsg } from '../util'

export default function SeriesList() {
  const { id: sid } = useServer()
  const { me } = useSession()
  const navigate = useNavigate()
  const [fromPlaylist, setFromPlaylist] = useState(false)
  const { data, error, loading, reload } = useFetch(() => listSeries(sid), String(sid))
  const [adding, setAdding] = useState(false)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  // A series waiting for the "also delete its queued videos?" choice.
  const [confirming, setConfirming] = useState<{ id: number; queued: number } | null>(null)
  const [busy, setBusy] = useState(false)

  if (loading && !data) return <Loading />
  if (error && !data) return <ErrorBox message={error} onRetry={reload} />

  // Asks first: if videos of the series are still queued, offer to delete them too.
  async function startRemove(s: Series) {
    setActionError(null)
    setBusy(true)
    try {
      const queued = await listVideos(sid, { status: ['queued'], series_id: s.id })
      if (queued.length > 0) {
        setConfirming({ id: s.id, queued: queued.length })
      } else if (window.confirm(`Delete series "${s.title}"?`)) {
        await deleteSeries(sid, s.id)
        reload()
      }
    } catch (e) {
      setActionError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  async function remove(s: Series, deleteQueued: boolean) {
    setActionError(null)
    setBusy(true)
    try {
      await deleteSeries(sid, s.id, deleteQueued)
      setConfirming(null)
      reload()
    } catch (e) {
      setActionError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <div className="page-head">
        <h1>Series</h1>
        {!adding && (
          <button type="button" className="primary" onClick={() => setAdding(true)}>
            New series
          </button>
        )}
      </div>
      {adding && (
        <section className="card">
          <h2>New series</h2>
          {me.youtube_enabled && (
            <div className="mode-toggle">
              <button type="button" className={fromPlaylist ? '' : 'active'} onClick={() => setFromPlaylist(false)}>
                Manual
              </button>
              <button type="button" className={fromPlaylist ? 'active' : ''} onClick={() => setFromPlaylist(true)}>
                From YouTube playlist or show
              </button>
            </div>
          )}
          {me.youtube_enabled && fromPlaylist ? (
            <PlaylistImportForm
              onCancel={() => setAdding(false)}
              onImported={(s) => navigate(`/s/${sid}/series/${s.id}`)}
            />
          ) : (
            <SeriesForm
              submitLabel="Create"
              onCancel={() => setAdding(false)}
              onSubmit={async (input) => {
                await createSeries(sid, input)
                setAdding(false)
                reload()
              }}
            />
          )}
        </section>
      )}
      {actionError && <ErrorBox message={actionError} />}
      {(data ?? []).length === 0 && <p className="muted">No series yet.</p>}
      {(data ?? []).map((s) => (
        <article key={s.id} className="card">
          {editingId === s.id ? (
            <SeriesForm
              initial={s}
              submitLabel="Save"
              onCancel={() => setEditingId(null)}
              onSubmit={async (input) => {
                await updateSeries(sid, s.id, input)
                setEditingId(null)
                reload()
              }}
            />
          ) : (
            <div className="card-head">
              <div className="media grow">
                <Thumbnail videoId={s.thumbnail_video_id} title={s.title} />
                <div className="grow">
                <div className="card-title">
                  <Link to={`/s/${sid}/series/${s.id}`}>{s.title}</Link>
                </div>
                <div className="muted small">
                  {[s.creator, s.default_venue && VENUE_LABELS[s.default_venue]]
                    .filter(Boolean)
                    .join(' · ')}
                  {s.url && (
                    <>
                      {' · '}
                      <a href={s.url} target="_blank" rel="noreferrer noopener">
                        link
                      </a>
                    </>
                  )}
                </div>
                {s.notes && <div className="small">{s.notes}</div>}
                </div>
              </div>
              <div className="btn-group">
                <button type="button" onClick={() => setEditingId(s.id)}>
                  Edit
                </button>
                <button type="button" className="danger" disabled={busy} onClick={() => startRemove(s)}>
                  Delete
                </button>
              </div>
            </div>
          )}
          {confirming?.id === s.id && (
            <div className="confirm-delete" role="alertdialog" aria-label={`Delete ${s.title}`}>
              <p>
                Delete series <strong>{s.title}</strong>? {confirming.queued}{' '}
                {confirming.queued === 1 ? 'video is' : 'videos are'} still in the queue.
              </p>
              <div className="btn-group">
                <button type="button" className="danger" disabled={busy} onClick={() => remove(s, true)}>
                  Delete series and {confirming.queued} queued {confirming.queued === 1 ? 'video' : 'videos'}
                </button>
                <button type="button" disabled={busy} onClick={() => remove(s, false)}>
                  Delete series, keep videos in queue
                </button>
                <button type="button" disabled={busy} onClick={() => setConfirming(null)}>
                  Cancel
                </button>
              </div>
              <p className="muted small">Videos that were already reacted to are always kept.</p>
            </div>
          )}
        </article>
      ))}
    </>
  )
}
